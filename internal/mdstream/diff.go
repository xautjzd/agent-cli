package mdstream

import (
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
