// Package agent implements the core agentic loop: send the conversation to
// the model, execute any requested tools, feed results back, and repeat
// until the model produces a final text answer.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/xautjzd/agent-cli/internal/log"
	"github.com/xautjzd/agent-cli/internal/provider"
	"github.com/xautjzd/agent-cli/internal/tool"
	"github.com/xautjzd/agent-cli/internal/usage"
)

// Events allows the UI layer to observe the loop without the loop knowing
// anything about rendering (DIP: the agent depends on this small interface,
// the terminal UI implements it).
type Events interface {
	// OnUserPrompt echoes a user input line. The live loop never calls it
	// (the input is already on screen in the editor); transcript replay
	// uses it so resumed history renders exactly like the original chat.
	OnUserPrompt(text string)
	// OnThinking is called with chain-of-thought text from reasoning
	// models, before the visible answer.
	OnThinking(text string)
	// OnAssistantText is called when the model produces visible text.
	OnAssistantText(text string)
	// OnToolCall is called before a tool executes.
	OnToolCall(name string, args string)
	// OnToolResult reports the tool output and whether the call succeeded,
	// so the UI can render success/failure status.
	OnToolResult(name string, result string, ok bool)
	// OnTurnStats reports token usage and timing after a completed turn.
	OnTurnStats(stats TurnStats)
}

// StreamEvents is optionally implemented by Events sinks that can render
// incremental output. When both the provider and the events sink support
// streaming, the agent forwards fragments live and skips the after-the-fact
// OnThinking/OnAssistantText calls.
type StreamEvents interface {
	// OnThinkingDelta delivers a reasoning fragment.
	OnThinkingDelta(text string)
	// OnAssistantDelta delivers a visible-answer fragment.
	OnAssistantDelta(text string)
	// OnStreamEnd marks the end of one streamed completion so the sink can
	// close styling and spacing.
	OnStreamEnd()
}

// TurnStats summarizes token consumption and timing for one Run call, plus
// running session totals.
type TurnStats struct {
	// PromptTokens/CompletionTokens/TotalTokens are summed over every
	// model round-trip in the turn (tool loops make several).
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	// ContextTokens estimates the conversation's current context
	// occupancy: the prompt of the latest request plus its completion —
	// i.e. what the next request will roughly start from.
	ContextTokens int
	// ContextLimit is the model's usable context window in tokens, carried
	// alongside ContextTokens so the UI can render occupancy as a fraction
	// of the window. 0 means the limit is unknown/unconfigured.
	ContextLimit int
	// Rounds is the number of model round-trips in this turn.
	Rounds   int
	Duration time.Duration
	// SessionTokens/SessionDuration accumulate across the whole session.
	SessionTokens   int
	SessionDuration time.Duration
}

// ContextPercent returns the conversation's context occupancy as a
// whole-number percentage of the model's window, or -1 when the window size
// is unknown (so callers can omit the percentage rather than divide by zero).
func (s TurnStats) ContextPercent() int {
	if s.ContextLimit <= 0 {
		return -1
	}
	return int(math.Round(float64(s.ContextTokens) / float64(s.ContextLimit) * 100))
}

// Gate is consulted before every tool call, letting a permission layer
// confirm, deny, or annotate the execution without the agent knowing any
// policy details (DIP).
type Gate interface {
	// BeforeToolCall returns whether the call may proceed and an optional
	// note. The note is prepended to the tool result so it becomes part of
	// the conversation context — the audit trail for unattended approvals.
	BeforeToolCall(name string, args string) (allow bool, note string)
}

// ToolHooks lets an integration layer observe and influence tool execution
// (the PreToolUse/PostToolUse extension points). The agent depends only on
// this interface (DIP); the hook runner implements it. Nil disables hooks.
type ToolHooks interface {
	// PreToolUse runs before a tool executes. Block stops the call (its
	// Reason is fed back to the model); Context, if any, is appended to the
	// eventual result.
	PreToolUse(ctx context.Context, name, args string) HookOutcome
	// PostToolUse runs after a tool executes. Context, if any, is appended to
	// the result the model sees.
	PostToolUse(ctx context.Context, name, args, result string, ok bool) HookOutcome
}

// HookOutcome is the agent-facing result of a tool hook.
type HookOutcome struct {
	Block   bool
	Reason  string
	Context string
}

// Agent owns one conversation with the model.
type Agent struct {
	Provider provider.Provider
	Model    string
	Tools    *tool.Registry
	Events   Events
	// Gate, when non-nil, screens every tool call (permission modes).
	Gate Gate
	// Hooks, when non-nil, runs PreToolUse/PostToolUse integration hooks
	// around each tool call.
	Hooks ToolHooks
	// MaxTurns bounds tool-use iterations per user message to prevent
	// runaway loops.
	MaxTurns int

	// AutoCompact enables automatic context compaction when occupancy nears
	// ContextLimit. ContextLimit is the model's usable context window in
	// tokens; both are set from configuration.
	AutoCompact  bool
	ContextLimit int
	// Summarizer produces compaction summaries; nil defaults to one backed
	// by this agent's provider and model.
	Summarizer Summarizer
	// Usage, when set, records per-turn token consumption and cost keyed by
	// provider and model (shared across sessions and subagents).
	Usage *usage.Recorder
	// Activation, when set, enables deferred tool loading: only core tools and
	// deferred tools the model has explicitly loaded (via search_tools) are
	// advertised each request, keeping per-request tool overhead flat as MCP
	// servers add many schema-heavy tools. nil advertises every registered tool
	// (the behavior when no deferred tools exist).
	Activation *tool.Activation
	// Now, when set, supplies the current date. It is injected into each
	// request as a small note right after the system prompt — never stored in
	// history and never in the system prompt itself — so the static prompt
	// stays byte-stable (cacheable across days) while only the day-granularity
	// note changes. Defaults to no date (nil).
	Now func() time.Time

	messages []provider.Message

	// Usage bookkeeping for stats display.
	sessionTokens   int
	sessionDuration time.Duration
	contextTokens   int
}

// New builds an agent with an initial system prompt.
func New(p provider.Provider, model string, tools *tool.Registry, systemPrompt string, events Events, maxTurns int) *Agent {
	if maxTurns <= 0 {
		maxTurns = 40
	}
	return &Agent{
		Provider: p,
		Model:    model,
		Tools:    tools,
		Events:   events,
		MaxTurns: maxTurns,
		messages: []provider.Message{{Role: provider.RoleSystem, Content: systemPrompt}},
	}
}

// Run processes one user message to completion and returns the final
// assistant text.
//
// Key flow (the heart of the CLI):
//  1. Append the user message to the conversation.
//  2. Ask the provider for a completion with the tool catalog attached.
//  3. If the reply contains tool calls, execute each via the registry,
//     append one RoleTool result message per call, and go back to step 2.
//  4. When a reply has no tool calls, it is the final answer.
func (a *Agent) Run(ctx context.Context, userInput string) (string, error) {
	return a.RunMessage(ctx, provider.Message{Role: provider.RoleUser, Content: userInput})
}

// RunMessage is Run for a pre-built user message — the entry point for
// multimodal input, where the message carries image parts alongside text.
func (a *Agent) RunMessage(ctx context.Context, userMsg provider.Message) (string, error) {
	userMsg.Role = provider.RoleUser
	a.messages = append(a.messages, userMsg)

	log.Info("agent", "Run: started, %d messages in history", len(a.messages))

	start := time.Now()
	var stats TurnStats

	for turn := 0; turn < a.MaxTurns; turn++ {
		log.Debug("agent", "complete: turn %d, %d messages, %d tools", turn, len(a.messages), len(a.toolDefs()))
		resp, streamed, err := a.complete(ctx, provider.Request{
			Model:    a.Model,
			Messages: a.requestMessages(),
			Tools:    a.toolDefs(),
		})
		if err != nil {
			// A user interrupt (Esc/Ctrl-C) cancels the context: keep whatever
			// streamed so far and leave history on an assistant turn, so the
			// user can add more and continue instead of losing the exchange.
			if ctx.Err() != nil {
				a.recordInterrupted(resp)
			}
			return "", err
		}
		// Accumulate usage; some providers omit TotalTokens, so derive it.
		u := resp.Usage
		if u.TotalTokens == 0 {
			u.TotalTokens = u.PromptTokens + u.CompletionTokens
		}
		stats.PromptTokens += u.PromptTokens
		stats.CompletionTokens += u.CompletionTokens
		stats.TotalTokens += u.TotalTokens
		stats.Rounds++
		// The latest request's prompt+completion is the best estimate of
		// what the conversation currently occupies.
		a.contextTokens = u.PromptTokens + u.CompletionTokens

		// Streamed output was already rendered fragment by fragment; only
		// blocking completions display here.
		if !streamed && resp.Message.ReasoningContent != "" && a.Events != nil {
			a.Events.OnThinking(resp.Message.ReasoningContent)
		}
		// Reasoning is display-only; it must never be echoed back to the
		// provider as context.
		resp.Message.ReasoningContent = ""
		a.messages = append(a.messages, resp.Message)

		if !streamed && resp.Message.Content != "" && a.Events != nil {
			a.Events.OnAssistantText(resp.Message.Content)
		}

		if len(resp.Message.ToolCalls) == 0 {
			log.Info("agent", "Run: final answer in %d rounds, %d+%d tokens", stats.Rounds, stats.PromptTokens, stats.CompletionTokens)
			a.finishTurn(&stats, start)
			// Compact after the turn completes so the next user message
			// starts from a smaller context. Done here (not mid-loop) to
			// avoid disturbing an in-flight tool sequence.
			a.maybeCompact(ctx)
			return resp.Message.Content, nil
		}

		a.executeToolCalls(ctx, resp.Message.ToolCalls)
		log.Debug("agent", "executed %d tool calls in turn %d", len(resp.Message.ToolCalls), turn)
	}
	return "", fmt.Errorf("reached max turns (%d) without a final answer", a.MaxTurns)
}

// interruptedMarker is appended to history when the user aborts a turn, so the
// model knows on the next request that its previous answer was cut short.
const interruptedMarker = "[Response interrupted by user]"

// recordInterrupted keeps the conversation consistent after the user aborts a
// turn. It preserves any assistant text that already streamed to the screen and
// ensures the transcript ends on an assistant turn, so the user's follow-up
// message alternates cleanly and the exchange can continue. partial is the
// possibly-partial response the provider returned on cancellation (may be nil).
func (a *Agent) recordInterrupted(partial *provider.Response) {
	if partial != nil && strings.TrimSpace(partial.Message.Content) != "" {
		msg := partial.Message
		msg.Role = provider.RoleAssistant
		msg.ReasoningContent = ""
		// Any tool calls were never executed; dropping them avoids an
		// unanswered tool_use that would make the next request invalid.
		msg.ToolCalls = nil
		msg.Content = strings.TrimRight(msg.Content, "\n") + "\n\n" + interruptedMarker
		a.messages = append(a.messages, msg)
		return
	}
	// Nothing usable streamed. If the transcript would otherwise end on a user
	// or tool message, add a marker so roles still alternate on the next turn.
	if n := len(a.messages); n > 0 && a.messages[n-1].Role != provider.RoleAssistant {
		a.messages = append(a.messages, provider.Message{
			Role:    provider.RoleAssistant,
			Content: interruptedMarker,
		})
	}
}

// deniedResult is fed back when the gate refuses a call, so the model can
// adjust course instead of aborting the turn.
const deniedResult = "Error: the user denied this operation. Ask the user how to proceed or choose a safer alternative."

// toolOutcome is one tool call's result, buffered so parallel calls can be
// appended to history in the model's original call order.
type toolOutcome struct {
	content string
	ok      bool
}

// executeToolCalls runs the tool calls from one assistant turn and appends a
// result message per call. A single call runs inline (the common case); when
// the model batches several independent calls — the mechanism that powers
// parallel subagent delegation — they run concurrently, while gating,
// announcement, and result ordering stay deterministic.
func (a *Agent) executeToolCalls(ctx context.Context, calls []provider.ToolCall) {
	if len(calls) == 1 {
		out := a.runOneToolCall(ctx, calls[0])
		a.appendToolResult(calls[0], out)
		return
	}

	// Gate and announce sequentially: a HITL confirmation reads from stdin
	// and must not race another, and headers should print in call order.
	allowed := make([]bool, len(calls))
	notes := make([]string, len(calls))
	for i, call := range calls {
		allow, note := true, ""
		if a.Gate != nil {
			allow, note = a.Gate.BeforeToolCall(call.Function.Name, call.Function.Arguments)
		}
		allowed[i], notes[i] = allow, note
		if a.Events != nil {
			a.Events.OnToolCall(call.Function.Name, call.Function.Arguments)
		}
	}

	// Execute the allowed calls concurrently. Registry reads and the built-in
	// tools are safe for concurrent use; independent subtasks are the design
	// target, so callers must not batch mutations of the same file.
	outcomes := make([]toolOutcome, len(calls))
	var wg sync.WaitGroup
	for i := range calls {
		if !allowed[i] {
			outcomes[i] = toolOutcome{content: deniedResult, ok: false}
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outcomes[i] = a.executeWithHooks(ctx, calls[i], notes[i])
		}(i)
	}
	wg.Wait()

	// Report and record in call order, so display and history are
	// deterministic regardless of completion order.
	for i, call := range calls {
		if a.Events != nil {
			a.Events.OnToolResult(call.Function.Name, outcomes[i].content, outcomes[i].ok)
		}
		a.appendToolResult(call, outcomes[i])
	}
}

// runOneToolCall gates, announces, executes, and reports a single tool call,
// returning its outcome without appending to history (the caller does that).
func (a *Agent) runOneToolCall(ctx context.Context, call provider.ToolCall) toolOutcome {
	// The gate runs before the call header is rendered so any confirmation
	// prompt appears above it, keeping the header and its status-dot rewrite
	// adjacent.
	allow, note := true, ""
	if a.Gate != nil {
		allow, note = a.Gate.BeforeToolCall(call.Function.Name, call.Function.Arguments)
	}
	if a.Events != nil {
		a.Events.OnToolCall(call.Function.Name, call.Function.Arguments)
	}
	var out toolOutcome
	if !allow {
		out = toolOutcome{content: deniedResult, ok: false}
	} else {
		out = a.executeWithHooks(ctx, call, note)
	}
	if a.Events != nil {
		a.Events.OnToolResult(call.Function.Name, out.content, out.ok)
	}
	return out
}

// executeWithHooks runs the PreToolUse hook, the tool itself, and the
// PostToolUse hook for a gate-approved call, weaving any hook-supplied context
// into the result the model sees. A blocking PreToolUse hook prevents
// execution and reports the reason instead.
func (a *Agent) executeWithHooks(ctx context.Context, call provider.ToolCall, note string) toolOutcome {
	name, args := call.Function.Name, call.Function.Arguments
	log.Debug("agent", "executeWithHooks: tool=%s args_len=%d", name, len(args))

	if a.Hooks != nil {
		if h := a.Hooks.PreToolUse(ctx, name, args); h.Block {
			reason := h.Reason
			if reason == "" {
				reason = "blocked by a PreToolUse hook"
			}
			return toolOutcome{content: "Error: " + reason, ok: false}
		}
	}

	content, ok := a.Tools.Execute(ctx, name, json.RawMessage(args))
	if note != "" {
		// Audit note becomes part of the context (and thus the persisted
		// session) for traceability.
		content = note + "\n\n" + content
	}

	if a.Hooks != nil {
		if h := a.Hooks.PostToolUse(ctx, name, args, content, ok); h.Context != "" {
			content += "\n\n" + h.Context
		}
	}
	return toolOutcome{content: content, ok: ok}
}

// appendToolResult records a tool result message. Every tool call must be
// answered, even on failure, or the API rejects the next request.
func (a *Agent) appendToolResult(call provider.ToolCall, out toolOutcome) {
	a.messages = append(a.messages, provider.Message{
		Role:       provider.RoleTool,
		Content:    out.content,
		ToolCallID: call.ID,
	})
}

// requestMessages returns the messages to send, inserting a small current-date
// note right after the system prompt when Now is set. The note is transient
// (never stored in a.messages) and sits after the byte-stable system prompt, so
// the static prefix stays cached while only the day-granularity note changes.
// For providers with a dedicated system field (Anthropic) the note folds into
// that field; for message-array providers (OpenAI/DeepSeek) it is a separate
// message that keeps the system prompt independently cacheable.
// toolDefs builds the tool catalog advertised on a request. Without an
// Activation set it advertises every registered tool (the historical behavior,
// used when nothing is deferred). With one, it advertises core tools plus only
// the deferred tools the model has loaded via search_tools, so the per-request
// tool payload stays flat as MCP servers contribute many schema-heavy tools.
// It is recomputed each turn so a tool loaded mid-turn becomes callable on the
// next round-trip.
func (a *Agent) toolDefs() []provider.ToolDef {
	var tools []tool.Tool
	if a.Activation == nil {
		tools = a.Tools.All()
	} else {
		tools = a.Tools.Core()
		for _, t := range a.Tools.Deferred() {
			if a.Activation.IsActive(t.Name()) {
				tools = append(tools, t)
			}
		}
	}
	defs := make([]provider.ToolDef, 0, len(tools))
	for _, t := range tools {
		defs = append(defs, provider.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Schema(),
		})
	}
	return defs
}

func (a *Agent) requestMessages() []provider.Message {
	if a.Now == nil || len(a.messages) == 0 {
		return a.messages
	}
	note := provider.Message{
		Role:    provider.RoleSystem,
		Content: "Today's date: " + a.Now().Format("2006-01-02"),
	}
	out := make([]provider.Message, 0, len(a.messages)+1)
	out = append(out, a.messages[0]) // system prompt (byte-stable, cached)
	out = append(out, note)          // volatile date, isolated right after it
	out = append(out, a.messages[1:]...)
	return out
}

// complete performs one model round-trip, streaming when both the provider
// and the events sink support it. The returned flag tells the caller
// whether output was already rendered live.
func (a *Agent) complete(ctx context.Context, req provider.Request) (*provider.Response, bool, error) {
	streamer, canStream := a.Provider.(provider.Streamer)
	sink, canRender := a.Events.(StreamEvents)
	if !canStream || !canRender {
		log.Debug("agent", "complete: blocking Chat, provider=%s, model=%s", a.Provider.Name(), req.Model)
		resp, err := a.Provider.Chat(ctx, req)
		if err != nil {
			log.Warn("agent", "complete: Chat failed: %v", err)
		}
		return resp, false, err
	}
	log.Debug("agent", "complete: streaming ChatStream, provider=%s, model=%s", a.Provider.Name(), req.Model)
	resp, err := streamer.ChatStream(ctx, req, func(d provider.Delta) {
		if d.Reasoning != "" {
			sink.OnThinkingDelta(d.Reasoning)
		}
		if d.Content != "" {
			sink.OnAssistantDelta(d.Content)
		}
	})
	// Always close the streamed block (reset the tint, clear the sink's
	// streaming state) — including on a user interrupt, where the provider
	// hands back the partial message so the caller can preserve it.
	sink.OnStreamEnd()
	if err != nil {
		if ctx.Err() != nil {
			return resp, true, err
		}
		return nil, false, err
	}
	return resp, true, nil
}

// finishTurn closes the turn's bookkeeping and notifies the UI.
func (a *Agent) finishTurn(stats *TurnStats, start time.Time) {
	stats.Duration = time.Since(start)
	stats.ContextTokens = a.contextTokens
	stats.ContextLimit = a.ContextLimit
	a.sessionTokens += stats.TotalTokens
	a.sessionDuration += stats.Duration
	stats.SessionTokens = a.sessionTokens
	stats.SessionDuration = a.sessionDuration
	// Record cross-session usage keyed by the provider and model that served
	// this turn (constant within a turn — no mid-turn switch).
	if a.Usage != nil && (stats.PromptTokens > 0 || stats.CompletionTokens > 0) {
		a.Usage.Record(a.Provider.Name(), a.Model, stats.PromptTokens, stats.CompletionTokens, stats.Duration)
	}
	if a.Events != nil {
		a.Events.OnTurnStats(*stats)
	}
}

// Stats returns the session's cumulative token count and model time, plus
// the current context-occupancy estimate (0 until the first completed
// request after a reset or restore).
func (a *Agent) Stats() (sessionTokens int, sessionDuration time.Duration, contextTokens int) {
	return a.sessionTokens, a.sessionDuration, a.contextTokens
}

// SetModel switches the model for subsequent turns, keeping history.
func (a *Agent) SetModel(model string) { a.Model = model }

// SetProvider switches the provider (and model) for subsequent turns.
// The conversation history is preserved across the switch, which is what
// makes mid-session provider hopping useful.
func (a *Agent) SetProvider(p provider.Provider, model string) {
	a.Provider = p
	a.Model = model
}

// Reset clears the conversation, keeping only the system prompt.
func (a *Agent) Reset() {
	if len(a.messages) > 0 {
		a.messages = a.messages[:1]
	}
	a.contextTokens = 0 // unknown until the next completed request
}

// StripImageParts drops multimodal parts from the conversation, leaving a
// text placeholder, so history can be replayed against a model without
// vision support. Returns how many messages were affected.
func (a *Agent) StripImageParts() int {
	n := 0
	for i := range a.messages {
		if len(a.messages[i].Parts) > 0 {
			a.messages[i].Parts = nil
			a.messages[i].Content += "\n[image omitted: current model has no vision]"
			n++
		}
	}
	return n
}

// Restore replaces the conversation with a previously saved one. The
// current system prompt is kept (it may have changed since the session was
// recorded — new memories, new skills); msgs must not include one.
func (a *Agent) Restore(msgs []provider.Message) {
	a.Reset()
	a.messages = append(a.messages, msgs...)
}

// History returns a copy of the conversation so far.
func (a *Agent) History() []provider.Message {
	out := make([]provider.Message, len(a.messages))
	copy(out, a.messages)
	return out
}
