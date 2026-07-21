package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// emptySchema is used when a server advertises a tool without an input schema;
// the model needs a valid object schema to call it.
var emptySchema = json.RawMessage(`{"type":"object","properties":{}}`)

// toolAdapter presents one MCP tool as the agent's tool.Tool. It lives in this
// package so mcp owns the mapping from the MCP wire shape to the agent's tool
// abstraction; the agent core still depends only on tool.Tool (DIP).
//
// The exposed name is namespaced as "mcp__<server>__<tool>" (Claude Code's
// convention) so tools from different servers never collide and the model can
// tell where a capability comes from.
type toolAdapter struct {
	client *Client
	info   ToolInfo
	name   string
}

// ToolName builds the namespaced tool name for a server/tool pair.
func ToolName(server, tool string) string {
	return fmt.Sprintf("mcp__%s__%s", server, tool)
}

func (a *toolAdapter) Name() string { return a.name }

func (a *toolAdapter) Description() string {
	if a.info.Description == "" {
		return fmt.Sprintf("MCP tool %q from server %q.", a.info.Name, a.client.Name())
	}
	return a.info.Description
}

func (a *toolAdapter) Schema() json.RawMessage {
	if len(a.info.InputSchema) == 0 {
		return emptySchema
	}
	return a.info.InputSchema
}

func (a *toolAdapter) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	return a.client.CallTool(ctx, a.info.Name, input)
}
