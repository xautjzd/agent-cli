package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

// httpTransport speaks the MCP Streamable HTTP transport: every JSON-RPC
// message is POSTed to a single endpoint. The server answers either with a
// plain application/json body or with a text/event-stream (SSE) carrying one
// or more JSON-RPC frames; this transport handles both and correlates the
// response by request id. A session id handed back on initialize (via the
// Mcp-Session-Id header) is echoed on every later request.
type httpTransport struct {
	url     string
	headers map[string]string
	client  *http.Client

	nextID    int64
	mu        sync.Mutex
	sessionID string
}

func newHTTPTransport(url string, headers map[string]string, client *http.Client) *httpTransport {
	if client == nil {
		client = http.DefaultClient
	}
	return &httpTransport{url: url, headers: headers, client: client}
}

func (t *httpTransport) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := atomic.AddInt64(&t.nextID, 1)
	return t.send(ctx, rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: params}, id)
}

func (t *httpTransport) notify(ctx context.Context, method string, params any) error {
	_, err := t.send(ctx, rpcRequest{JSONRPC: "2.0", Method: method, Params: params}, 0)
	return err
}

// send POSTs one frame and, when expectID != 0, returns the result of the
// matching response. A notification (expectID == 0) discards the body.
func (t *httpTransport) send(ctx context.Context, frame rpcRequest, expectID int64) (json.RawMessage, error) {
	body, err := json.Marshal(frame)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	t.mu.Lock()
	sid := t.sessionID
	t.mu.Unlock()
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Capture a server-assigned session id (sent on the initialize response).
	if got := resp.Header.Get("Mcp-Session-Id"); got != "" {
		t.mu.Lock()
		t.sessionID = got
		t.mu.Unlock()
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	if expectID == 0 {
		return nil, nil // notification: nothing to read
	}

	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		return readSSE(resp.Body, expectID)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return decodeFrame(data, expectID)
}

// readSSE scans a Server-Sent Events stream for the JSON-RPC frame whose id
// matches the request, returning as soon as it is found.
func readSSE(r io.Reader, expectID int64) (json.RawMessage, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if res, err := decodeFrame([]byte(payload), expectID); err == nil {
			return res, nil
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("no response for request id %d in event stream", expectID)
}

// decodeFrame parses a single JSON-RPC response frame and returns its result,
// erroring unless the frame's id matches the expected request.
func decodeFrame(data []byte, expectID int64) (json.RawMessage, error) {
	var resp rpcResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	if resp.ID == nil || *resp.ID != expectID {
		return nil, fmt.Errorf("mismatched response id")
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

func (t *httpTransport) Close() error { return nil }
