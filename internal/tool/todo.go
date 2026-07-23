package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// TodoWrite is the task-planning tool: the model maintains a structured todo
// list to plan and track a multi-step task, which measurably improves
// completion of complex work (the pattern popularized by Claude Code / pi /
// opencode). Each call replaces the whole list; the tool holds it for the
// session so progress persists across turns.
type TodoWrite struct {
	mu    sync.Mutex
	items []TodoItem
}

// TodoItem is one task in the list.
type TodoItem struct {
	// Content is the task in imperative form ("Add the parser").
	Content string `json:"content"`
	// Status is "pending", "in_progress", or "completed".
	Status string `json:"status"`
	// ActiveForm is the present-continuous label shown while in progress
	// ("Adding the parser"); optional, falls back to Content.
	ActiveForm string `json:"activeForm,omitempty"`
}

// Todo status values.
const (
	TodoPending    = "pending"
	TodoInProgress = "in_progress"
	TodoCompleted  = "completed"
)

func (t *TodoWrite) Name() string { return "todo_write" }

func (t *TodoWrite) Description() string {
	return "Create and update a structured todo list to plan and track a multi-step task. " +
		"Use it for non-trivial work: write the plan up front, then each call replaces the whole " +
		"list. Mark exactly one item in_progress before you start it and completed the moment it's " +
		"done — do not batch completions. Skip it for trivial single-step tasks."
}

func (t *TodoWrite) Schema() json.RawMessage {
	return mustSchema(`{
		"type": "object",
		"properties": {
			"todos": {
				"type": "array",
				"description": "The full todo list (replaces the previous one)",
				"items": {
					"type": "object",
					"properties": {
						"content": {"type": "string", "description": "The task, in imperative form"},
						"status": {"type": "string", "enum": ["pending", "in_progress", "completed"]},
						"activeForm": {"type": "string", "description": "Present-continuous label shown while in progress"}
					},
					"required": ["content", "status"]
				}
			}
		},
		"required": ["todos"]
	}`)
}

func (t *TodoWrite) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Todos []TodoItem `json:"todos"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	inProgress := 0
	for i, it := range args.Todos {
		if strings.TrimSpace(it.Content) == "" {
			return "", fmt.Errorf("todo %d has empty content", i+1)
		}
		switch it.Status {
		case TodoPending, TodoInProgress, TodoCompleted:
		default:
			return "", fmt.Errorf("todo %d has invalid status %q (use pending, in_progress, or completed)", i+1, it.Status)
		}
		if it.Status == TodoInProgress {
			inProgress++
		}
	}
	if inProgress > 1 {
		return "", fmt.Errorf("only one todo may be in_progress at a time (found %d)", inProgress)
	}

	t.mu.Lock()
	t.items = args.Todos
	t.mu.Unlock()

	return t.render(), nil
}

// Items returns a copy of the current list (for /todos and the UI).
func (t *TodoWrite) Items() []TodoItem {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]TodoItem, len(t.items))
	copy(out, t.items)
	return out
}

// render draws the list as a colored checklist for the terminal.
func (t *TodoWrite) render() string {
	t.mu.Lock()
	items := t.items
	t.mu.Unlock()
	return RenderTodos(items)
}

// RenderTodos formats a todo list as a checklist. Exported so /todos and the
// UI share one rendering.
func RenderTodos(items []TodoItem) string {
	if len(items) == 0 {
		return "Todo list is empty."
	}
	const (
		dim    = "\033[2m"
		green  = "\033[32m"
		yellow = "\033[33m"
		reset  = "\033[0m"
	)
	var b strings.Builder
	b.WriteString("Todos:\n")
	var pending, active, done int
	for _, it := range items {
		switch it.Status {
		case TodoCompleted:
			done++
			fmt.Fprintf(&b, "  %s✓ %s%s\n", green, it.Content, reset)
		case TodoInProgress:
			active++
			label := it.Content
			if it.ActiveForm != "" {
				label = it.ActiveForm
			}
			fmt.Fprintf(&b, "  %s▶ %s%s\n", yellow, label, reset)
		default:
			pending++
			fmt.Fprintf(&b, "  %s☐ %s%s\n", dim, it.Content, reset)
		}
	}
	fmt.Fprintf(&b, "%s(%d done · %d in progress · %d pending)%s", dim, done, active, pending, reset)
	return b.String()
}
