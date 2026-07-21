// Package textwidth measures and fits strings by terminal display width
// rather than by bytes or runes.
//
// Go's %-Ns padding counts runes, and slicing counts bytes — both are wrong
// for terminal layout: a CJK character occupies two columns but is one rune,
// and cutting a string at a byte offset can split a UTF-8 sequence into
// replacement characters. Every column that must line up goes through here.
package textwidth

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ellipsis marks a truncated string. It is one column wide.
const ellipsis = "…"

// Width returns the number of terminal columns a string occupies. ANSI
// escape sequences are not counted.
func Width(s string) int {
	return lipgloss.Width(s)
}

// Truncate shortens s to at most max columns, appending an ellipsis when
// anything was dropped. It never splits a rune, so multi-byte text cannot
// degrade into replacement characters.
func Truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if Width(s) <= max {
		return s
	}
	if max == 1 {
		return ellipsis
	}
	// Reserve one column for the ellipsis and accumulate whole runes until
	// the budget is spent.
	budget := max - 1
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := Width(string(r))
		if used+w > budget {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String() + ellipsis
}

// Pad left-aligns s in a field of the given column width, truncating when it
// does not fit so a column can never overflow into the next one.
func Pad(s string, width int) string {
	s = Truncate(s, width)
	if gap := width - Width(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}

// Wrap breaks s into lines of at most width columns, splitting on spaces.
// A word longer than the whole width is hard-split rather than allowed to
// overflow, so a long URL cannot break the layout.
func Wrap(s string, width int) []string {
	if width <= 0 {
		return nil
	}
	var lines []string
	var cur strings.Builder
	curWidth := 0

	flush := func() {
		if cur.Len() > 0 {
			lines = append(lines, cur.String())
			cur.Reset()
			curWidth = 0
		}
	}

	for _, word := range strings.Fields(s) {
		w := Width(word)
		// A word that cannot fit on any line is split across lines.
		for w > width {
			flush()
			head := Truncate(word, width+1) // +1 leaves no room for an ellipsis
			head = strings.TrimSuffix(head, ellipsis)
			if head == "" {
				break
			}
			lines = append(lines, head)
			word = word[len(head):]
			w = Width(word)
		}
		if word == "" {
			continue
		}
		switch {
		case curWidth == 0:
			cur.WriteString(word)
			curWidth = w
		case curWidth+1+w <= width:
			cur.WriteString(" ")
			cur.WriteString(word)
			curWidth += 1 + w
		default:
			flush()
			cur.WriteString(word)
			curWidth = w
		}
	}
	flush()
	return lines
}

// WriteList renders name/description pairs as an aligned two-column list.
//
// Key flow: the name column is sized to the longest name (bounded), and the
// description is word-wrapped into the remaining width with a hanging
// indent, so continuation lines line up under the first instead of falling
// back to column zero and colliding with the names. Descriptions are capped
// at maxLines to keep a long list scannable.
func WriteList(w io.Writer, rows [][2]string, avail, maxLines int) {
	if len(rows) == 0 {
		return
	}
	const (
		gap          = 2
		nameColLimit = 30
		minDesc      = 24
	)
	nameWidth := 0
	for _, row := range rows {
		if n := Width(row[0]); n > nameWidth {
			nameWidth = n
		}
	}
	if nameWidth > nameColLimit {
		nameWidth = nameColLimit
	}
	descWidth := avail - nameWidth - gap
	if descWidth < minDesc {
		descWidth = minDesc
	}
	indent := strings.Repeat(" ", nameWidth+gap)

	for _, row := range rows {
		lines := Wrap(row[1], descWidth)
		if len(lines) > maxLines {
			// Mark the elision on the last kept line so it is obvious the
			// description continues.
			last := lines[maxLines-1]
			lines = lines[:maxLines]
			lines[maxLines-1] = Truncate(last+" …", descWidth)
		}
		if len(lines) == 0 {
			fmt.Fprintln(w, Pad(row[0], nameWidth))
			continue
		}
		fmt.Fprintf(w, "%s%s%s\n", Pad(row[0], nameWidth), strings.Repeat(" ", gap), lines[0])
		for _, l := range lines[1:] {
			fmt.Fprintf(w, "%s%s\n", indent, l)
		}
	}
}

// PadLeft right-aligns s in a field of the given column width.
func PadLeft(s string, width int) string {
	s = Truncate(s, width)
	if gap := width - Width(s); gap > 0 {
		return strings.Repeat(" ", gap) + s
	}
	return s
}
