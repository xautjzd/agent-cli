package repl

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/xautjzd/agent-cli/internal/agent"
	"github.com/xautjzd/agent-cli/internal/mdstream"
	"github.com/xautjzd/agent-cli/internal/theme"
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
	// render is the Markdown renderer for the in-flight answer stream: it
	// colorizes diff blocks and syntax-highlights code as fragments arrive.
	render *mdstream.Renderer
}

func newTUIEvents(out io.Writer) *tuiEvents { return &tuiEvents{out: out} }

func (e *tuiEvents) OnUserPrompt(text string) {
	th := theme.Current()
	fmt.Fprintf(e.out, "\n%s\n", th.Paint(th.Accent, "❯ "+text))
}

func (e *tuiEvents) OnThinking(text string) {
	th := theme.Current()
	fmt.Fprintf(e.out, "%s\n%s\n", th.Paint(th.Thinking, "✻ Thinking"), th.Paint(th.Thinking, text))
}

func (e *tuiEvents) OnAssistantText(text string) {
	r := mdstream.New(e.out, theme.Current().Text)
	r.Write(text)
	r.Close()
}

func (e *tuiEvents) OnToolCall(name, args string) {
	th := theme.Current()
	e.lastCall = fmt.Sprintf("%s(%s)", th.Paint(th.Accent, camelTool(name)), compactToolArgs(args))
	// Print immediately (warning dot = running) so long tools show progress.
	fmt.Fprintf(e.out, "%s %s\n", th.Paint(th.Warning, "●"), e.lastCall)
}

func (e *tuiEvents) OnToolResult(name, result string, ok bool) {
	th := theme.Current()
	marker := th.Success
	if !ok {
		marker = th.Error
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
	fmt.Fprintf(e.out, "  %s %s\n", th.Paint(marker, "⎿"), th.Paint(th.Muted, preview))
}

func (e *tuiEvents) OnTurnStats(s agent.TurnStats) {
	th := theme.Current()
	fmt.Fprintf(e.out, "%s\n", th.Paint(th.Muted, fmt.Sprintf(
		"⏱ %s · %d in + %d out · context %d tok",
		s.Duration.Round(time.Millisecond), s.PromptTokens, s.CompletionTokens, s.ContextTokens)))
}

// StreamEvents: fragments append live; the scrollback notify drives the redraw.

func (e *tuiEvents) OnThinkingDelta(text string) {
	th := theme.Current()
	if e.streaming != 1 {
		fmt.Fprintf(e.out, "%s\n%s", th.Paint(th.Thinking, "✻ Thinking"), th.Thinking)
		e.streaming = 1
	}
	fmt.Fprint(e.out, text)
}

func (e *tuiEvents) OnAssistantDelta(text string) {
	th := theme.Current()
	if e.streaming == 1 {
		fmt.Fprint(e.out, th.Reset+"\n") // close the thinking block
	}
	if e.streaming != 2 {
		// Route answer text through the Markdown renderer so diff blocks and
		// code are colorized as they stream in.
		e.render = mdstream.New(e.out, th.Text)
		e.streaming = 2
	}
	e.render.Write(text)
}

func (e *tuiEvents) OnStreamEnd() {
	if e.render != nil {
		e.render.Close()
		e.render = nil
	}
	if e.streaming != 0 {
		fmt.Fprint(e.out, theme.Current().Reset)
		fmt.Fprint(e.out, "\n")
	}
	e.streaming = 0
}

// OnCompaction reports an automatic context compaction.
func (e *tuiEvents) OnCompaction(s agent.CompactionStats) {
	th := theme.Current()
	fmt.Fprintf(e.out, "%s\n", th.Paint(th.Muted, fmt.Sprintf(
		"⊙ Compacted context (%s): %d→%d messages", s.Trigger, s.MessagesBefore, s.MessagesAfter)))
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
