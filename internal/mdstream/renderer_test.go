package mdstream

import (
	"strings"
	"testing"

	"github.com/muesli/termenv"

	"github.com/xautjzd/agent-cli/internal/theme"
)

// withColor forces a color profile for the duration of a test and restores the
// detected one afterward, so assertions can inspect the emitted SGR sequences.
func withColor(t *testing.T, p termenv.Profile) {
	t.Helper()
	prev := theme.ActiveProfile()
	theme.SetColorProfile(p)
	t.Cleanup(func() { theme.SetColorProfile(prev) })
}

// render feeds the whole input as one fragment and returns the styled output.
func render(tint, input string) string {
	var sb strings.Builder
	r := New(&sb, tint)
	r.Write(input)
	r.Close()
	return sb.String()
}

func TestProsePassesThroughWithTint(t *testing.T) {
	withColor(t, termenv.Ascii) // color off: tint is empty, output is plain
	got := render("", "hello\nworld\n")
	if got != "hello\nworld\n" {
		t.Fatalf("prose not preserved: %q", got)
	}
}

func TestFenceMarkersAreStripped(t *testing.T) {
	withColor(t, termenv.Ascii)
	got := render("", "before\n```go\nx := 1\n```\nafter\n")
	// The ``` delimiters must not appear in the output.
	if strings.Contains(got, "```") {
		t.Fatalf("fence markers leaked into output: %q", got)
	}
	for _, want := range []string{"before", "x := 1", "after"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in output: %q", want, got)
		}
	}
}

func TestDiffLinesAreColored(t *testing.T) {
	withColor(t, termenv.TrueColor)
	theme.Set("dark")
	th := theme.Current()
	got := render("", "```diff\n-removed line\n+added line\n context line\n```\n")

	wantAdd := th.Success + "+added line" + th.Reset
	wantRemove := th.Error + "-removed line" + th.Reset
	if !strings.Contains(got, wantAdd) {
		t.Errorf("added line not painted green: %q", got)
	}
	if !strings.Contains(got, wantRemove) {
		t.Errorf("removed line not painted red: %q", got)
	}
}

func TestHunkHeaderAndFileHeaderColoring(t *testing.T) {
	withColor(t, termenv.TrueColor)
	theme.Set("dark")
	th := theme.Current()
	got := render("", "```diff\n--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,3 @@\n+new\n```\n")

	if !strings.Contains(got, th.Accent+"@@ -1,2 +1,3 @@"+th.Reset) {
		t.Errorf("hunk header not in accent color: %q", got)
	}
	// "+++ b/x.go" is a file header (muted), not an addition (green).
	if !strings.Contains(got, th.Muted+"+++ b/x.go"+th.Reset) {
		t.Errorf("+++ file header not muted: %q", got)
	}
}

func TestUnlabeledDiffIsDetected(t *testing.T) {
	withColor(t, termenv.TrueColor)
	theme.Set("dark")
	th := theme.Current()
	// No language on the fence, but a hunk header makes it a diff.
	got := render("", "```\n@@ -1 +1 @@\n-old\n+new\n```\n")
	if !strings.Contains(got, th.Success+"+new"+th.Reset) {
		t.Errorf("unlabeled diff addition not colored: %q", got)
	}
}

func TestYAMLIsNotMisreadAsDiff(t *testing.T) {
	withColor(t, termenv.TrueColor)
	theme.Set("dark")
	th := theme.Current()
	// A YAML list has '-' lines but no diff signal; it must not be red-colored.
	got := render("", "```yaml\n- apple\n- banana\n```\n")
	if strings.Contains(got, th.Error+"- apple") {
		t.Errorf("YAML list wrongly treated as a diff: %q", got)
	}
}

func TestFragmentedStreamReassemblesLines(t *testing.T) {
	withColor(t, termenv.TrueColor)
	theme.Set("dark")
	th := theme.Current()
	var sb strings.Builder
	r := New(&sb, "")
	// Split a diff block across arbitrary fragment boundaries mid-line.
	for _, frag := range []string{"```di", "ff\n-rem", "oved\n+ad", "ded\n``", "`\n"} {
		r.Write(frag)
	}
	r.Close()
	got := sb.String()
	if !strings.Contains(got, th.Error+"-removed"+th.Reset) {
		t.Errorf("removal lost across fragment boundaries: %q", got)
	}
	if !strings.Contains(got, th.Success+"+added"+th.Reset) {
		t.Errorf("addition lost across fragment boundaries: %q", got)
	}
	if strings.Contains(got, "```") {
		t.Errorf("fence markers leaked: %q", got)
	}
}

func TestUnterminatedBlockFlushedOnClose(t *testing.T) {
	withColor(t, termenv.TrueColor)
	theme.Set("dark")
	th := theme.Current()
	// The closing fence never arrives (interrupted turn); Close must still emit.
	got := render("", "```diff\n+kept\n")
	if !strings.Contains(got, th.Success+"+kept"+th.Reset) {
		t.Errorf("unterminated block not flushed: %q", got)
	}
}

func TestCodeBlockIsSyntaxHighlighted(t *testing.T) {
	withColor(t, termenv.TrueColor)
	theme.Set("dark")
	got := render("", "```go\npackage main\n```\n")
	// Highlighting injects SGR sequences the raw text does not contain.
	if !strings.Contains(got, "\033[") {
		t.Errorf("code block was not syntax-highlighted: %q", got)
	}
	if !strings.Contains(got, "package") {
		t.Errorf("code content missing after highlight: %q", got)
	}
}

func TestDiffContentIsSyntaxHighlighted(t *testing.T) {
	withColor(t, termenv.TrueColor)
	theme.Set("dark")
	th := theme.Current()
	// A diff whose header names a .go file: the code should be Go-highlighted
	// while the add/remove signal moves to the marker + a line background.
	got := render("", "```diff\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n+func main() {}\n```\n")

	// The '+' marker is still painted green.
	if !strings.Contains(got, th.Paint(th.Success, "+")) {
		t.Errorf("addition marker not painted green: %q", got)
	}
	// A truecolor foreground sequence proves the content was syntax-highlighted
	// (the plain diff path only ever emits palette-role sequences, never 38;2).
	if !strings.Contains(got, "\033[38;2;") {
		t.Errorf("diff content was not syntax-highlighted: %q", got)
	}
	// A background sequence marks the added line GitHub-style.
	if !strings.Contains(got, "\033[48;2;") {
		t.Errorf("added line has no background: %q", got)
	}
}

func TestFenceInfoRejectsInlineCode(t *testing.T) {
	// Inline code spans (`x`) must not be mistaken for a fence.
	if _, _, ok := fenceInfo("here is `code` inline"); ok {
		t.Errorf("inline code misread as a fence")
	}
	if m, lang, ok := fenceInfo("```go"); !ok || m != "```" || lang != "go" {
		t.Errorf("fence not parsed: marker=%q lang=%q ok=%v", m, lang, ok)
	}
}
