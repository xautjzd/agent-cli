package mdstream

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/muesli/termenv"

	"github.com/xautjzd/agent-cli/internal/theme"
)

// hunkHeaderRe matches a unified-diff hunk header ("@@ -1,4 +1,6 @@ …"), the
// strongest signal that an unlabeled code block is actually a diff.
var hunkHeaderRe = regexp.MustCompile(`^@@ .* @@`)

// isDiffLang reports whether a fence info string names a diff.
func isDiffLang(lang string) bool {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "diff", "patch", "udiff":
		return true
	}
	return false
}

// looksLikeDiff reports whether an unlabeled block is a unified diff, using
// signals specific enough to avoid misreading ordinary code (a YAML list, say)
// that merely contains '-' lines: a hunk header, or a git/unified file-header
// pair.
func looksLikeDiff(lines []string) bool {
	sawOldFile := false
	for _, l := range lines {
		switch {
		case hunkHeaderRe.MatchString(l):
			return true
		case strings.HasPrefix(l, "diff --git "):
			return true
		case strings.HasPrefix(l, "--- "):
			sawOldFile = true
		case strings.HasPrefix(l, "+++ ") && sawOldFile:
			return true
		}
	}
	return false
}

// renderDiff colorizes a diff block GitHub-style: each changed line's code is
// syntax-highlighted (the language inferred from the diff's file path), and the
// add/remove signal is carried by a subtle line background plus a colored
// +/- marker — so a reader gets both syntax colors and change legibility at
// once. Hunk headers use the accent color and file headers are dimmed.
//
// When the language is unknown or the terminal cannot render backgrounds, it
// falls back to whole-line foreground coloring (additions green, removals red),
// which stays legible everywhere.
func renderDiff(lines []string) string {
	th := theme.Current()
	hl := highlighter("", diffFilename(lines), strings.Join(lines, "\n"))
	addBg, delBg := diffBackgrounds()
	var sb strings.Builder
	for i, l := range lines {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(renderDiffLine(th, hl, addBg, delBg, l))
	}
	return sb.String()
}

// renderDiffLine styles one diff line. hl (may be nil) syntax-highlights code;
// addBg/delBg (may be empty) are the line backgrounds for additions/removals.
func renderDiffLine(th theme.Theme, hl func(string) string, addBg, delBg, line string) string {
	switch {
	case isFileHeader(line):
		return th.Paint(th.Muted, line)
	case hunkHeaderRe.MatchString(line):
		return th.Paint(th.Accent, line)
	}

	// Without a highlighter, keep the robust whole-line foreground coloring.
	if hl == nil {
		return th.Paint(diffLineSeq(th, line), line)
	}

	switch {
	case strings.HasPrefix(line, "+"):
		return changedLine(th, th.Success, addBg, "+", hl(line[1:]))
	case strings.HasPrefix(line, "-"):
		return changedLine(th, th.Error, delBg, "-", hl(line[1:]))
	default:
		// Context line: syntax-highlight it, no marker recolor or background.
		return hl(line)
	}
}

// changedLine renders one added/removed line: a marker painted in the change
// color, the syntax-highlighted code, and a line-spanning background. Because
// the highlighter resets styling after every token (which also clears the
// background), the background is re-asserted after each reset so it covers the
// whole line.
func changedLine(th theme.Theme, markerSeq, bg, marker, code string) string {
	body := th.Paint(markerSeq, marker) + code
	if bg == "" {
		return body // no background support: the colored marker carries the signal.
	}
	return bg + strings.ReplaceAll(body, th.Reset, th.Reset+bg) + th.Reset
}

// isFileHeader reports whether a diff line is a file header rather than an
// added/removed content line, so "+++"/"---" are dimmed, not painted green/red.
func isFileHeader(line string) bool {
	return strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "--- ") ||
		strings.HasPrefix(line, "diff ") || strings.HasPrefix(line, "index ")
}

// diffLineSeq picks the whole-line color role used in the no-highlighter
// fallback path.
func diffLineSeq(th theme.Theme, line string) string {
	switch {
	case isFileHeader(line):
		return th.Muted
	case hunkHeaderRe.MatchString(line):
		return th.Accent
	case strings.HasPrefix(line, "+"):
		return th.Success
	case strings.HasPrefix(line, "-"):
		return th.Error
	default:
		return th.Text
	}
}

// diffFilename extracts the changed file's path from a diff's headers so the
// content can be highlighted for the right language. The new-file header
// ("+++ b/…") is preferred over the old one; "/dev/null" and a missing header
// yield "".
func diffFilename(lines []string) string {
	for _, l := range lines {
		if p, ok := strings.CutPrefix(l, "+++ "); ok {
			if f := cleanDiffPath(p); f != "" {
				return f
			}
		}
	}
	for _, l := range lines {
		if p, ok := strings.CutPrefix(l, "--- "); ok {
			if f := cleanDiffPath(p); f != "" {
				return f
			}
		}
	}
	return ""
}

// cleanDiffPath normalizes a diff header path: it drops a trailing tab-delimited
// timestamp and the a/ or b/ prefix git adds, and maps /dev/null to "".
func cleanDiffPath(p string) string {
	p = strings.TrimSpace(p)
	if i := strings.IndexByte(p, '\t'); i >= 0 {
		p = p[:i]
	}
	p = strings.TrimPrefix(p, "a/")
	p = strings.TrimPrefix(p, "b/")
	if p == "/dev/null" {
		return ""
	}
	return p
}

// numberedDiffLineRe matches a rendered file-edit diff line as produced by
// diff.FileDiff.Unified: a right-aligned line number, a space, then the ' ',
// '+' or '-' marker and a space. It is the signal that a file-tool result
// carries a diff rather than a plain message.
var numberedDiffLineRe = regexp.MustCompile(`^\s*\d+ [ +-] `)

// numberedDiffParts splits a numbered diff line into its number column,
// change marker, and code, so each can be styled independently.
var numberedDiffParts = regexp.MustCompile(`^(\s*\d+) ([ +-]) (.*)$`)

// editSummaryRe strips the trailing change summary ("(+3 -1)"/"(no changes)")
// so the file path can be recovered from a file-edit header line.
var editSummaryRe = regexp.MustCompile(`\s*\((?:\+\d+ -\d+|no changes)\)\s*$`)

// FileEditDiff detects whether a file-tool result carries a numbered unified
// diff. Such results have a summary header on the first line followed by at
// least one numbered diff line. On a match it returns the header and the diff
// body; otherwise ok is false and callers fall back to a plain preview.
func FileEditDiff(result string) (header string, body []string, ok bool) {
	lines := strings.Split(result, "\n")
	if len(lines) < 2 {
		return "", nil, false
	}
	for _, l := range lines[1:] {
		if numberedDiffLineRe.MatchString(l) {
			return lines[0], lines[1:], true
		}
	}
	return "", nil, false
}

// RenderFileEditDiff colorizes a file-edit diff body for terminal display,
// matching the GitHub-style rendering used for diff blocks in assistant text:
// the code on each line is syntax-highlighted (language inferred from the path
// in header), additions and removals carry a colored marker and a subtle line
// background, and line numbers stay dimmed. It caps output at maxLines (<=0
// means unbounded) and notes how many lines were dropped.
func RenderFileEditDiff(header string, body []string, maxLines int) string {
	th := theme.Current()
	hl := highlighter("", editHeaderPath(header), strings.Join(diffCode(body), "\n"))
	addBg, delBg := diffBackgrounds()

	shown, truncated := body, 0
	if maxLines > 0 && len(shown) > maxLines {
		truncated, shown = len(shown)-maxLines, shown[:maxLines]
	}

	var sb strings.Builder
	for i, l := range shown {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(renderNumberedDiffLine(th, hl, addBg, delBg, l))
	}
	if truncated > 0 {
		if len(shown) > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(th.Paint(th.Muted, fmt.Sprintf("… %d more diff line(s)", truncated)))
	}
	return sb.String()
}

// renderNumberedDiffLine styles one numbered diff line. Lines that are not
// numbered (hunk "..." separators) are dimmed as-is.
func renderNumberedDiffLine(th theme.Theme, hl func(string) string, addBg, delBg, line string) string {
	m := numberedDiffParts.FindStringSubmatch(line)
	if m == nil {
		return th.Paint(th.Muted, line)
	}
	num, marker, code := th.Paint(th.Muted, m[1]+" "), m[2], m[3]

	// Without a highlighter, keep robust whole-line foreground coloring so the
	// change signal survives on limited terminals.
	if hl == nil {
		return num + th.Paint(diffLineSeq(th, marker+" "), marker+" "+code)
	}

	switch marker {
	case "+":
		return num + changedLine(th, th.Success, addBg, "+", " "+hl(code))
	case "-":
		return num + changedLine(th, th.Error, delBg, "-", " "+hl(code))
	default:
		return num + "  " + hl(code) // context: highlighted, no marker recolor.
	}
}

// diffCode returns just the code portion of each numbered diff line, used as a
// sample for language detection when the path alone is inconclusive.
func diffCode(body []string) []string {
	out := make([]string, 0, len(body))
	for _, l := range body {
		if m := numberedDiffParts.FindStringSubmatch(l); m != nil {
			out = append(out, m[3])
		}
	}
	return out
}

// editHeaderPath recovers the changed file's path from a file-edit header line
// (e.g. "Edited /path/file.go (+3 -1)"): the summary is stripped and the path
// is the last remaining field, which holds whether or not the verb itself
// contains spaces ("Edited (2 occurrences in) …").
func editHeaderPath(header string) string {
	fields := strings.Fields(editSummaryRe.ReplaceAllString(header, ""))
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

// diffBackgrounds returns the line-background SGR sequences for added and
// removed lines, tuned subtle against a dark or light theme. It returns empty
// strings on terminals without addressable backgrounds (16-color or none), so
// callers fall back to the colored-marker signal.
func diffBackgrounds() (add, del string) {
	light := theme.Current().Name == "light"
	switch theme.ActiveProfile() {
	case termenv.TrueColor:
		if light {
			return "\033[48;2;230;255;236m", "\033[48;2;255;235;233m"
		}
		return "\033[48;2;18;38;28m", "\033[48;2;45;26;29m"
	case termenv.ANSI256:
		if light {
			return "\033[48;5;194m", "\033[48;5;224m"
		}
		return "\033[48;5;22m", "\033[48;5;52m"
	default:
		return "", ""
	}
}
