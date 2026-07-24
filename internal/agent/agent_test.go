package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xautjzd/agent-cli/internal/provider"
	"github.com/xautjzd/agent-cli/internal/tool"
)

// fakeProvider replays scripted responses, capturing requests for assertions.
type fakeProvider struct {
	responses []provider.Response
	requests  []provider.Request
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Chat(_ context.Context, req provider.Request) (*provider.Response, error) {
	f.requests = append(f.requests, req)
	if len(f.responses) == 0 {
		return &provider.Response{Message: provider.Message{Role: provider.RoleAssistant, Content: "done"}}, nil
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return &resp, nil
}

// echoTool records that it ran and returns its input back.
type echoTool struct{ calls []string }

func (e *echoTool) Name() string            { return "echo" }
func (e *echoTool) Description() string     { return "echoes input" }
func (e *echoTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (e *echoTool) Execute(_ context.Context, in json.RawMessage) (string, error) {
	e.calls = append(e.calls, string(in))
	return "echoed:" + string(in), nil
}

func TestRunToolLoop(t *testing.T) {
	echo := &echoTool{}
	fake := &fakeProvider{responses: []provider.Response{
		{Message: provider.Message{
			Role: provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{{
				ID:       "call_1",
				Type:     "function",
				Function: provider.FunctionCall{Name: "echo", Arguments: `{"x":1}`},
			}},
		}},
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "final answer"}},
	}}

	ag := New(fake, "test-model", tool.NewRegistry(echo), "system prompt", nil, 10)
	out, err := ag.Run(context.Background(), "do the thing")
	if err != nil {
		t.Fatal(err)
	}
	if out != "final answer" {
		t.Errorf("out = %q", out)
	}
	if len(echo.calls) != 1 || echo.calls[0] != `{"x":1}` {
		t.Errorf("tool calls = %v", echo.calls)
	}

	// Second request must contain the tool result linked by tool_call_id.
	if len(fake.requests) != 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(fake.requests))
	}
	msgs := fake.requests[1].Messages
	last := msgs[len(msgs)-1]
	if last.Role != provider.RoleTool || last.ToolCallID != "call_1" {
		t.Errorf("tool result message wrong: %+v", last)
	}
	if last.Content != `echoed:{"x":1}` {
		t.Errorf("tool result content = %q", last.Content)
	}

	// Tool definitions must be advertised on every request.
	if len(fake.requests[0].Tools) != 1 || fake.requests[0].Tools[0].Name != "echo" {
		t.Errorf("tools not advertised: %+v", fake.requests[0].Tools)
	}
}

func TestRunMaxTurns(t *testing.T) {
	// A provider that always asks for another tool call must hit the cap.
	loop := provider.Response{Message: provider.Message{
		Role: provider.RoleAssistant,
		ToolCalls: []provider.ToolCall{{
			ID: "c", Type: "function",
			Function: provider.FunctionCall{Name: "echo", Arguments: `{}`},
		}},
	}}
	fake := &fakeProvider{responses: []provider.Response{loop, loop, loop}}
	ag := New(fake, "m", tool.NewRegistry(&echoTool{}), "s", nil, 3)
	if _, err := ag.Run(context.Background(), "go"); err == nil {
		t.Fatal("expected max turns error")
	}
}

// recordingEvents captures event callbacks for assertions.
type recordingEvents struct {
	thinking []string
	texts    []string
	results  []bool
	stats    []TurnStats
}

func (r *recordingEvents) OnUserPrompt(string)               {}
func (r *recordingEvents) OnThinking(t string)               { r.thinking = append(r.thinking, t) }
func (r *recordingEvents) OnAssistantText(t string)          { r.texts = append(r.texts, t) }
func (r *recordingEvents) OnToolCall(string, string)         {}
func (r *recordingEvents) OnToolResult(_, _ string, ok bool) { r.results = append(r.results, ok) }
func (r *recordingEvents) OnTurnStats(s TurnStats)           { r.stats = append(r.stats, s) }

func TestThinkingIsSurfacedButNotEchoed(t *testing.T) {
	fake := &fakeProvider{responses: []provider.Response{
		{Message: provider.Message{
			Role:             provider.RoleAssistant,
			Content:          "answer",
			ReasoningContent: "let me think about this",
		}},
	}}
	ev := &recordingEvents{}
	ag := New(fake, "m", tool.NewRegistry(), "sys", ev, 5)
	if _, err := ag.Run(context.Background(), "question"); err != nil {
		t.Fatal(err)
	}
	if len(ev.thinking) != 1 || ev.thinking[0] != "let me think about this" {
		t.Errorf("thinking events = %v", ev.thinking)
	}
	// Reasoning must be stripped before the message enters history.
	for _, m := range ag.History() {
		if m.ReasoningContent != "" {
			t.Error("reasoning leaked into history")
		}
	}
}

func TestToolResultStatusReachesEvents(t *testing.T) {
	fake := &fakeProvider{responses: []provider.Response{
		{Message: provider.Message{
			Role: provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{{
				ID: "c1", Type: "function",
				Function: provider.FunctionCall{Name: "no_such_tool", Arguments: `{}`},
			}},
		}},
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "done"}},
	}}
	ev := &recordingEvents{}
	ag := New(fake, "m", tool.NewRegistry(&echoTool{}), "sys", ev, 5)
	if _, err := ag.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if len(ev.results) != 1 || ev.results[0] != false {
		t.Errorf("expected one failed tool status, got %v", ev.results)
	}
}

func TestRestoreKeepsSystemPrompt(t *testing.T) {
	ag := New(&fakeProvider{}, "m", tool.NewRegistry(), "the system prompt", nil, 5)
	ag.Restore([]provider.Message{
		{Role: provider.RoleUser, Content: "old question"},
		{Role: provider.RoleAssistant, Content: "old answer"},
	})
	h := ag.History()
	if len(h) != 3 || h[0].Content != "the system prompt" || h[2].Content != "old answer" {
		t.Errorf("restore wrong: %+v", h)
	}
}

// interruptProvider simulates a user aborting mid-response: it cancels the
// turn's context and returns the partial answer alongside the cancellation
// error, mirroring what the real streaming providers do on interrupt.
type interruptProvider struct {
	cancel  context.CancelFunc
	partial string
}

func (p *interruptProvider) Name() string { return "interrupt" }
func (p *interruptProvider) Chat(ctx context.Context, req provider.Request) (*provider.Response, error) {
	p.cancel()
	return &provider.Response{Message: provider.Message{
		Role:    provider.RoleAssistant,
		Content: p.partial,
	}}, context.Canceled
}

func TestInterruptPreservesPartialAnswer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ag := New(&interruptProvider{cancel: cancel, partial: "here is the start"},
		"m", tool.NewRegistry(), "system prompt", nil, 5)

	_, err := ag.Run(ctx, "the question")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	h := ag.History()
	last := h[len(h)-1]
	if last.Role != provider.RoleAssistant {
		t.Fatalf("history must end on an assistant turn, got %s", last.Role)
	}
	if !strings.Contains(last.Content, "here is the start") {
		t.Errorf("partial answer not preserved: %q", last.Content)
	}
	if !strings.Contains(last.Content, interruptedMarker) {
		t.Errorf("interruption not marked: %q", last.Content)
	}
	// The user message must still be present, before the assistant turn.
	if h[len(h)-2].Role != provider.RoleUser || h[len(h)-2].Content != "the question" {
		t.Errorf("user message not retained: %+v", h[len(h)-2])
	}
}

func TestInterruptWithNoOutputStillAlternates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ag := New(&interruptProvider{cancel: cancel, partial: ""},
		"m", tool.NewRegistry(), "system prompt", nil, 5)

	if _, err := ag.Run(ctx, "the question"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	h := ag.History()
	last := h[len(h)-1]
	if last.Role != provider.RoleAssistant || last.Content != interruptedMarker {
		t.Errorf("expected a lone interruption marker, got %+v", last)
	}
}

// streamingFake implements both Provider and Streamer, replaying scripted
// responses and emitting their content as two deltas each.
type streamingFake struct {
	fakeProvider
}

func (f *streamingFake) ChatStream(ctx context.Context, req provider.Request, onDelta func(provider.Delta)) (*provider.Response, error) {
	resp, err := f.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	if onDelta != nil {
		if r := resp.Message.ReasoningContent; r != "" {
			onDelta(provider.Delta{Reasoning: r[:len(r)/2]})
			onDelta(provider.Delta{Reasoning: r[len(r)/2:]})
		}
		if c := resp.Message.Content; c != "" {
			onDelta(provider.Delta{Content: c[:len(c)/2]})
			onDelta(provider.Delta{Content: c[len(c)/2:]})
		}
	}
	return resp, nil
}

// streamRecorder extends recordingEvents with StreamEvents.
type streamRecorder struct {
	recordingEvents
	deltas []string
	ends   int
}

func (s *streamRecorder) OnThinkingDelta(t string)  { s.deltas = append(s.deltas, "think:"+t) }
func (s *streamRecorder) OnAssistantDelta(t string) { s.deltas = append(s.deltas, "text:"+t) }
func (s *streamRecorder) OnStreamEnd()              { s.ends++ }

func TestStreamingPath(t *testing.T) {
	fake := &streamingFake{fakeProvider{responses: []provider.Response{
		{Message: provider.Message{
			Role:             provider.RoleAssistant,
			Content:          "answer",
			ReasoningContent: "reason",
		}},
	}}}
	ev := &streamRecorder{}
	ag := New(fake, "m", tool.NewRegistry(), "sys", ev, 5)
	out, err := ag.Run(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if out != "answer" {
		t.Errorf("out = %q", out)
	}
	// Fragments arrived live, thinking before text, then the end marker.
	want := []string{"think:rea", "think:son", "text:ans", "text:wer"}
	if len(ev.deltas) != len(want) {
		t.Fatalf("deltas = %v", ev.deltas)
	}
	for i := range want {
		if ev.deltas[i] != want[i] {
			t.Errorf("delta[%d] = %q, want %q", i, ev.deltas[i], want[i])
		}
	}
	if ev.ends != 1 {
		t.Errorf("stream ends = %d", ev.ends)
	}
	// No duplicate after-the-fact rendering.
	if len(ev.texts) != 0 || len(ev.thinking) != 0 {
		t.Errorf("duplicate display: texts=%v thinking=%v", ev.texts, ev.thinking)
	}
	// Reasoning still stripped from history.
	for _, m := range ag.History() {
		if m.ReasoningContent != "" {
			t.Error("reasoning leaked into history")
		}
	}
}

func TestNonStreamingSinkFallsBack(t *testing.T) {
	// A streaming-capable provider with a non-streaming events sink must
	// use the blocking path and render after the fact.
	fake := &streamingFake{fakeProvider{responses: []provider.Response{
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "plain"}},
	}}}
	ev := &recordingEvents{}
	ag := New(fake, "m", tool.NewRegistry(), "sys", ev, 5)
	if _, err := ag.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if len(ev.texts) != 1 || ev.texts[0] != "plain" {
		t.Errorf("fallback rendering wrong: %v", ev.texts)
	}
}

func TestTurnStatsAccumulate(t *testing.T) {
	// Two rounds: a tool call then the final answer, each with usage.
	fake := &fakeProvider{responses: []provider.Response{
		{
			Message: provider.Message{
				Role: provider.RoleAssistant,
				ToolCalls: []provider.ToolCall{{
					ID: "c1", Type: "function",
					Function: provider.FunctionCall{Name: "echo", Arguments: `{}`},
				}},
			},
			Usage: provider.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
		},
		{
			Message: provider.Message{Role: provider.RoleAssistant, Content: "done"},
			Usage:   provider.Usage{PromptTokens: 150, CompletionTokens: 30}, // TotalTokens omitted → derived
		},
	}}
	ev := &recordingEvents{}
	ag := New(fake, "m", tool.NewRegistry(&echoTool{}), "sys", ev, 5)
	if _, err := ag.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}

	if len(ev.stats) != 1 {
		t.Fatalf("expected 1 stats event, got %d", len(ev.stats))
	}
	s := ev.stats[0]
	if s.PromptTokens != 250 || s.CompletionTokens != 50 || s.TotalTokens != 300 {
		t.Errorf("turn tokens = %d/%d/%d, want 250/50/300", s.PromptTokens, s.CompletionTokens, s.TotalTokens)
	}
	if s.Rounds != 2 {
		t.Errorf("rounds = %d, want 2", s.Rounds)
	}
	// Context occupancy comes from the last round only.
	if s.ContextTokens != 180 {
		t.Errorf("context = %d, want 180", s.ContextTokens)
	}
	if s.Duration <= 0 {
		t.Error("duration should be positive")
	}
	if s.SessionTokens != 300 {
		t.Errorf("session tokens = %d, want 300", s.SessionTokens)
	}

	// A second run accumulates the session totals.
	fake.responses = []provider.Response{{
		Message: provider.Message{Role: provider.RoleAssistant, Content: "again"},
		Usage:   provider.Usage{PromptTokens: 200, CompletionTokens: 10, TotalTokens: 210},
	}}
	ag.Run(context.Background(), "more")
	if got := ev.stats[1].SessionTokens; got != 510 {
		t.Errorf("session tokens after 2nd run = %d, want 510", got)
	}
	tokens, _, ctxTok := ag.Stats()
	if tokens != 510 || ctxTok != 210 {
		t.Errorf("Stats() = %d/%d, want 510/210", tokens, ctxTok)
	}

	// Reset invalidates the context estimate.
	ag.Reset()
	if _, _, ctxTok := ag.Stats(); ctxTok != 0 {
		t.Errorf("context after reset = %d, want 0", ctxTok)
	}
}

func TestConversationPersistsAcrossRuns(t *testing.T) {
	fake := &fakeProvider{}
	ag := New(fake, "m", tool.NewRegistry(), "sys", nil, 5)
	ag.Run(context.Background(), "first")
	ag.Run(context.Background(), "second")

	// system + user1 + assistant1 + user2 + assistant2
	if got := len(ag.History()); got != 5 {
		t.Errorf("history length = %d, want 5", got)
	}
	if ag.History()[0].Role != provider.RoleSystem {
		t.Error("first message must be the system prompt")
	}
}
