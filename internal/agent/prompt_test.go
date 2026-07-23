package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xautjzd/agent-cli/internal/provider"
	"github.com/xautjzd/agent-cli/internal/tool"
)

// The system prompt must be byte-stable — no date/time — so it caches
// indefinitely (even across a day boundary for long-lived prefix caches).
func TestSystemPromptHasNoDate(t *testing.T) {
	p := (&PromptBuilder{WorkDir: "/proj"}).Build()
	for _, v := range []string{"2024", "2025", "2026", "Today's date"} {
		if strings.Contains(p, v) {
			t.Errorf("system prompt must not contain volatile data (%q):\n%s", v, p)
		}
	}
}

// The date is injected as a separate note right AFTER the system prompt, at
// request time — not stored in history, not inside the system prompt. This
// keeps the static prefix cacheable while only the tiny date note changes.
func TestDateNoteAfterSystemPrompt(t *testing.T) {
	fake := &fakeProvider{responses: []provider.Response{
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "ok"}},
	}}
	ag := New(fake, "m", tool.NewRegistry(), "STATIC SYSTEM PROMPT", nil, 3)
	ag.Now = func() time.Time { return time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC) }

	if _, err := ag.Run(context.Background(), "latest news this week?"); err != nil {
		t.Fatal(err)
	}
	// The request the provider saw: [system, date-note, user].
	msgs := fake.requests[0].Messages
	if len(msgs) != 3 {
		t.Fatalf("expected system + date note + user, got %d messages", len(msgs))
	}
	if msgs[0].Content != "STATIC SYSTEM PROMPT" {
		t.Errorf("system prompt must be untouched: %q", msgs[0].Content)
	}
	if msgs[1].Role != provider.RoleSystem || msgs[1].Content != "Today's date: 2026-07-22" {
		t.Errorf("expected a date note after the system prompt, got %+v", msgs[1])
	}
	if !strings.Contains(msgs[2].Content, "latest news this week?") {
		t.Errorf("user message wrong: %q", msgs[2].Content)
	}

	// History must NOT contain the transient date note (kept clean).
	for _, m := range ag.History() {
		if strings.Contains(m.Content, "Today's date") {
			t.Errorf("date note leaked into stored history: %q", m.Content)
		}
	}
}

// Without Now, no note is added.
func TestNoDateNoteWhenNowUnset(t *testing.T) {
	fake := &fakeProvider{}
	ag := New(fake, "m", tool.NewRegistry(), "SYS", nil, 3)
	if _, err := ag.Run(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if len(fake.requests[0].Messages) != 2 { // system + user only
		t.Errorf("no date note expected, got %d messages", len(fake.requests[0].Messages))
	}
}
