package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/xautjzd/agent-cli/internal/config"
	"github.com/xautjzd/agent-cli/internal/tool"
)

// TestStdioHelperProcess is not a real test: when MCP_STDIO_HELPER=1 it acts as
// a minimal MCP server speaking newline-delimited JSON-RPC on stdin/stdout, so
// the stdio transport can be exercised against a genuine child process.
func TestStdioHelperProcess(t *testing.T) {
	if os.Getenv("MCP_STDIO_HELPER") != "1" {
		return
	}
	// It also proves env vars reach the child: the tool echoes TOKEN.
	token := os.Getenv("TOKEN")
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for in.Scan() {
		var req rpcRequest
		if json.Unmarshal(in.Bytes(), &req) != nil || req.ID == nil {
			continue // ignore notifications
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": protocolVersion}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{
				"name": "whoami", "description": "return the token",
			}}}
		case "tools/call":
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "token=" + token}}}
		}
		out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": result})
		fmt.Fprintf(os.Stdout, "%s\n", out)
	}
	os.Exit(0)
}

func TestStdioClientEndToEnd(t *testing.T) {
	reg := tool.NewRegistry()
	mgr := Connect(context.Background(), map[string]config.MCPServerConfig{
		"local": {
			Command: os.Args[0],
			Args:    []string{"-test.run=TestStdioHelperProcess"},
			Env:     map[string]string{"MCP_STDIO_HELPER": "1", "TOKEN": "s3cret"},
		},
	}, reg)
	defer mgr.Close()

	if len(mgr.Status) != 1 || !mgr.Status[0].OK() {
		t.Fatalf("stdio connect failed: %+v", mgr.Status)
	}
	tl, ok := reg.Get(ToolName("local", "whoami"))
	if !ok {
		t.Fatalf("stdio tool not registered; have %v", reg.Names())
	}
	out, err := tl.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "token=s3cret") {
		t.Errorf("stdio env not propagated / wrong output: %q", out)
	}
}
