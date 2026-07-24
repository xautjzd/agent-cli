package stats

import (
	"fmt"
	"strings"
	"time"
)

// Style carries the terminal color sequences the renderer paints with, kept as
// plain strings so this package stays independent of the theme package and
// renders as clean text when the caller passes empty sequences (tests, NO_COLOR).
type Style struct {
	Accent string
	Muted  string
	Bold   string
	Reset  string
}

func (s Style) paint(seq, text string) string {
	if seq == "" {
		return text
	}
	return seq + text + s.Reset
}

// intensity glyphs from empty to full, matching the "Less … More" legend.
var levels = []rune{'·', '░', '▒', '▓', '█'}

const gutter = "    " // 4-col left margin aligning weekday labels and the grid

// Render draws the full overview: activity heatmap, legend, range selector, and
// the headline figures. width aligns the two-column summary; st supplies colors.
func Render(s Summary, st Style, width int) string {
	var b strings.Builder
	b.WriteString(renderHeatmap(s.Heatmap, st))
	b.WriteString("\n")
	b.WriteString(renderLegend(st))
	b.WriteString("\n\n")
	b.WriteString(renderRangeBar(s.Range, st))
	b.WriteString("\n\n")
	b.WriteString(renderSummary(s, st, width))
	return b.String()
}

// renderHeatmap draws the month header plus seven weekday rows.
func renderHeatmap(hm Heatmap, st Style) string {
	var b strings.Builder
	b.WriteString(gutter + monthHeader(hm) + "\n")

	labels := [7]string{"", "Mon", "", "Wed", "", "Fri", ""}
	for d := 0; d < 7; d++ {
		lbl := labels[d]
		b.WriteString(st.paint(st.Muted, fmt.Sprintf("%-4s", lbl)))
		for w := 0; w < heatmapWeeks; w++ {
			b.WriteString(cell(hm.Counts[w][d], hm.Max, st))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// cell renders one day as an intensity glyph. Below-zero cells (beyond today)
// are blank; zero-activity cells are a muted dot; the rest scale with Max.
func cell(n, max int, st Style) string {
	if n < 0 {
		return " "
	}
	if n == 0 || max <= 0 {
		return st.paint(st.Muted, string(levels[0]))
	}
	// Bucket 1..4 by share of the busiest day.
	lvl := 1 + (n*3)/max // n==max → 4
	if lvl > 4 {
		lvl = 4
	}
	return st.paint(st.Accent, string(levels[lvl]))
}

// monthHeader places 3-letter month abbreviations above the week columns where
// each month first appears.
func monthHeader(hm Heatmap) string {
	row := []byte(strings.Repeat(" ", heatmapWeeks))
	prev := time.Month(0)
	lastLabel := -3 // column of the last placed label; keep >=3 apart so none collide
	for w := 0; w < heatmapWeeks; w++ {
		m := hm.Start.AddDate(0, 0, w*7).Month()
		if m == prev {
			continue
		}
		prev = m
		if w-lastLabel < 3 {
			continue // too close to the previous label to fit "Jan" cleanly
		}
		abbr := hm.Start.AddDate(0, 0, w*7).Format("Jan")
		for i := 0; i < len(abbr) && w+i < heatmapWeeks; i++ {
			row[w+i] = abbr[i]
		}
		lastLabel = w
	}
	return string(row)
}

func renderLegend(st Style) string {
	var b strings.Builder
	b.WriteString(gutter)
	b.WriteString(st.paint(st.Muted, "Less "))
	for _, r := range levels[1:] {
		b.WriteString(st.paint(st.Accent, string(r)))
	}
	b.WriteString(st.paint(st.Muted, " More"))
	return b.String()
}

// renderRangeBar shows the three windows with the active one highlighted.
func renderRangeBar(active Range, st Style) string {
	parts := make([]string, len(Ranges))
	for i, r := range Ranges {
		if r == active {
			parts[i] = st.paint(st.Accent, r.Label())
		} else {
			parts[i] = st.paint(st.Muted, r.Label())
		}
	}
	return gutter + strings.Join(parts, st.paint(st.Muted, " · "))
}

// renderSummary lays out the headline figures in two aligned columns.
func renderSummary(s Summary, st Style, width int) string {
	label := func(k, v string) string {
		return st.paint(st.Muted, k+": ") + st.paint(st.Bold+st.Accent, v)
	}

	activeDays := fmt.Sprintf("%d", s.ActiveDays)
	if s.TotalDays > 0 {
		activeDays = fmt.Sprintf("%d%s", s.ActiveDays, st.paint(st.Muted, fmt.Sprintf("/%d", s.TotalDays)))
	}
	mostActive := "—"
	if !s.MostActiveDay.IsZero() {
		mostActive = s.MostActiveDay.Format("Jan 2")
	}
	favModel := s.FavoriteModel
	if favModel == "" {
		favModel = "—"
	}

	left := []string{
		label("Favorite model", favModel),
		"",
		label("Sessions", fmt.Sprintf("%d", s.Sessions)),
		label("Active days", activeDays),
		label("Most active day", mostActive),
	}
	right := []string{
		label("Total tokens", abbrevTokens(s.TotalTokens)),
		"",
		label("Longest session", humanizeDuration(s.LongestSession)),
		label("Longest streak", plural(s.LongestStreak, "day")),
		label("Current streak", plural(s.CurrentStreak, "day")),
	}

	col := 44
	if width > 0 && width/2 > col {
		col = width / 2
	}
	var b strings.Builder
	for i := range left {
		b.WriteString(gutter)
		b.WriteString(padVisible(left[i], col))
		b.WriteString(right[i])
		b.WriteString("\n")
	}

	if books := s.BookEquivalent(); books > 0 {
		b.WriteString("\n" + gutter)
		b.WriteString(st.paint(st.Accent, fmt.Sprintf(
			"You've used ~%s more tokens than The Catcher in the Rye", plainInt(books)+"x")))
		b.WriteString("\n")
	}
	return b.String()
}

// abbrevTokens renders a token count compactly (1.2k, 3.4m), matching /usage.
func abbrevTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fm", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// humanizeDuration renders a span like "5d 5h 39m", dropping leading zero units
// and collapsing to "0m" for nothing.
func humanizeDuration(d time.Duration) string {
	if d <= 0 {
		return "0m"
	}
	days := int(d / (24 * time.Hour))
	d -= time.Duration(days) * 24 * time.Hour
	hours := int(d / time.Hour)
	d -= time.Duration(hours) * time.Hour
	mins := int(d / time.Minute)

	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 || days > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	parts = append(parts, fmt.Sprintf("%dm", mins))
	return strings.Join(parts, " ")
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

func plainInt(n int) string { return fmt.Sprintf("%d", n) }

// padVisible right-pads s to n visible columns, ignoring the width of ANSI
// escape sequences so colored cells still align.
func padVisible(s string, n int) string {
	w := visibleWidth(s)
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
}

// visibleWidth counts runes outside ANSI SGR escape sequences. It assumes
// single-width runes, which holds for the ASCII labels and digits used here.
func visibleWidth(s string) int {
	w, inEsc := 0, false
	for _, r := range s {
		switch {
		case r == '\033':
			inEsc = true
		case inEsc && r == 'm':
			inEsc = false
		case inEsc:
			// still inside the escape
		default:
			w++
		}
	}
	return w
}
