package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xautjzd/agent-cli/internal/config"
	"github.com/xautjzd/agent-cli/internal/tool"
)

// fakeMCP is an httptest handler implementing the slice of MCP the client
// uses: initialize, the initialized notification, tools/list, and tools/call.
// It answers with plain JSON (the transport's other branch, SSE, is covered
// separately).
func fakeMCP(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req rpcRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("bad request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// Notifications carry no id and expect no response body.
		if req.ID == nil {
			w.Header().Set("Mcp-Session-Id", "sess-123")
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "sess-123")
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": protocolVersion, "serverInfo": map[string]any{"name": "fake"}}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{
				"name":        "echo",
				"description": "Echo the message back.",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"msg": map[string]any{"type": "string"}}},
			}}}
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			json.Unmarshal(mustParams(t, req), &p)
			if p.Name != "echo" {
				result = map[string]any{"content": []map[string]any{{"type": "text", "text": "unknown tool"}}, "isError": true}
				break
			}
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "you said: " + string(p.Arguments)}}}
		default:
			t.Errorf("unexpected method %q", req.Method)
		}
		resp := map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": result}
		json.NewEncoder(w).Encode(resp)
	}
}

func mustParams(t *testing.T, req rpcRequest) []byte {
	t.Helper()
	b, err := json.Marshal(req.Params)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestHTTPClientEndToEnd(t *testing.T) {
	srv := httptest.NewServer(fakeMCP(t))
	defer srv.Close()

	reg := tool.NewRegistry()
	mgr := Connect(context.Background(), map[string]config.MCPServerConfig{
		"fake": {Type: "http", URL: srv.URL},
	}, reg)
	defer mgr.Close()

	if len(mgr.Status) != 1 || !mgr.Status[0].OK() {
		t.Fatalf("connect failed: %+v", mgr.Status)
	}
	// The tool is namespaced and registered.
	name := ToolName("fake", "echo")
	tl, ok := reg.Get(name)
	if !ok {
		t.Fatalf("tool %q not registered; have %v", name, reg.Names())
	}
	if tl.Description() != "Echo the message back." {
		t.Errorf("description = %q", tl.Description())
	}
	// Calling it round-trips through the server.
	out, err := tl.Execute(context.Background(), json.RawMessage(`{"msg":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `you said: {"msg":"hi"}`) {
		t.Errorf("unexpected tool output: %q", out)
	}
}

func TestHTTPToolError(t *testing.T) {
	srv := httptest.NewServer(fakeMCP(t))
	defer srv.Close()
	tr := newHTTPTransport(srv.URL, nil, nil)
	c := &Client{name: "fake", transport: tr}
	if err := c.initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A tool that reports isError surfaces as a Go error to the caller.
	_, err := c.CallTool(context.Background(), "nope", nil)
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("expected tool error, got %v", err)
	}
}

func TestSSEResponse(t *testing.T) {
	// A server that replies to tools/list over an event stream.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req rpcRequest
		json.Unmarshal(body, &req)
		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": protocolVersion}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{"name": "t", "description": "d"}}}
		}
		frame, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": result})
		// Interleave an unrelated event to prove the scanner skips it.
		io.WriteString(w, "event: message\n")
		io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/log\"}\n\n")
		io.WriteString(w, "data: "+string(frame)+"\n\n")
	}))
	defer srv.Close()

	tr := newHTTPTransport(srv.URL, nil, nil)
	c := &Client{name: "sse", transport: tr}
	if err := c.initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "t" {
		t.Fatalf("SSE tools/list wrong: %+v", tools)
	}
}

func TestConnectRecordsFailure(t *testing.T) {
	reg := tool.NewRegistry()
	mgr := Connect(context.Background(), map[string]config.MCPServerConfig{
		"broken": {Type: "http", URL: "http://127.0.0.1:0"},
	}, reg)
	defer mgr.Close()
	if len(mgr.Status) != 1 || mgr.Status[0].OK() {
		t.Fatalf("expected a recorded failure, got %+v", mgr.Status)
	}
	if len(reg.Names()) != 0 {
		t.Errorf("a failed server must contribute no tools, got %v", reg.Names())
	}
}

func TestDisabledServerSkipped(t *testing.T) {
	reg := tool.NewRegistry()
	mgr := Connect(context.Background(), map[string]config.MCPServerConfig{
		"off": {Type: "http", URL: "http://example.invalid", Disabled: true},
	}, reg)
	defer mgr.Close()
	if len(mgr.Status) != 0 {
		t.Errorf("disabled server should be skipped entirely, got %+v", mgr.Status)
	}
}

func TestTransportInference(t *testing.T) {
	cases := []struct {
		cfg  config.MCPServerConfig
		want string
	}{
		{config.MCPServerConfig{Command: "npx"}, "stdio"},
		{config.MCPServerConfig{URL: "https://x/mcp"}, "http"},
		{config.MCPServerConfig{Type: "http", Command: "npx"}, "http"},
		{config.MCPServerConfig{Type: "sse", URL: "https://x"}, "http"},
		{config.MCPServerConfig{}, ""},
	}
	for i, c := range cases {
		if got := c.cfg.Transport(); got != c.want {
			t.Errorf("case %d: Transport() = %q, want %q", i, got, c.want)
		}
	}
}
