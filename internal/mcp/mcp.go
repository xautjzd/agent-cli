// Package mcp is a minimal Model Context Protocol client. It connects to MCP
// servers over one of two transports — a child process speaking newline-
// delimited JSON-RPC on stdio, or an HTTP endpoint speaking the Streamable
// HTTP transport — lists their tools, and invokes them.
//
// The design follows the same interface-first discipline as the rest of the
// codebase: a small transport interface (DIP) isolates the two wire formats
// (SRP), and the Client depends only on that interface, so a third transport
// would be a pure addition (OCP). The package deliberately implements only the
// slice of MCP the agent needs — initialize, tools/list, tools/call — rather
// than the full spec.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/xautjzd/agent-cli/internal/log"
	"github.com/xautjzd/agent-cli/internal/version"
)

// protocolVersion is the MCP revision this client negotiates.
const protocolVersion = "2025-06-18"

// transport is one bidirectional JSON-RPC channel to a server.
type transport interface {
	// call sends a request and blocks for the matching response.
	call(ctx context.Context, method string, params any) (json.RawMessage, error)
	// notify sends a fire-and-forget notification (no response expected).
	notify(ctx context.Context, method string, params any) error
	// Close shuts the transport down, terminating any child process.
	Close() error
}

// ToolInfo is a tool advertised by a server via tools/list.
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Client is a connected MCP session over one transport.
type Client struct {
	name      string // the local server name, for diagnostics and tool prefixing
	transport transport

	mu     sync.Mutex
	tools  []ToolInfo
	closed bool
}

// rpcError is a JSON-RPC 2.0 error object.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("mcp rpc error %d: %s", e.Code, e.Message) }

// rpcRequest is an outgoing JSON-RPC request or notification. ID is omitted
// (making it a notification) when nil.
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcResponse is an incoming JSON-RPC response frame.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

// Name returns the server's local name.
func (c *Client) Name() string { return c.name }

// initialize performs the MCP handshake: the initialize request followed by
// the notifications/initialized acknowledgement the spec requires.
func (c *Client) initialize(ctx context.Context) error {
	log.Debug("mcp", "%s: initialize handshake", c.name)
	params := map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "agent-cli",
			"version": version.Version,
		},
	}
	if _, err := c.transport.call(ctx, "initialize", params); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	if err := c.transport.notify(ctx, "notifications/initialized", nil); err != nil {
		return fmt.Errorf("initialized notification: %w", err)
	}
	return nil
}

// ListTools fetches and caches the server's tool catalog.
func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	raw, err := c.transport.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}
	var out struct {
		Tools []ToolInfo `json:"tools"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode tools/list: %w", err)
	}
	c.mu.Lock()
	c.tools = out.Tools
	c.mu.Unlock()
	return out.Tools, nil
}

// CallTool invokes a tool and returns its result rendered as text. Both a
// tool-level error (isError) and transport errors are surfaced to the caller;
// the tool adapter decides how to present them to the model.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	log.Debug("mcp", "%s: CallTool %s, args=%d bytes", c.name, name, len(args))
	params := map[string]any{"name": name}
	if len(args) > 0 {
		params["arguments"] = json.RawMessage(args)
	} else {
		params["arguments"] = map[string]any{}
	}
	raw, err := c.transport.call(ctx, "tools/call", params)
	if err != nil {
		return "", err
	}
	var res toolResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("decode tools/call: %w", err)
	}
	text := res.text()
	if res.IsError {
		return "", fmt.Errorf("tool reported an error: %s", text)
	}
	return text, nil
}

// Close ends the session, terminating a child process if any.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	return c.transport.Close()
}

// toolResult is the result payload of tools/call. Only the text of content
// blocks is rendered; non-text blocks are summarized so the model still learns
// they were returned.
type toolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

func (r toolResult) text() string {
	var b []byte
	for i, c := range r.Content {
		if i > 0 {
			b = append(b, '\n')
		}
		if c.Type == "text" {
			b = append(b, c.Text...)
		} else {
			b = append(b, fmt.Sprintf("[%s content]", c.Type)...)
		}
	}
	return string(b)
}
