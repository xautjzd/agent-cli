// Package diff computes line-oriented differences between two versions of a
// file and renders them as a unified diff.
//
// It is pure computation with no I/O and no terminal awareness (SRP): tools
// use it to describe what an edit changed, and the display layer decides how
// to colorize the result.
package diff

import (
	"fmt"
	"strings"
)

// Kind classifies a line in a diff.
type Kind int

const (
	// Context is a line present in both versions.
	Context Kind = iota
	// Removed is a line only in the old version.
	Removed
	// Added is a line only in the new version.
	Added
)

// Line is one rendered diff line. OldNum/NewNum are 1-based line numbers in
// the respective versions, or 0 when the line does not exist there.
type Line struct {
	Kind   Kind
	OldNum int
	NewNum int
	Text   string
}

// Hunk is a contiguous run of changes plus its surrounding context.
type Hunk struct {
	Lines []Line
}

// FileDiff is the full comparison of one file.
type FileDiff struct {
	Path  string
	Hunks []Hunk
	// Added and Removed count changed lines, for the summary line.
	Added   int
	Removed int
	// Truncated reports that hunks were dropped to bound output size.
	Truncated int
}

// Options bound the work and the output size.
type Options struct {
	// Context is the number of unchanged lines kept around each change.
	Context int
	// MaxHunks caps how many hunks are rendered; the rest are counted in
	// Truncated. Zero means the default.
	MaxHunks int
	// MaxLines is the largest file (in lines) that will be diffed exactly.
	// Beyond it a whole-file replacement is reported instead, keeping the
	// quadratic LCS step off pathological inputs.
	MaxLines int
}

// DefaultOptions are tuned for terminal display of source edits.
func DefaultOptions() Options {
	return Options{Context: 3, MaxHunks: 12, MaxLines: 4000}
}

// Compute diffs two file versions.
//
// Key flow: identical prefixes and suffixes are trimmed first — most edits
// touch a small region, so this reduces the expensive middle to a few lines
// — then an LCS over the remainder produces the change script, which is
// grouped into hunks with surrounding context.
func Compute(path, before, after string, opt Options) *FileDiff {
	if opt.Context <= 0 {
		opt.Context = DefaultOptions().Context
	}
	if opt.MaxHunks <= 0 {
		opt.MaxHunks = DefaultOptions().MaxHunks
	}
	if opt.MaxLines <= 0 {
		opt.MaxLines = DefaultOptions().MaxLines
	}

	fd := &FileDiff{Path: path}
	if before == after {
		return fd
	}
	oldLines := splitLines(before)
	newLines := splitLines(after)

	// Trim the common head and tail; they are context at most.
	head := 0
	for head < len(oldLines) && head < len(newLines) && oldLines[head] == newLines[head] {
		head++
	}
	tail := 0
	for tail < len(oldLines)-head && tail < len(newLines)-head &&
		oldLines[len(oldLines)-1-tail] == newLines[len(newLines)-1-tail] {
		tail++
	}
	oldMid := oldLines[head : len(oldLines)-tail]
	newMid := newLines[head : len(newLines)-tail]

	var script []Line
	if len(oldMid) > opt.MaxLines || len(newMid) > opt.MaxLines {
		// Too large to diff precisely; report a wholesale replacement so
		// the caller still gets accurate counts.
		for i, t := range oldMid {
			script = append(script, Line{Kind: Removed, OldNum: head + i + 1, Text: t})
		}
		for i, t := range newMid {
			script = append(script, Line{Kind: Added, NewNum: head + i + 1, Text: t})
		}
	} else {
		script = lcsScript(oldMid, newMid, head)
	}

	// Re-attach trimmed context so hunks can show surrounding lines.
	full := make([]Line, 0, len(script)+head+tail)
	for i := 0; i < head; i++ {
		full = append(full, Line{Kind: Context, OldNum: i + 1, NewNum: i + 1, Text: oldLines[i]})
	}
	full = append(full, script...)
	for i := 0; i < tail; i++ {
		oldIdx := len(oldLines) - tail + i
		newIdx := len(newLines) - tail + i
		full = append(full, Line{
			Kind: Context, OldNum: oldIdx + 1, NewNum: newIdx + 1, Text: oldLines[oldIdx],
		})
	}

	for _, l := range full {
		switch l.Kind {
		case Added:
			fd.Added++
		case Removed:
			fd.Removed++
		}
	}
	fd.Hunks = group(full, opt)
	if len(fd.Hunks) > opt.MaxHunks {
		fd.Truncated = len(fd.Hunks) - opt.MaxHunks
		fd.Hunks = fd.Hunks[:opt.MaxHunks]
	}
	return fd
}

// splitLines splits content into lines, dropping the trailing empty element
// that a final newline produces so it is not reported as a change.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

// lcsScript produces the change script for the trimmed middle sections.
// offset is how many identical lines preceded them, so emitted line numbers
// refer to the whole file.
func lcsScript(oldMid, newMid []string, offset int) []Line {
	n, m := len(oldMid), len(newMid)
	// table[i][j] = LCS length of oldMid[i:] and newMid[j:].
	table := make([][]int, n+1)
	for i := range table {
		table[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if oldMid[i] == newMid[j] {
				table[i][j] = table[i+1][j+1] + 1
			} else if table[i+1][j] >= table[i][j+1] {
				table[i][j] = table[i+1][j]
			} else {
				table[i][j] = table[i][j+1]
			}
		}
	}

	var out []Line
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case oldMid[i] == newMid[j]:
			out = append(out, Line{Kind: Context, OldNum: offset + i + 1, NewNum: offset + j + 1, Text: oldMid[i]})
			i, j = i+1, j+1
		case table[i+1][j] >= table[i][j+1]:
			out = append(out, Line{Kind: Removed, OldNum: offset + i + 1, Text: oldMid[i]})
			i++
		default:
			out = append(out, Line{Kind: Added, NewNum: offset + j + 1, Text: newMid[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, Line{Kind: Removed, OldNum: offset + i + 1, Text: oldMid[i]})
	}
	for ; j < m; j++ {
		out = append(out, Line{Kind: Added, NewNum: offset + j + 1, Text: newMid[j]})
	}
	return out
}

// group slices the change script into hunks, keeping opt.Context unchanged
// lines on each side of a run of changes and dropping the rest.
func group(lines []Line, opt Options) []Hunk {
	var hunks []Hunk
	var cur []Line
	// pending holds trailing context that may either close the current hunk
	// or, if more changes follow soon, stay inside it.
	var pending []Line

	for _, l := range lines {
		if l.Kind == Context {
			if len(cur) == 0 {
				// Leading context: keep only the last opt.Context lines.
				pending = append(pending, l)
				if len(pending) > opt.Context {
					pending = pending[1:]
				}
				continue
			}
			pending = append(pending, l)
			if len(pending) > opt.Context*2 {
				// Far enough from the last change to close the hunk.
				cur = append(cur, pending[:opt.Context]...)
				hunks = append(hunks, Hunk{Lines: cur})
				cur = nil
				pending = pending[len(pending)-opt.Context:]
			}
			continue
		}
		// A change: any pending context belongs inside the hunk.
		cur = append(cur, pending...)
		pending = nil
		cur = append(cur, l)
	}
	if len(cur) > 0 {
		if len(pending) > opt.Context {
			pending = pending[:opt.Context]
		}
		cur = append(cur, pending...)
		hunks = append(hunks, Hunk{Lines: cur})
	}
	return hunks
}

// Summary renders the one-line change count, e.g. "(+3 -1)".
func (f *FileDiff) Summary() string {
	if f.Added == 0 && f.Removed == 0 {
		return "(no changes)"
	}
	return fmt.Sprintf("(+%d -%d)", f.Added, f.Removed)
}

// Unified renders the diff in a line-numbered unified form. Each line is
// prefixed with its number and one of ' ', '-' or '+', which is what the
// terminal layer colorizes.
func (f *FileDiff) Unified() string {
	if len(f.Hunks) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, h := range f.Hunks {
		if i > 0 {
			sb.WriteString("   ...\n")
		}
		for _, l := range h.Lines {
			num := l.NewNum
			marker := " "
			switch l.Kind {
			case Removed:
				num, marker = l.OldNum, "-"
			case Added:
				marker = "+"
			}
			fmt.Fprintf(&sb, "%5d %s %s\n", num, marker, l.Text)
		}
	}
	if f.Truncated > 0 {
		fmt.Fprintf(&sb, "   ... (%d more change block(s) not shown)\n", f.Truncated)
	}
	return strings.TrimRight(sb.String(), "\n")
}
