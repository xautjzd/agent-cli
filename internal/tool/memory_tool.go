package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/xautjzd/agent-cli/internal/memory"
)

// Remember persists a project-scoped fact for future sessions. Saved
// memories are injected into the system prompt of every later session.
type Remember struct {
	Store memory.Store
}

func (t *Remember) Name() string { return "remember" }

func (t *Remember) Description() string {
	return "Save a project-scoped memory for future sessions: user preferences, project conventions, " +
		"or decisions not derivable from the code. Use a short kebab-case name; saving to an existing name overwrites it."
}

func (t *Remember) Schema() json.RawMessage {
	return mustSchema(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "Kebab-case slug identifying the memory, e.g. \"api-error-format\""},
			"content": {"type": "string", "description": "The fact to remember, written so it is useful without conversation context"}
		},
		"required": ["name", "content"]
	}`)
}

func (t *Remember) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if err := t.Store.Save(args.Name, args.Content); err != nil {
		return "", err
	}
	return fmt.Sprintf("Memory %q saved.", args.Name), nil
}

// ForgetMemory deletes a saved memory that turned out to be wrong or stale.
type ForgetMemory struct {
	Store memory.Store
}

func (t *ForgetMemory) Name() string { return "forget" }

func (t *ForgetMemory) Description() string {
	return "Delete a previously saved project memory by name when it is wrong or no longer relevant."
}

func (t *ForgetMemory) Schema() json.RawMessage {
	return mustSchema(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "Name of the memory to delete"}
		},
		"required": ["name"]
	}`)
}

func (t *ForgetMemory) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if err := t.Store.Delete(args.Name); err != nil {
		return "", err
	}
	return fmt.Sprintf("Memory %q deleted.", args.Name), nil
}
