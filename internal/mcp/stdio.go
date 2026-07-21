package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
)

// stdioTransport speaks newline-delimited JSON-RPC to a child process over its
// stdin/stdout, the MCP stdio transport. A single reader goroutine demuxes
// responses to the caller waiting on each request id, so concurrent calls and
// interleaved server notifications are handled without corrupting the stream.
type stdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	nextID  int64
	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[int64]chan rpcResponse
	closed  bool
	readErr error
}

// newStdioTransport launches the server process and starts its reader loop.
func newStdioTransport(ctx context.Context, command string, args []string, env map[string]string) (*stdioTransport, error) {
	cmd := exec.Command(command, args...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	// The server's stderr is its own diagnostic channel; forward it so
	// startup failures are visible rather than silently swallowed.
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %q: %w", command, err)
	}
	t := &stdioTransport{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReaderSize(stdout, 1<<20),
		pending: map[int64]chan rpcResponse{},
	}
	go t.readLoop()
	return t, nil
}

// readLoop reads one JSON-RPC frame per line and routes responses to waiters.
// Frames without an id (server notifications/requests) are ignored: this
// client advertises no capabilities, so servers have nothing to ask of it.
func (t *stdioTransport) readLoop() {
	for {
		line, err := t.stdout.ReadBytes('\n')
		if len(line) > 0 {
			var resp rpcResponse
			if json.Unmarshal(line, &resp) == nil && resp.ID != nil {
				t.deliver(*resp.ID, resp)
			}
		}
		if err != nil {
			t.failAll(err)
			return
		}
	}
}

func (t *stdioTransport) deliver(id int64, resp rpcResponse) {
	t.mu.Lock()
	ch, ok := t.pending[id]
	delete(t.pending, id)
	t.mu.Unlock()
	if ok {
		ch <- resp
	}
}

// failAll wakes every pending caller when the stream ends, so a crashed server
// surfaces as an error instead of a hang.
func (t *stdioTransport) failAll(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.readErr = err
	for id, ch := range t.pending {
		close(ch)
		delete(t.pending, id)
	}
}

func (t *stdioTransport) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := atomic.AddInt64(&t.nextID, 1)
	ch := make(chan rpcResponse, 1)

	t.mu.Lock()
	if t.closed || t.readErr != nil {
		t.mu.Unlock()
		return nil, fmt.Errorf("mcp stdio closed: %v", t.readErr)
	}
	t.pending[id] = ch
	t.mu.Unlock()

	if err := t.write(rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: params}); err != nil {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return nil, err
	}

	select {
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return nil, ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("mcp server closed the connection: %v", t.readErr)
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

func (t *stdioTransport) notify(_ context.Context, method string, params any) error {
	return t.write(rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
}

// write serializes and sends one frame, guarded so concurrent callers never
// interleave bytes on stdin.
func (t *stdioTransport) write(req rpcRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	_, err = t.stdin.Write(data)
	return err
}

func (t *stdioTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.mu.Unlock()

	_ = t.stdin.Close()
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	_ = t.cmd.Wait()
	return nil
}
