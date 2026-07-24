package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/muesli/termenv"

	"github.com/xautjzd/agent-cli/internal/theme"
)

// TestMain forces a 16-color profile so the terminal renderer emits ANSI
// sequences the color-assertion tests can check (a plain test buffer would
// otherwise auto-detect no-color).
func TestMain(m *testing.M) {
	theme.SetColorProfile(termenv.ANSI)
	os.Exit(m.Run())
}

func TestStreamRendering(t *testing.T) {
	var buf strings.Builder
	e := &terminalEvents{out: &buf}

	e.OnThinkingDelta("first ")
	e.OnThinkingDelta("second")
	e.OnAssistantDelta("Hello")
	e.OnAssistantDelta(" world")
	e.OnStreamEnd()
	out := buf.String()

	if !strings.Contains(out, "✻ Thinking…") || !strings.Contains(out, "first second") {
		t.Errorf("thinking stream wrong:\n%q", out)
	}
	if !strings.Contains(out, "Hello world") {
		t.Errorf("text stream wrong:\n%q", out)
	}
	// Thinking styling is closed before the answer begins.
	if strings.Index(out, "second"+theme.Current().Reset) > strings.Index(out, "Hello") {
		t.Errorf("style not reset before answer:\n%q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("stream end must terminate the line:\n%q", out)
	}

	// A stream with no output at all (pure tool-call round) prints nothing.
	buf.Reset()
	e.OnStreamEnd()
	if buf.Len() != 0 {
		t.Errorf("empty stream should render nothing, got %q", buf.String())
	}
}

func TestDiffDetectionAndColorization(t *testing.T) {
	var buf strings.Builder
	e := &terminalEvents{out: &buf}
	e.lastCall = "EditFile(code.go)"

	e.OnToolResult("edit_file", strings.Join([]string{
		"Edited code.go (+1 -1)",
		"    1   package main",
		"    2 - old line",
		"    2 + new line",
	}, "\n"), true)
	out := buf.String()

	// The full diff is rendered, not collapsed to a one-line preview.
	if strings.Contains(out, "(+3 lines)") {
		t.Errorf("diff was collapsed to a preview:\n%q", out)
	}
	if !strings.Contains(out, "Edited code.go (+1 -1)") {
		t.Errorf("missing header in:\n%q", out)
	}
	// Removals render red, additions green (marker coloring survives even when
	// the code itself is syntax-highlighted).
	if !strings.Contains(out, theme.Current().Error) {
		t.Errorf("removal not colored:\n%q", out)
	}
	if !strings.Contains(out, theme.Current().Success) {
		t.Errorf("addition not colored:\n%q", out)
	}

	// Non-diff output keeps the compact one-line preview.
	buf.Reset()
	e.OnToolResult("bash", "line one\nline two\nline three", true)
	if !strings.Contains(buf.String(), "(+2 lines)") {
		t.Errorf("non-diff result should stay collapsed:\n%q", buf.String())
	}
}

func TestFormatTokens(t *testing.T) {
	cases := map[int]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567"}
	for in, want := range cases {
		if got := formatTokens(in); got != want {
			t.Errorf("formatTokens(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	if got := formatDuration(4200 * time.Millisecond); got != "4.2s" {
		t.Errorf("formatDuration = %q", got)
	}
	if got := formatDuration(63 * time.Second); got != "1m03s" {
		t.Errorf("formatDuration = %q", got)
	}
}

func TestCamelName(t *testing.T) {
	cases := map[string]string{
		"bash":       "Bash",
		"read_file":  "ReadFile",
		"write_file": "WriteFile",
		"edit_file":  "EditFile",
		"glob":       "Glob",
		"grep":       "Grep",
		"list_dir":   "ListDir",
		"use_skill":  "UseSkill",
		"remember":   "Remember",
		"forget":     "Forget",
	}
	for in, want := range cases {
		if got := camelName(in); got != want {
			t.Errorf("camelName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCompactArgs(t *testing.T) {
	// Primary argument is extracted.
	if got := compactArgs(`{"command":"go test ./...","timeout_seconds":60}`); got != "go test ./..." {
		t.Errorf("compactArgs command = %q", got)
	}
	if got := compactArgs(`{"path":"a/b.go"}`); got != "a/b.go" {
		t.Errorf("compactArgs path = %q", got)
	}
	// Fallback renders sorted key-value pairs.
	if got := compactArgs(`{"old_string":"x","new_string":"y"}`); got != "new_string: y, old_string: x" {
		t.Errorf("compactArgs fallback = %q", got)
	}
	// Invalid JSON degrades to a truncated one-liner.
	if got := compactArgs("not json\nsecond line"); got != "not json second line" {
		t.Errorf("compactArgs invalid = %q", got)
	}
}
