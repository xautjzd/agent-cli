// Package mdstream renders streamed assistant Markdown to a terminal, focused
// on the two things chat output most needs to get right: fenced code blocks,
// which are syntax-highlighted, and diff blocks, whose additions and removals
// are colored green and red so the change is legible at a glance. Prose outside
// code fences passes through with a caller-chosen tint.
//
// It is line-oriented and stateful. Callers Write raw model fragments as they
// arrive off the wire; the renderer reassembles complete lines, tracks whether
// it is inside a fence, and emits styled output. A code block is buffered in
// full and highlighted on its closing fence, because a syntax highlighter needs
// the whole block — so code appears at once when the fence closes while
// surrounding prose still streams live. Close flushes any unterminated line or
// block, keeping partial output from an interrupted turn.
package mdstream

import (
	"io"
	"strings"

	"github.com/xautjzd/agent-cli/internal/theme"
)

// Renderer turns a stream of Markdown fragments into styled terminal output.
// The zero value is not usable; construct one with New.
type Renderer struct {
	out io.Writer
	// tint is the SGR sequence applied to prose lines (typically the theme's
	// assistant-answer tint); "" leaves prose in the terminal default.
	tint string

	line    strings.Builder // the current, not-yet-terminated line
	inFence bool            // whether we are inside a fenced code block
	fence   string          // the exact opening delimiter, so ``` and ~~~ pair correctly
	lang    string          // the open fence's info string (language)
	block   []string        // buffered lines of the open code block
}

// New returns a Renderer writing styled output to out. tint is the SGR
// open-sequence applied to prose lines (e.g. theme.Current().Text); pass "" to
// leave prose unstyled.
func New(out io.Writer, tint string) *Renderer {
	return &Renderer{out: out, tint: tint}
}

// Write feeds one streamed fragment. It emits every line the fragment
// completes and retains any trailing partial line for the next call.
func (r *Renderer) Write(s string) {
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			r.line.WriteString(s)
			return
		}
		r.line.WriteString(s[:i])
		r.emitLine(r.line.String())
		r.line.Reset()
		s = s[i+1:]
	}
}

// Close flushes buffered state at the end of a stream: a trailing partial line,
// and an unterminated code block (rendered best-effort). It is safe to call
// once per stream; the Renderer must not be reused afterward.
func (r *Renderer) Close() {
	if r.line.Len() > 0 {
		r.emitLine(r.line.String())
		r.line.Reset()
	}
	if r.inFence {
		// The model never closed the fence (or the turn was interrupted):
		// flush what we have so no output is lost.
		r.flushBlock()
		r.inFence = false
	}
}

// emitLine routes one complete line by fence state.
func (r *Renderer) emitLine(line string) {
	if marker, lang, ok := fenceInfo(line); ok {
		if !r.inFence {
			r.inFence, r.fence, r.lang, r.block = true, marker, lang, nil
			return
		}
		// A fence line inside a block closes it only when the delimiter
		// matches the opener; otherwise it is content.
		if strings.HasPrefix(marker, r.fence) && lang == "" {
			r.flushBlock()
			r.inFence, r.fence, r.lang, r.block = false, "", "", nil
			return
		}
	}
	if r.inFence {
		r.block = append(r.block, line)
		return
	}
	r.writeProse(line)
}

// writeProse prints one non-code line with the prose tint.
func (r *Renderer) writeProse(line string) {
	if r.tint == "" {
		io.WriteString(r.out, line+"\n")
		return
	}
	io.WriteString(r.out, r.tint+line+theme.Current().Reset+"\n")
}

// flushBlock renders the buffered code block: diff coloring when it is a diff,
// otherwise syntax highlighting. The block is emitted verbatim (no added
// indentation) so wide code is not pushed off narrow terminals.
func (r *Renderer) flushBlock() {
	code := strings.Join(r.block, "\n")
	var rendered string
	switch {
	case isDiffLang(r.lang) || (r.lang == "" && looksLikeDiff(r.block)):
		rendered = renderDiff(r.block)
	default:
		rendered = highlight(code, r.lang)
	}
	io.WriteString(r.out, rendered+"\n")
}

// fenceInfo reports whether a line opens or closes a code fence, returning the
// run of backticks/tildes and the trimmed info string (language). A fence is at
// least three of the same character, optionally indented up to three spaces per
// CommonMark; the info string is only meaningful on an opening fence.
func fenceInfo(line string) (marker, lang string, ok bool) {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 {
		return "", "", false
	}
	var ch byte
	switch {
	case strings.HasPrefix(t, "```"):
		ch = '`'
	case strings.HasPrefix(t, "~~~"):
		ch = '~'
	default:
		return "", "", false
	}
	n := 0
	for n < len(t) && t[n] == ch {
		n++
	}
	info := strings.TrimSpace(t[n:])
	// A backtick fence's info string may not contain a backtick (CommonMark),
	// which rules out inline code like `x` being mistaken for a fence.
	if ch == '`' && strings.ContainsRune(info, '`') {
		return "", "", false
	}
	return t[:n], info, true
}
