// Package lsp provides a minimal Language Server Protocol client so the agent
// can navigate and understand code the way a human's editor does: jump to a
// definition, list references, read hover type/documentation, and — most
// valuable after an edit — surface compiler/linter diagnostics.
//
// The design mirrors the MCP package: one child process per language server,
// JSON-RPC over stdio (here with LSP's Content-Length framing), a single
// reader goroutine demultiplexing responses and server notifications. A
// Manager routes each file to the right server by extension and starts servers
// lazily, so nothing runs until a language-aware tool is actually used. The
// capabilities are exposed to the model as ordinary tools (DIP: the lsp
// package implements tool.Tool, the agent depends only on the interface).
package lsp

import "encoding/json"

// Position is a zero-based location in a document. Character is a UTF-16 code
// unit offset, per the LSP spec.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is a span between two positions.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Location is a range within a specific document.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// Diagnostic is one problem the server reported for a document.
type Diagnostic struct {
	Range    Range           `json:"range"`
	Severity int             `json:"severity"` // 1 error, 2 warning, 3 info, 4 hint
	Code     json.RawMessage `json:"code,omitempty"`
	Source   string          `json:"source,omitempty"`
	Message  string          `json:"message"`
}

// SeverityName renders a diagnostic severity as a short label.
func SeverityName(sev int) string {
	switch sev {
	case 1:
		return "error"
	case 2:
		return "warning"
	case 3:
		return "info"
	case 4:
		return "hint"
	default:
		return "diagnostic"
	}
}

// --- JSON-RPC envelope --------------------------------------------------------

// rpcRequest is an outgoing request or notification (no ID → notification).
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcResponse is an outgoing reply to a server-initiated request.
type rpcResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id"`
	Result  any    `json:"result"`
}

// rpcError is a JSON-RPC error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return e.Message }

// rpcMessage is any incoming frame: a response (ID+Result/Error), a server
// notification (Method, no ID), or a server→client request (Method+ID).
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// publishDiagnosticsParams is the payload of textDocument/publishDiagnostics.
type publishDiagnosticsParams struct {
	URI         string       `json:"uri"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// hoverResult is the (partial) response to textDocument/hover.
type hoverResult struct {
	Contents json.RawMessage `json:"contents"`
	Range    *Range          `json:"range,omitempty"`
}
