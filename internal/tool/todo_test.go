package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestTodoWriteStoresAndRenders(t *testing.T) {
	tw := &TodoWrite{}
	args := `{"todos":[
		{"content":"Add the parser","status":"completed"},
		{"content":"Wire it in","status":"in_progress","activeForm":"Wiring it in"},
		{"content":"Write tests","status":"pending"}
	]}`
	out, err := tw.Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	// Rendered checklist with the right markers and a summary.
	if !strings.Contains(out, "✓ Add the parser") {
		t.Errorf("missing completed marker:\n%s", out)
	}
	if !strings.Contains(out, "▶ Wiring it in") { // uses activeForm when in progress
		t.Errorf("in-progress should use activeForm:\n%s", out)
	}
	if !strings.Contains(out, "☐ Write tests") {
		t.Errorf("missing pending marker:\n%s", out)
	}
	if !strings.Contains(out, "1 done · 1 in progress · 1 pending") {
		t.Errorf("summary wrong:\n%s", out)
	}

	// State persists and is readable.
	items := tw.Items()
	if len(items) != 3 || items[1].Status != TodoInProgress {
		t.Errorf("items not stored: %+v", items)
	}
}

func TestTodoWriteReplacesList(t *testing.T) {
	tw := &TodoWrite{}
	tw.Execute(context.Background(), json.RawMessage(`{"todos":[{"content":"a","status":"pending"}]}`))
	tw.Execute(context.Background(), json.RawMessage(`{"todos":[{"content":"b","status":"completed"},{"content":"c","status":"pending"}]}`))
	items := tw.Items()
	if len(items) != 2 || items[0].Content != "b" {
		t.Errorf("second call should replace the whole list: %+v", items)
	}
}

func TestTodoWriteValidation(t *testing.T) {
	tw := &TodoWrite{}
	cases := []string{
		`{"todos":[{"content":"","status":"pending"}]}`,                                             // empty content
		`{"todos":[{"content":"x","status":"done"}]}`,                                               // invalid status
		`{"todos":[{"content":"a","status":"in_progress"},{"content":"b","status":"in_progress"}]}`, // two in_progress
	}
	for _, c := range cases {
		if _, err := tw.Execute(context.Background(), json.RawMessage(c)); err == nil {
			t.Errorf("expected validation error for %s", c)
		}
	}
}

func TestRenderTodosEmpty(t *testing.T) {
	if RenderTodos(nil) != "Todo list is empty." {
		t.Error("empty render wrong")
	}
}
