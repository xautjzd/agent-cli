package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"testing"
	"time"
)

// newPipeClient wires a Client to a fake server over in-memory pipes and runs
// the server's dispatch loop until the client closes its stdin, so the protocol
// logic can be exercised without spawning a real language server.
func newPipeClient(t *testing.T, handle func(w io.Writer, msg rpcMessage)) *Client {
	t.Helper()
	serverIn, clientOut := io.Pipe() // client stdin  → server reads
	clientIn, serverOut := io.Pipe() // server writes → client stdout

	c := newClient("go", "/root", &exec.Cmd{}, clientOut, clientIn)

	go func() {
		r := bufio.NewReader(serverIn)
		for {
			msg, err := readMessage(r)
			if err != nil {
				return
			}
			handle(serverOut, msg)
		}
	}()
	t.Cleanup(func() { _ = clientOut.Close(); _ = serverOut.Close() })
	return c
}

func writeFrame(w io.Writer, v any) {
	data, _ := json.Marshal(v)
	fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(data))
	_, _ = w.Write(data)
}

func TestReferencesRoundTrip(t *testing.T) {
	sawDidOpen := make(chan struct{}, 1)
	c := newPipeClient(t, func(w io.Writer, msg rpcMessage) {
		switch msg.Method {
		case "textDocument/didOpen":
			select {
			case sawDidOpen <- struct{}{}:
			default:
			}
		case "textDocument/references":
			writeFrame(w, rpcResponse{JSONRPC: "2.0", ID: msg.ID, Result: []Location{
				{URI: "file:///root/a.go", Range: Range{Start: Position{Line: 3, Character: 5}}},
			}})
		}
	})
	defer c.Close()

	locs, err := c.References(context.Background(), "file:///root/a.go", "go", "src", Position{Line: 3, Character: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(locs) != 1 || locs[0].Range.Start.Line != 3 {
		t.Errorf("references = %+v", locs)
	}
	// The document must have been opened before the request.
	select {
	case <-sawDidOpen:
	default:
		t.Error("references did not open the document first")
	}
}

func TestDiagnosticsPublish(t *testing.T) {
	c := newPipeClient(t, func(w io.Writer, msg rpcMessage) {
		if msg.Method != "textDocument/didOpen" {
			return
		}
		var p struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		writeFrame(w, rpcRequest{JSONRPC: "2.0", Method: "textDocument/publishDiagnostics",
			Params: publishDiagnosticsParams{
				URI: p.TextDocument.URI,
				Diagnostics: []Diagnostic{
					{Range: Range{Start: Position{Line: 2, Character: 4}}, Severity: 1, Message: "undefined: Foo"},
				},
			}})
	})
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	diags, err := c.Diagnostics(ctx, "file:///root/a.go", "go", "package main")
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 1 || diags[0].Message != "undefined: Foo" || diags[0].Severity != 1 {
		t.Errorf("diagnostics = %+v", diags)
	}
}

// TestServerRequestAutoReply verifies the client answers a server-initiated
// workspace/configuration request with one null per item, so a server like
// gopls (which blocks on that reply) keeps running.
func TestServerRequestAutoReply(t *testing.T) {
	items := make(chan int, 1)
	c := newPipeClient(t, func(w io.Writer, msg rpcMessage) {
		switch {
		case msg.Method == "textDocument/didOpen":
			// Server asks the client for configuration (2 items).
			writeFrame(w, rpcRequest{JSONRPC: "2.0", ID: idPtr(1), Method: "workspace/configuration",
				Params: map[string]any{"items": []any{map[string]any{}, map[string]any{}}}})
		case msg.Method == "textDocument/hover":
			writeFrame(w, rpcResponse{JSONRPC: "2.0", ID: msg.ID, Result: hoverResult{Contents: json.RawMessage(`"info"`)}})
		case msg.ID != nil && msg.Method == "":
			// The client's reply to our configuration request.
			var arr []json.RawMessage
			_ = json.Unmarshal(msg.Result, &arr)
			select {
			case items <- len(arr):
			default:
			}
		}
	})
	defer c.Close()

	// Hover drives a didOpen, which makes the fake server issue the
	// configuration request the client must auto-answer.
	if _, err := c.Hover(context.Background(), "file:///root/a.go", "go", "src", Position{}); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-items:
		if n != 2 {
			t.Errorf("configuration reply items = %d, want 2 nulls", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client did not answer workspace/configuration")
	}
}

func idPtr(n int64) *int64 { return &n }
