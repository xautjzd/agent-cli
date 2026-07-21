package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/xautjzd/agent-cli/internal/provider"
)

// Context compaction keeps a long-running conversation inside the model's
// context window, the way mainstream coding agents (Claude Code, Codex,
// opencode) do: when occupancy nears the limit, the older turns are replaced
// by a model-written summary while a verbatim tail of recent turns is kept for
// fidelity. The system prompt is never touched.
//
// The design keeps the policy (when to compact) and the mechanism (how to
// summarize) separate. The Agent owns the policy and the message surgery; the
// Summarizer interface owns turning prior turns into prose (DIP), so the
// summarizing model can differ from the chat model and tests can inject a fake.

// Summarizer condenses a slice of prior conversation into a compact brief that
// lets the assistant continue as if it still had the full history.
type Summarizer interface {
	Summarize(ctx context.Context, prior []provider.Message) (string, error)
}

// CompactionObserver is optionally implemented by an Events sink to be told
// when a compaction happens, so the UI can surface it (Claude Code prints a
// "Compacted…" line). It is a separate optional interface so adding compaction
// does not force every existing sink to change (ISP).
type CompactionObserver interface {
	OnCompaction(stats CompactionStats)
}

// CompactionStats describes one compaction for display and logging.
type CompactionStats struct {
	// Trigger is "auto" or "manual".
	Trigger string
	// MessagesBefore/MessagesAfter are conversation lengths including the
	// system prompt.
	MessagesBefore int
	MessagesAfter  int
	// SummarizedMessages is how many older messages were folded into the
	// summary.
	SummarizedMessages int
	// ContextTokensBefore is the occupancy estimate that triggered it.
	ContextTokensBefore int
	// SummaryChars is the length of the generated summary.
	SummaryChars int
}

// compactThresholdRatio is the fraction of ContextLimit at which automatic
// compaction engages. Chosen to leave room for the next request+reply.
const compactThresholdRatio = 0.85

// compactKeepRecent is the number of most-recent messages to try to preserve
// verbatim; the real cut is snapped to a turn boundary so tool calls keep
// their results (see safeSplit).
const compactKeepRecent = 6

// SummaryMarker prefixes the injected summary message so it is recognizable in
// transcripts and never mistaken for ordinary user input. It is exported so
// the session layer can detect a synthetic summary turn (it carries no raw
// user input to display on resume).
const SummaryMarker = "[Earlier conversation compacted to save context]\n\n"

// IsSummaryMessage reports whether a message is a compaction summary the agent
// injected (a synthetic user turn), rather than something the user typed.
func IsSummaryMessage(m provider.Message) bool {
	return m.Role == provider.RoleUser && strings.HasPrefix(m.Content, SummaryMarker)
}

// shouldCompact reports whether automatic compaction should run now.
func (a *Agent) shouldCompact() bool {
	if !a.AutoCompact || a.ContextLimit <= 0 || a.contextTokens <= 0 {
		return false
	}
	if len(a.messages) <= compactKeepRecent+2 {
		return false // too short to gain anything
	}
	return a.contextTokens >= int(float64(a.ContextLimit)*compactThresholdRatio)
}

// maybeCompact runs an automatic compaction when the policy calls for it. It
// is best-effort: a summarizer failure leaves the conversation untouched so a
// transient error never destroys history.
func (a *Agent) maybeCompact(ctx context.Context) {
	if !a.shouldCompact() {
		return
	}
	if _, err := a.compact(ctx, "auto"); err != nil {
		// Surface nothing fatal: the next turn simply carries full history
		// and may retry. Compaction must never break the loop.
		return
	}
}

// Compact runs a compaction on demand (the /compact command). It returns the
// stats, or an error if there is nothing to compact or summarization failed.
func (a *Agent) Compact(ctx context.Context) (CompactionStats, error) {
	return a.compact(ctx, "manual")
}

// compact performs the message surgery: summarize messages[1:split] and
// rebuild the conversation as [system, summary(user), ack(assistant),
// tail...]. split is chosen at a user-turn boundary so no tool result is
// orphaned from the assistant tool call it answers.
func (a *Agent) compact(ctx context.Context, trigger string) (CompactionStats, error) {
	if len(a.messages) < 3 {
		return CompactionStats{}, fmt.Errorf("conversation too short to compact")
	}
	split := a.safeSplit()
	if split <= 1 {
		return CompactionStats{}, fmt.Errorf("no safe compaction boundary found")
	}
	prior := a.messages[1:split]
	tail := a.messages[split:]

	summarizer := a.summarizer()
	summary, err := summarizer.Summarize(ctx, prior)
	if err != nil {
		return CompactionStats{}, fmt.Errorf("summarize: %w", err)
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return CompactionStats{}, fmt.Errorf("summarizer returned an empty summary")
	}

	stats := CompactionStats{
		Trigger:             trigger,
		MessagesBefore:      len(a.messages),
		SummarizedMessages:  len(prior),
		ContextTokensBefore: a.contextTokens,
		SummaryChars:        len(summary),
	}

	rebuilt := make([]provider.Message, 0, len(tail)+3)
	rebuilt = append(rebuilt, a.messages[0]) // system prompt, untouched
	rebuilt = append(rebuilt, provider.Message{
		Role:    provider.RoleUser,
		Content: SummaryMarker + summary,
	})
	// A short assistant acknowledgement keeps role alternation valid for
	// providers that require it and separates the summary from the tail.
	rebuilt = append(rebuilt, provider.Message{
		Role:    provider.RoleAssistant,
		Content: "Understood — I'll continue from that summary.",
	})
	rebuilt = append(rebuilt, tail...)
	a.messages = rebuilt

	// The old occupancy estimate no longer reflects the shrunken history;
	// the next completed request will refresh it.
	a.contextTokens = 0

	stats.MessagesAfter = len(a.messages)
	// Auto compaction has no command to report it, so notify the UI via the
	// observer. Manual /compact is reported by its command handler instead,
	// keeping each path to a single message.
	if trigger == "auto" {
		if obs, ok := a.Events.(CompactionObserver); ok && a.Events != nil {
			obs.OnCompaction(stats)
		}
	}
	return stats, nil
}

// safeSplit returns the index at which to cut history so the tail begins on a
// user message (a clean turn boundary) and the summarized head is non-empty.
// Cutting only at a user boundary guarantees no tool result is separated from
// the assistant tool call it answers — both live together in the head or the
// tail. It aims to keep the last compactKeepRecent messages verbatim: it
// prefers the latest user turn at or before that target, and if none exists
// there, the earliest user turn after it (a smaller tail). Returns 0 when no
// valid boundary exists (index 0 is the system prompt, index 1 would leave an
// empty head).
func (a *Agent) safeSplit() int {
	target := len(a.messages) - compactKeepRecent
	best := 0
	for i := 2; i < len(a.messages); i++ {
		if a.messages[i].Role != provider.RoleUser {
			continue
		}
		if i <= target {
			best = i // latest boundary within the "summarize" region
			continue
		}
		// Past the target: only take it if we found nothing earlier.
		if best == 0 {
			return i
		}
		break
	}
	return best
}

// summarizer returns the configured Summarizer, defaulting to one backed by
// the agent's own provider and model.
func (a *Agent) summarizer() Summarizer {
	if a.Summarizer != nil {
		return a.Summarizer
	}
	return &ProviderSummarizer{Provider: a.Provider, Model: a.Model}
}

// ProviderSummarizer summarizes prior conversation with an LLM. By default it
// reuses the agent's chat provider/model, but any provider can be injected —
// e.g. a cheaper, larger-context model dedicated to summaries.
type ProviderSummarizer struct {
	Provider provider.Provider
	Model    string
}

// summarizeSystemPrompt instructs the model to produce a hand-off brief. It is
// tuned for coding agents: preserve exact identifiers and actionable state.
const summarizeSystemPrompt = `You are compacting a coding assistant's conversation to fit its context window. Write a dense, factual summary that lets the assistant continue seamlessly without the original messages.

Cover, as applicable:
- The user's goals and specific requests (in order).
- Key decisions and the reasoning behind them.
- Files created or modified and the essence of each change.
- Important facts, constraints, and results learned (build/test outcomes, errors).
- The current state of the work and concrete next steps.

Preserve exact identifiers verbatim: file paths, function/type names, commands, config keys, error strings. Be concise but complete — omit chit-chat, keep substance. Output only the summary, no preamble.`

// Summarize renders the prior messages to text and asks the model to condense
// them. It does not stream and attaches no tools — this is a plain call.
func (s *ProviderSummarizer) Summarize(ctx context.Context, prior []provider.Message) (string, error) {
	req := provider.Request{
		Model: s.Model,
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: summarizeSystemPrompt},
			{Role: provider.RoleUser, Content: "Conversation to summarize:\n\n" + renderTranscript(prior)},
		},
	}
	resp, err := s.Provider.Chat(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Message.Content, nil
}

// renderTranscript flattens messages into a readable transcript for the
// summarizer: role-labeled text, tool calls named with their arguments, and
// tool results included, so nothing material is lost to the summary.
func renderTranscript(msgs []provider.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		switch m.Role {
		case provider.RoleUser:
			b.WriteString("USER: ")
			b.WriteString(messageText(m))
			b.WriteByte('\n')
		case provider.RoleAssistant:
			if m.Content != "" {
				b.WriteString("ASSISTANT: ")
				b.WriteString(m.Content)
				b.WriteByte('\n')
			}
			for _, c := range m.ToolCalls {
				fmt.Fprintf(&b, "ASSISTANT called tool %s(%s)\n", c.Function.Name, c.Function.Arguments)
			}
		case provider.RoleTool:
			fmt.Fprintf(&b, "TOOL RESULT: %s\n", truncate(m.Content, 2000))
		}
	}
	return b.String()
}

// messageText returns a message's plain text, noting any image parts so the
// summary records that images were present.
func messageText(m provider.Message) string {
	if len(m.Parts) == 0 {
		return m.Content
	}
	var b strings.Builder
	b.WriteString(m.Content)
	for _, p := range m.Parts {
		if p.Type == "image_url" {
			b.WriteString(" [image]")
		}
	}
	return b.String()
}

// truncate bounds a long tool result so one huge output cannot dominate the
// transcript handed to the summarizer.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("… (%d more chars)", len(s)-max)
}
