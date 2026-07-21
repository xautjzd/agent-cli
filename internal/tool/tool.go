// Package tool defines the agent's tool abstraction and its built-in tools.
//
// Each tool is a small, single-responsibility unit (SRP) implementing the
// Tool interface. The agent core depends on the interface and the Registry
// only (DIP), so new tools are pure additions (OCP).
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// Tool is one capability the model may invoke.
type Tool interface {
	// Name is the identifier exposed to the model (e.g. "read_file").
	Name() string
	// Description tells the model when and how to use the tool.
	Description() string
	// Schema returns the JSON Schema of the tool's input object.
	Schema() json.RawMessage
	// Execute runs the tool with the model-supplied JSON arguments and
	// returns the text fed back to the model. Errors are also fed back so
	// the model can self-correct rather than aborting the whole turn.
	Execute(ctx context.Context, input json.RawMessage) (string, error)
}

// Registry holds the tools available to one agent session.
type Registry struct {
	tools map[string]Tool
	order []string
}

// NewRegistry builds a registry from the given tools.
func NewRegistry(tools ...Tool) *Registry {
	r := &Registry{tools: map[string]Tool{}}
	for _, t := range tools {
		r.Register(t)
	}
	return r
}

// Register adds a tool, replacing any previous tool with the same name.
func (r *Registry) Register(t Tool) {
	if _, exists := r.tools[t.Name()]; !exists {
		r.order = append(r.order, t.Name())
	}
	r.tools[t.Name()] = t
}

// Get looks a tool up by name.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// All returns tools in registration order for stable prompt construction.
func (r *Registry) All() []Tool {
	out := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.tools[name])
	}
	return out
}

// Names returns the sorted tool names, useful for diagnostics.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Execute dispatches a call to the named tool. A missing tool or a tool
// error is reported as a normal result string so the model can recover;
// the boolean tells display layers whether the call succeeded.
func (r *Registry) Execute(ctx context.Context, name string, input json.RawMessage) (string, bool) {
	t, ok := r.Get(name)
	if !ok {
		return fmt.Sprintf("Error: unknown tool %q. Available tools: %v", name, r.Names()), false
	}
	out, err := t.Execute(ctx, input)
	if err != nil {
		return "Error: " + err.Error(), false
	}
	if out == "" {
		return "(no output)", true
	}
	return out, true
}

// mustSchema panics on invalid schema literals; used only with compile-time
// constants so a failure is a programming error caught by tests.
func mustSchema(s string) json.RawMessage {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		panic(fmt.Sprintf("invalid tool schema: %v", err))
	}
	return json.RawMessage(s)
}
