package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/xautjzd/agent-cli/internal/provider"
	"github.com/xautjzd/agent-cli/internal/tool"
)

// fakeSummarizer returns a canned summary and records the prior it was given.
type fakeSummarizer struct {
	summary string
	got     []provider.Message
	err     error
}

func (f *fakeSummarizer) Summarize(_ context.Context, prior []provider.Message) (string, error) {
	f.got = prior
	return f.summary, f.err
}

// convo builds an agent whose history is a system prompt followed by the given
// messages, with a summarizer injected.
func convo(sum Summarizer, msgs ...provider.Message) *Agent {
	a := New(&fakeProvider{}, "m", tool.NewRegistry(), "SYSTEM", nil, 10)
	a.Summarizer = sum
	a.messages = append(a.messages, msgs...)
	return a
}

func u(text string) provider.Message { return provider.Message{Role: provider.RoleUser, Content: text} }
func as(text string) provider.Message {
	return provider.Message{Role: provider.RoleAssistant, Content: text}
}

func TestCompactRebuildsHistory(t *testing.T) {
	sum := &fakeSummarizer{summary: "SUMMARY OF EARLIER WORK"}
	a := convo(sum,
		u("q1"), as("a1"), u("q2"), as("a2"),
		u("q3"), as("a3"), u("q4"), as("a4"),
		u("q5"), as("a5"), u("q6"), as("a6"),
		u("q7"), as("a7"),
	)
	before := len(a.messages)

	stats, err := a.Compact(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	msgs := a.History()

	// System prompt preserved at index 0.
	if msgs[0].Role != provider.RoleSystem || msgs[0].Content != "SYSTEM" {
		t.Fatalf("system prompt not preserved: %+v", msgs[0])
	}
	// Index 1 is the summary (user role, marked), index 2 the ack.
	if msgs[1].Role != provider.RoleUser || !strings.Contains(msgs[1].Content, "SUMMARY OF EARLIER WORK") {
		t.Errorf("summary message wrong: %+v", msgs[1])
	}
	if !strings.HasPrefix(msgs[1].Content, SummaryMarker) {
		t.Errorf("summary missing marker: %q", msgs[1].Content)
	}
	if msgs[2].Role != provider.RoleAssistant {
		t.Errorf("expected assistant ack at index 2, got %+v", msgs[2])
	}
	// Result is shorter and the tail is preserved verbatim at the end.
	if len(msgs) >= before {
		t.Errorf("compaction did not shrink history: %d -> %d", before, len(msgs))
	}
	if msgs[len(msgs)-1].Content != "a7" {
		t.Errorf("tail not preserved: last = %+v", msgs[len(msgs)-1])
	}
	if stats.MessagesBefore != before || stats.MessagesAfter != len(msgs) {
		t.Errorf("stats wrong: %+v (before=%d after=%d)", stats, before, len(msgs))
	}
	if stats.SummarizedMessages != len(sum.got) {
		t.Errorf("summarized count %d != prior given %d", stats.SummarizedMessages, len(sum.got))
	}
}

func TestSafeSplitKeepsToolPairs(t *testing.T) {
	// A tail boundary must land on a user message so a tool result is never
	// separated from the assistant tool call it answers.
	sum := &fakeSummarizer{summary: "S"}
	a := convo(sum,
		u("start"),
		as("thinking"), // assistant with a tool call
		provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "t1", Function: provider.FunctionCall{Name: "echo"}}}},
		provider.Message{Role: provider.RoleTool, ToolCallID: "t1", Content: "result"},
		u("next"),
		as("done1"),
		u("again"),
		as("done2"),
	)
	if _, err := a.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Every RoleTool in the rebuilt history must be preceded (somewhere
	// earlier) by an assistant message carrying its tool_call_id.
	msgs := a.History()
	for i, m := range msgs {
		if m.Role != provider.RoleTool {
			continue
		}
		found := false
		for j := 0; j < i; j++ {
			for _, c := range msgs[j].ToolCalls {
				if c.ID == m.ToolCallID {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("orphaned tool result at %d (id %q) — compaction broke a tool pair", i, m.ToolCallID)
		}
	}
}

func TestShouldCompactThreshold(t *testing.T) {
	a := convo(&fakeSummarizer{summary: "s"}, u("a"), as("b"), u("c"), as("d"), u("e"), as("f"), u("g"), as("h"))
	a.AutoCompact = true
	a.ContextLimit = 1000

	a.contextTokens = 800 // below 85%
	if a.shouldCompact() {
		t.Error("should not compact below threshold")
	}
	a.contextTokens = 900 // above 85%
	if !a.shouldCompact() {
		t.Error("should compact above threshold")
	}
	// Disabled auto-compaction never triggers.
	a.AutoCompact = false
	if a.shouldCompact() {
		t.Error("auto-compact off must never trigger")
	}
}

func TestMaybeCompactAtEndOfTurn(t *testing.T) {
	// A real Run should compact once occupancy crosses the threshold. Drive
	// it with a provider that reports high prompt usage.
	fake := &fakeProvider{responses: []provider.Response{
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "answer"},
			Usage: provider.Usage{PromptTokens: 950, CompletionTokens: 10}},
	}}
	sum := &fakeSummarizer{summary: "COMPACTED"}
	a := New(fake, "m", tool.NewRegistry(), "SYSTEM", nil, 5)
	a.Summarizer = sum
	a.AutoCompact = true
	a.ContextLimit = 1000
	// Pre-load enough history that there is something to compact.
	a.messages = append(a.messages, u("old1"), as("r1"), u("old2"), as("r2"), u("old3"), as("r3"))

	if _, err := a.Run(context.Background(), "new question"); err != nil {
		t.Fatal(err)
	}
	// After the turn, history should have been compacted: a summary message
	// is present and the summarizer was invoked.
	if len(sum.got) == 0 {
		t.Fatal("auto-compaction did not run")
	}
	if !strings.Contains(a.History()[1].Content, "COMPACTED") {
		t.Errorf("expected summary in history, got %+v", a.History()[1])
	}
}

func TestCompactTooShort(t *testing.T) {
	a := convo(&fakeSummarizer{summary: "s"}, u("only one"))
	if _, err := a.Compact(context.Background()); err == nil {
		t.Error("expected error compacting a too-short conversation")
	}
}

func TestCompactSummarizerErrorPreservesHistory(t *testing.T) {
	sum := &fakeSummarizer{err: context.DeadlineExceeded}
	a := convo(sum, u("q1"), as("a1"), u("q2"), as("a2"), u("q3"), as("a3"), u("q4"), as("a4"))
	before := a.History()
	if _, err := a.Compact(context.Background()); err == nil {
		t.Fatal("expected summarizer error to propagate")
	}
	// History must be untouched on failure.
	if len(a.History()) != len(before) {
		t.Errorf("history mutated on summarizer failure: %d -> %d", len(before), len(a.History()))
	}
}

func TestDefaultSummarizerUsesProvider(t *testing.T) {
	// With no injected summarizer, the agent falls back to its provider.
	fake := &fakeProvider{responses: []provider.Response{
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "provider-made summary"}},
	}}
	a := New(fake, "m", tool.NewRegistry(), "SYSTEM", nil, 5)
	a.messages = append(a.messages, u("q1"), as("a1"), u("q2"), as("a2"), u("q3"), as("a3"), u("q4"), as("a4"))
	if _, err := a.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.History()[1].Content, "provider-made summary") {
		t.Errorf("default summarizer not used: %+v", a.History()[1])
	}
	// The summarization request must carry no tools and its own system prompt.
	last := fake.requests[len(fake.requests)-1]
	if len(last.Tools) != 0 {
		t.Errorf("summarization request should attach no tools")
	}
	if last.Messages[0].Role != provider.RoleSystem || !strings.Contains(last.Messages[0].Content, "compacting") {
		t.Errorf("summarization system prompt wrong: %+v", last.Messages[0])
	}
}
