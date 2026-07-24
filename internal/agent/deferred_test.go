package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xautjzd/agent-cli/internal/provider"
	"github.com/xautjzd/agent-cli/internal/tool"
)

// namedTool is a trivial deferred tool used to observe advertisement.
type namedTool struct{ name string }

func (n namedTool) Name() string            { return n.name }
func (n namedTool) Description() string     { return n.name + " desc" }
func (n namedTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (n namedTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

func advertised(req provider.Request) map[string]bool {
	m := map[string]bool{}
	for _, t := range req.Tools {
		m[t.Name] = true
	}
	return m
}

// With an Activation set, deferred tools stay hidden until the model loads
// them via search_tools, at which point they appear in the advertised set.
func TestDeferredToolsHiddenUntilActivated(t *testing.T) {
	reg := tool.NewRegistry(&echoTool{})
	reg.RegisterDeferred(namedTool{name: "mcp__srv__do"})
	act := &tool.Activation{}
	reg.Register(&tool.SearchTools{Registry: reg, Activation: act})

	fake := &fakeProvider{responses: []provider.Response{
		{Message: provider.Message{
			Role: provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{{
				ID: "c1", Type: "function",
				Function: provider.FunctionCall{
					Name:      "search_tools",
					Arguments: `{"names":["mcp__srv__do"]}`,
				},
			}},
		}},
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "done"}},
	}}

	ag := New(fake, "m", reg, "sys", nil, 10)
	ag.Activation = act
	if _, err := ag.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}

	if len(fake.requests) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(fake.requests))
	}
	first := advertised(fake.requests[0])
	if first["mcp__srv__do"] {
		t.Error("deferred tool must not be advertised before activation")
	}
	if !first["echo"] || !first["search_tools"] {
		t.Errorf("core tools missing from first request: %v", first)
	}
	second := advertised(fake.requests[1])
	if !second["mcp__srv__do"] {
		t.Errorf("deferred tool must be advertised after activation: %v", second)
	}
}

// Without an Activation, every registered tool is advertised (the behavior
// when no MCP tools were deferred).
func TestNoActivationAdvertisesAll(t *testing.T) {
	reg := tool.NewRegistry(&echoTool{})
	reg.RegisterDeferred(namedTool{name: "mcp__srv__do"})

	fake := &fakeProvider{}
	ag := New(fake, "m", reg, "sys", nil, 10)
	if _, err := ag.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	got := advertised(fake.requests[0])
	if !got["echo"] || !got["mcp__srv__do"] {
		t.Errorf("all tools should be advertised without Activation: %v", got)
	}
}
