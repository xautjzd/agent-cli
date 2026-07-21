package diff

import (
	"strings"
	"testing"
)

// render is a compact view of a diff for assertions: one "marker text" per
// line, ignoring line numbers.
func render(f *FileDiff) []string {
	var out []string
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			m := " "
			switch l.Kind {
			case Added:
				m = "+"
			case Removed:
				m = "-"
			}
			out = append(out, m+l.Text)
		}
	}
	return out
}

func TestComputeSingleLineChange(t *testing.T) {
	before := "a\nb\nc\n"
	after := "a\nB\nc\n"
	d := Compute("f.go", before, after, DefaultOptions())

	if d.Added != 1 || d.Removed != 1 {
		t.Errorf("counts = +%d -%d, want +1 -1", d.Added, d.Removed)
	}
	if d.Summary() != "(+1 -1)" {
		t.Errorf("summary = %q", d.Summary())
	}
	got := render(d)
	want := []string{" a", "-b", "+B", " c"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("diff = %v, want %v", got, want)
	}
}

func TestComputeLineNumbers(t *testing.T) {
	// Line numbers must refer to the correct version: removals use the old
	// numbering, additions the new one.
	d := Compute("f", "one\ntwo\nthree\n", "one\ntwo-changed\nthree\nfour\n", DefaultOptions())
	var removed, added []int
	for _, h := range d.Hunks {
		for _, l := range h.Lines {
			switch l.Kind {
			case Removed:
				removed = append(removed, l.OldNum)
			case Added:
				added = append(added, l.NewNum)
			}
		}
	}
	if len(removed) != 1 || removed[0] != 2 {
		t.Errorf("removed line numbers = %v, want [2]", removed)
	}
	if len(added) != 2 || added[0] != 2 || added[1] != 4 {
		t.Errorf("added line numbers = %v, want [2 4]", added)
	}
}

func TestComputeIdenticalAndEmpty(t *testing.T) {
	d := Compute("f", "same\n", "same\n", DefaultOptions())
	if len(d.Hunks) != 0 || d.Added != 0 || d.Removed != 0 {
		t.Errorf("identical files produced a diff: %+v", d)
	}
	if d.Summary() != "(no changes)" || d.Unified() != "" {
		t.Errorf("summary=%q unified=%q", d.Summary(), d.Unified())
	}

	// Creating content from nothing is all additions.
	d = Compute("f", "", "x\ny\n", DefaultOptions())
	if d.Added != 2 || d.Removed != 0 {
		t.Errorf("from empty: +%d -%d, want +2 -0", d.Added, d.Removed)
	}
	// Deleting everything is all removals.
	d = Compute("f", "x\ny\n", "", DefaultOptions())
	if d.Added != 0 || d.Removed != 2 {
		t.Errorf("to empty: +%d -%d, want +0 -2", d.Added, d.Removed)
	}
}

func TestComputeTrailingNewlineNotAChange(t *testing.T) {
	// A final newline must not register as an extra changed line.
	d := Compute("f", "a\nb\n", "a\nb\n", DefaultOptions())
	if d.Added != 0 || d.Removed != 0 {
		t.Errorf("trailing newline treated as change: %+v", d)
	}
}

func TestContextIsLimited(t *testing.T) {
	// A one-line change in a large file yields a small hunk, not the file.
	var before, after []string
	for i := 0; i < 100; i++ {
		before = append(before, "line")
		after = append(after, "line")
	}
	after[50] = "CHANGED"
	d := Compute("f", strings.Join(before, "\n")+"\n", strings.Join(after, "\n")+"\n",
		Options{Context: 2})

	if len(d.Hunks) != 1 {
		t.Fatalf("hunks = %d, want 1", len(d.Hunks))
	}
	// 2 context + 1 removal + 1 addition + 2 context.
	if got := len(d.Hunks[0].Lines); got != 6 {
		t.Errorf("hunk lines = %d, want 6 (context bounded)", got)
	}
}

func TestSeparateChangesProduceSeparateHunks(t *testing.T) {
	var before, after []string
	for i := 0; i < 60; i++ {
		before = append(before, "line")
		after = append(after, "line")
	}
	after[5] = "FIRST"
	after[50] = "SECOND"
	d := Compute("f", strings.Join(before, "\n"), strings.Join(after, "\n"), Options{Context: 2})

	if len(d.Hunks) != 2 {
		t.Fatalf("hunks = %d, want 2 separate change regions", len(d.Hunks))
	}
	// The rendered form separates hunks with an ellipsis marker.
	if !strings.Contains(d.Unified(), "...") {
		t.Errorf("unified output should mark the gap:\n%s", d.Unified())
	}
}

func TestHunkCapAndTruncationNote(t *testing.T) {
	var before, after []string
	for i := 0; i < 200; i++ {
		before = append(before, "line")
		after = append(after, "line")
	}
	// Widely separated changes → many hunks.
	for i := 5; i < 200; i += 20 {
		after[i] = "CHANGED"
	}
	d := Compute("f", strings.Join(before, "\n"), strings.Join(after, "\n"),
		Options{Context: 1, MaxHunks: 3})

	if len(d.Hunks) != 3 {
		t.Errorf("hunks = %d, want capped at 3", len(d.Hunks))
	}
	if d.Truncated == 0 {
		t.Error("truncated count not reported")
	}
	if !strings.Contains(d.Unified(), "more change block") {
		t.Errorf("truncation note missing:\n%s", d.Unified())
	}
	// Counts still reflect the whole file, not just rendered hunks.
	if d.Added != 10 || d.Removed != 10 {
		t.Errorf("counts = +%d -%d, want the full totals +10 -10", d.Added, d.Removed)
	}
}

func TestLargeFileFallback(t *testing.T) {
	// Beyond MaxLines the exact LCS is skipped, but counts stay correct.
	var before, after []string
	for i := 0; i < 50; i++ {
		before = append(before, "a")
		after = append(after, "b")
	}
	d := Compute("f", strings.Join(before, "\n"), strings.Join(after, "\n"),
		Options{Context: 1, MaxLines: 10})
	if d.Removed != 50 || d.Added != 50 {
		t.Errorf("fallback counts = +%d -%d, want +50 -50", d.Added, d.Removed)
	}
}

func TestUnifiedFormat(t *testing.T) {
	d := Compute("f", "keep\nold\n", "keep\nnew\n", DefaultOptions())
	out := d.Unified()
	// Each line carries a number and a marker, which the terminal layer
	// keys on for colorization.
	for _, want := range []string{"    1   keep", "    2 - old", "    2 + new"} {
		if !strings.Contains(out, want) {
			t.Errorf("unified output missing %q:\n%s", want, out)
		}
	}
}
