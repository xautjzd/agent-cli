package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// stubTool is a minimal Tool for exercising registry and meta-tool behavior.
type stubTool struct {
	name   string
	desc   string
	schema string
}

func (s stubTool) Name() string            { return s.name }
func (s stubTool) Description() string     { return s.desc }
func (s stubTool) Schema() json.RawMessage { return json.RawMessage(s.schema) }
func (s stubTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

func newStub(name string) stubTool {
	return stubTool{name: name, desc: name + " description", schema: `{"type":"object"}`}
}

func TestRegistryCoreAndDeferred(t *testing.T) {
	r := NewRegistry(newStub("read"), newStub("bash"))
	r.RegisterDeferred(newStub("mcp__srv__foo"))
	r.RegisterDeferred(newStub("mcp__srv__bar"))

	if got := len(r.Core()); got != 2 {
		t.Fatalf("Core() = %d tools, want 2", got)
	}
	if got := len(r.Deferred()); got != 2 {
		t.Fatalf("Deferred() = %d tools, want 2", got)
	}
	if !r.IsDeferred("mcp__srv__foo") || r.IsDeferred("read") {
		t.Fatalf("IsDeferred wrong: foo=%v read=%v",
			r.IsDeferred("mcp__srv__foo"), r.IsDeferred("read"))
	}
	// All() still returns everything, in registration order.
	if got := len(r.All()); got != 4 {
		t.Fatalf("All() = %d, want 4", got)
	}
	// Re-registering a deferred tool as core clears the deferred flag.
	r.Register(newStub("mcp__srv__foo"))
	if r.IsDeferred("mcp__srv__foo") {
		t.Fatalf("Register should clear deferred flag")
	}
}

func TestSearchToolsByName(t *testing.T) {
	r := NewRegistry(newStub("read"))
	r.RegisterDeferred(stubTool{name: "mcp__db__query", desc: "run SQL", schema: `{"type":"object","properties":{"sql":{"type":"string"}}}`})
	act := &Activation{}
	st := &SearchTools{Registry: r, Activation: act}

	out, err := runTool(t, st, `{"names":["mcp__db__query"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "mcp__db__query") || !strings.Contains(out, "run SQL") {
		t.Fatalf("output missing tool details: %q", out)
	}
	if !strings.Contains(out, `"sql"`) {
		t.Fatalf("output missing schema: %q", out)
	}
	if !act.IsActive("mcp__db__query") {
		t.Fatal("tool was not activated after search")
	}
}

func TestSearchToolsByQuery(t *testing.T) {
	r := NewRegistry()
	r.RegisterDeferred(stubTool{name: "mcp__gh__issues", desc: "list GitHub issues", schema: `{"type":"object"}`})
	r.RegisterDeferred(stubTool{name: "mcp__db__query", desc: "run SQL", schema: `{"type":"object"}`})
	act := &Activation{}
	st := &SearchTools{Registry: r, Activation: act}

	out, err := runTool(t, st, `{"query":"github"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "mcp__gh__issues") {
		t.Fatalf("query should match GitHub tool: %q", out)
	}
	if act.IsActive("mcp__db__query") {
		t.Fatal("unrelated tool should not be activated")
	}
}

func TestSearchToolsNoMatchListsAvailable(t *testing.T) {
	r := NewRegistry()
	r.RegisterDeferred(newStub("mcp__db__query"))
	st := &SearchTools{Registry: r, Activation: &Activation{}}

	out, err := runTool(t, st, `{"names":["mcp__db__typo"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "mcp__db__query") {
		t.Fatalf("no-match reply should list available tools: %q", out)
	}
}

func TestSearchToolsRequiresInput(t *testing.T) {
	st := &SearchTools{Registry: NewRegistry(), Activation: &Activation{}}
	if _, err := runTool(t, st, `{}`); err == nil {
		t.Fatal("empty input should error")
	}
}
