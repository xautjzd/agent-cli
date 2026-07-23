package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// diagnosticsSettle is how long to keep waiting for an updated diagnostics
// publish after the first one arrives: servers often emit an empty set
// immediately, then the real results a moment later.
const diagnosticsSettle = 400 * time.Millisecond

// Client is one running language-server process. It speaks JSON-RPC with LSP's
// Content-Length framing over the child's stdio, with a single reader goroutine
// routing responses to waiters and stashing published diagnostics by URI.
type Client struct {
	lang string
	root string

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	writeMu sync.Mutex
	nextID  int64

	mu       sync.Mutex
	pending  map[int64]chan rpcMessage
	diags    map[string][]Diagnostic
	diagSubs map[string][]chan struct{}
	opened   map[string]bool
	closed   bool
	readErr  error
}

// startClient launches the server and completes the LSP initialize handshake,
// leaving the client ready for document requests.
func startClient(ctx context.Context, lang, root, command string, args []string, env map[string]string) (*Client, error) {
	cmd := exec.Command(command, args...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Dir = root
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

	c := newClient(lang, root, cmd, stdin, stdout)
	if err := c.initialize(ctx); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// newClient wires a client to an already-connected transport and starts its
// reader loop. Separated from process spawning so the protocol logic can be
// tested over in-memory pipes.
func newClient(lang, root string, cmd *exec.Cmd, stdin io.WriteCloser, stdout io.Reader) *Client {
	c := &Client{
		lang:     lang,
		root:     root,
		cmd:      cmd,
		stdin:    stdin,
		stdout:   bufio.NewReaderSize(stdout, 1<<20),
		pending:  map[int64]chan rpcMessage{},
		diags:    map[string][]Diagnostic{},
		diagSubs: map[string][]chan struct{}{},
		opened:   map[string]bool{},
	}
	go c.readLoop()
	return c
}

// initialize runs the handshake: an initialize request followed by the
// initialized notification, after which the server accepts document syncs.
func (c *Client) initialize(ctx context.Context) error {
	rootURI := pathToURI(c.root)
	params := map[string]any{
		"processId": os.Getpid(),
		"rootUri":   rootURI,
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"synchronization":    map[string]any{"didSave": true},
				"publishDiagnostics": map[string]any{},
				"hover":              map[string]any{"contentFormat": []string{"markdown", "plaintext"}},
				"definition":         map[string]any{},
				"references":         map[string]any{},
			},
			"workspace": map[string]any{
				"configuration":    true,
				"workspaceFolders": true,
			},
		},
		"workspaceFolders": []map[string]any{{"uri": rootURI, "name": baseName(c.root)}},
	}
	if _, err := c.call(ctx, "initialize", params); err != nil {
		return fmt.Errorf("lsp %s initialize: %w", c.lang, err)
	}
	return c.notify("initialized", map[string]any{})
}

// readLoop reads framed messages and dispatches them: responses to their
// waiter, publishDiagnostics into the per-URI store, and server→client
// requests to a minimal auto-reply so the server never stalls waiting on us.
func (c *Client) readLoop() {
	for {
		msg, err := readMessage(c.stdout)
		if err != nil {
			c.failAll(err)
			return
		}
		switch {
		case msg.ID != nil && msg.Method == "":
			c.deliver(*msg.ID, msg)
		case msg.Method == "textDocument/publishDiagnostics":
			c.onDiagnostics(msg.Params)
		case msg.ID != nil && msg.Method != "":
			c.answerServerRequest(msg)
		}
	}
}

// answerServerRequest replies to the requests gopls and friends make during a
// session (workspace/configuration, client/registerCapability, progress
// creation). We hold no real config, so configuration items get nulls and
// everything else a null result — enough to keep the server moving.
//
// The reply is written from a separate goroutine so the read loop never blocks
// on the write: it must keep reading to receive the responses our own requests
// are waiting on, or the connection deadlocks.
func (c *Client) answerServerRequest(msg rpcMessage) {
	var result any
	if msg.Method == "workspace/configuration" {
		var p struct {
			Items []json.RawMessage `json:"items"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		result = make([]any, len(p.Items))
	}
	go func() { _ = c.write(rpcResponse{JSONRPC: "2.0", ID: msg.ID, Result: result}) }()
}

// onDiagnostics stores the latest diagnostics for a URI and wakes any waiter.
func (c *Client) onDiagnostics(params json.RawMessage) {
	var p publishDiagnosticsParams
	if json.Unmarshal(params, &p) != nil {
		return
	}
	c.mu.Lock()
	c.diags[p.URI] = p.Diagnostics
	subs := c.diagSubs[p.URI]
	delete(c.diagSubs, p.URI)
	c.mu.Unlock()
	for _, ch := range subs {
		close(ch)
	}
}

func (c *Client) deliver(id int64, msg rpcMessage) {
	c.mu.Lock()
	ch, ok := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if ok {
		ch <- msg
	}
}

func (c *Client) failAll(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.readErr = err
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
}

// call sends a request and waits for its response.
func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := atomic.AddInt64(&c.nextID, 1)
	ch := make(chan rpcMessage, 1)

	c.mu.Lock()
	if c.closed || c.readErr != nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("lsp %s closed: %v", c.lang, c.readErr)
	}
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.write(rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: params}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case msg, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("lsp %s server closed the connection: %v", c.lang, c.readErr)
		}
		if msg.Error != nil {
			return nil, msg.Error
		}
		return msg.Result, nil
	}
}

func (c *Client) notify(method string, params any) error {
	return c.write(rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
}

// write frames and sends one JSON-RPC value with a Content-Length header.
func (c *Client) write(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := io.WriteString(c.stdin, frame); err != nil {
		return err
	}
	_, err = c.stdin.Write(data)
	return err
}

// ensureOpen sends textDocument/didOpen once per URI, giving the server the
// file's current contents so requests and diagnostics have something to work
// with.
func (c *Client) ensureOpen(uri, languageID, text string) error {
	c.mu.Lock()
	if c.opened[uri] {
		c.mu.Unlock()
		return nil
	}
	c.opened[uri] = true
	c.mu.Unlock()

	return c.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        uri,
			"languageId": languageID,
			"version":    1,
			"text":       text,
		},
	})
}

// Diagnostics opens the document and waits briefly for the server to publish
// diagnostics for it, returning the latest set.
func (c *Client) Diagnostics(ctx context.Context, uri, languageID, text string) ([]Diagnostic, error) {
	sub := c.subscribeDiag(uri)
	if err := c.ensureOpen(uri, languageID, text); err != nil {
		return nil, err
	}
	select {
	case <-sub:
	case <-ctx.Done():
		return c.storedDiag(uri), nil
	}
	// Give the server a moment to replace an initial empty publish with the
	// real results before reading them back.
	time.Sleep(diagnosticsSettle)
	return c.storedDiag(uri), nil
}

func (c *Client) subscribeDiag(uri string) chan struct{} {
	ch := make(chan struct{})
	c.mu.Lock()
	c.diagSubs[uri] = append(c.diagSubs[uri], ch)
	c.mu.Unlock()
	return ch
}

func (c *Client) storedDiag(uri string) []Diagnostic {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.diags[uri]
}

// References returns every reference to the symbol at pos (declaration
// included), after ensuring the document is open.
func (c *Client) References(ctx context.Context, uri, languageID, text string, pos Position) ([]Location, error) {
	if err := c.ensureOpen(uri, languageID, text); err != nil {
		return nil, err
	}
	raw, err := c.call(ctx, "textDocument/references", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     pos,
		"context":      map[string]any{"includeDeclaration": true},
	})
	if err != nil {
		return nil, err
	}
	var locs []Location
	_ = json.Unmarshal(raw, &locs)
	return locs, nil
}

// Definition returns the definition location(s) of the symbol at pos. The
// server may answer with a single Location or an array; both are handled.
func (c *Client) Definition(ctx context.Context, uri, languageID, text string, pos Position) ([]Location, error) {
	if err := c.ensureOpen(uri, languageID, text); err != nil {
		return nil, err
	}
	raw, err := c.call(ctx, "textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     pos,
	})
	if err != nil {
		return nil, err
	}
	return parseLocations(raw), nil
}

// Hover returns the server's hover text (type signature and documentation) for
// the symbol at pos, already flattened to plain text.
func (c *Client) Hover(ctx context.Context, uri, languageID, text string, pos Position) (string, error) {
	if err := c.ensureOpen(uri, languageID, text); err != nil {
		return "", err
	}
	raw, err := c.call(ctx, "textDocument/hover", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     pos,
	})
	if err != nil {
		return "", err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var hov hoverResult
	if json.Unmarshal(raw, &hov) != nil {
		return "", nil
	}
	return flattenHover(hov.Contents), nil
}

// Close shuts the server down and terminates the process.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	// A quick, best-effort graceful shutdown; the process is killed regardless.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	_, _ = c.call(ctx, "shutdown", nil)
	_ = c.notify("exit", nil)
	cancel()

	_ = c.stdin.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	_ = c.cmd.Wait()
	return nil
}

// readMessage reads one Content-Length-framed JSON-RPC message.
func readMessage(r *bufio.Reader) (rpcMessage, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return rpcMessage{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // end of headers
		}
		if key, val, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(key), "Content-Length") {
			length, _ = strconv.Atoi(strings.TrimSpace(val))
		}
	}
	if length < 0 {
		return rpcMessage{}, fmt.Errorf("lsp: missing Content-Length header")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return rpcMessage{}, err
	}
	var msg rpcMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return rpcMessage{}, fmt.Errorf("lsp: bad message: %w", err)
	}
	return msg, nil
}
