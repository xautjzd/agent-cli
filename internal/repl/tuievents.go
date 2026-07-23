package repl

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/xautjzd/agent-cli/internal/agent"
)

// tuiEvents renders agent activity into the scrollback for the full-screen UI.
// Unlike the inline terminalEvents (package main), it never moves the cursor to
// recolor a status dot — cursor tricks corrupt a buffer the viewport re-renders
// — so it prints each event as plain, append-only, ANSI-colored text. Colors
// pass through the viewport fine; only cursor motion does not.
type tuiEvents struct {
	out io.Writer
	// streaming tracks whether the current streamed block is thinking (1),
	// answer text (2), or nothing (0), so spacing/headers are emitted once.
	streaming int
	lastCall  string
}

func newTUIEvents(out io.Writer) *tuiEvents { return &tuiEvents{out: out} }

// ANSI fragments (kept local so this file has no cross-package dependency).
const (
	tuiReset  = "\033[0m"
	tuiDim    = "\033[2m"
	tuiItalic = "\033[3m"
	tuiGreen  = "\033[32m"
	tuiRed    = "\033[31m"
	tuiYellow = "\033[33m"
)

func (e *tuiEvents) OnUserPrompt(text string) {
	fmt.Fprintf(e.out, "\n\033[36m❯\033[0m %s\n", text)
}

func (e *tuiEvents) OnThinking(text string) {
	fmt.Fprintf(e.out, "%s%s✻ Thinking%s\n%s%s%s%s\n",
		tuiDim, tuiItalic, tuiReset, tuiDim, tuiItalic, text, tuiReset)
}

func (e *tuiEvents) OnAssistantText(text string) {
	fmt.Fprintf(e.out, "%s\n", text)
}

func (e *tuiEvents) OnToolCall(name, args string) {
	e.lastCall = fmt.Sprintf("%s(%s)", camelTool(name), compactToolArgs(args))
	// Print immediately (yellow dot = running) so long tools show progress.
	fmt.Fprintf(e.out, "%s●%s %s\n", tuiYellow, tuiReset, e.lastCall)
}

func (e *tuiEvents) OnToolResult(name, result string, ok bool) {
	color := tuiGreen
	if !ok {
		color = tuiRed
	}
	// The todo list is meant to be read in full — show every line, indented.
	if name == "todo_write" {
		for _, line := range strings.Split(strings.TrimRight(result, "\n"), "\n") {
			fmt.Fprintf(e.out, "  %s\n", line)
		}
		return
	}
	// A short preview of the result under the call, dimmed.
	preview := firstLine(result, 200)
	fmt.Fprintf(e.out, "  %s⎿%s %s%s%s\n", color, tuiReset, tuiDim, preview, tuiReset)
}

func (e *tuiEvents) OnTurnStats(s agent.TurnStats) {
	fmt.Fprintf(e.out, "%s⏱ %s · %d in + %d out · context %d tok%s\n",
		tuiDim, s.Duration.Round(time.Millisecond), s.PromptTokens, s.CompletionTokens, s.ContextTokens, tuiReset)
}

// StreamEvents: fragments append live; the scrollback notify drives the redraw.

func (e *tuiEvents) OnThinkingDelta(text string) {
	if e.streaming != 1 {
		fmt.Fprintf(e.out, "%s%s✻ Thinking%s\n%s%s", tuiDim, tuiItalic, tuiReset, tuiDim, tuiItalic)
		e.streaming = 1
	}
	fmt.Fprint(e.out, text)
}

func (e *tuiEvents) OnAssistantDelta(text string) {
	if e.streaming == 1 {
		fmt.Fprint(e.out, tuiReset+"\n") // close the thinking block
	}
	e.streaming = 2
	fmt.Fprint(e.out, text)
}

func (e *tuiEvents) OnStreamEnd() {
	if e.streaming == 1 {
		fmt.Fprint(e.out, tuiReset)
	}
	if e.streaming != 0 {
		fmt.Fprint(e.out, "\n")
	}
	e.streaming = 0
}

// OnCompaction reports an automatic context compaction.
func (e *tuiEvents) OnCompaction(s agent.CompactionStats) {
	fmt.Fprintf(e.out, "%s⊙ Compacted context (%s): %d→%d messages%s\n",
		tuiDim, s.Trigger, s.MessagesBefore, s.MessagesAfter, tuiReset)
}

// camelTool renders a tool name in CamelCase (read_file → ReadFile).
func camelTool(name string) string {
	parts := strings.Split(name, "_")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

// compactToolArgs renders tool arguments one-line and short, preferring the
// primary argument (command, path, pattern, name) over the full JSON blob.
func compactToolArgs(args string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(args), &m); err != nil || len(m) == 0 {
		return oneLine(args, 70)
	}
	for _, key := range []string{"command", "path", "pattern", "name", "prompt", "description"} {
		if v, ok := m[key].(string); ok {
			return oneLine(v, 70)
		}
	}
	var parts []string
	for k, v := range m {
		parts = append(parts, fmt.Sprintf("%s: %v", k, v))
	}
	sort.Strings(parts)
	return oneLine(strings.Join(parts, ", "), 70)
}

func oneLine(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

// firstLine returns the first non-empty line of s, truncated to max columns.
func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}
