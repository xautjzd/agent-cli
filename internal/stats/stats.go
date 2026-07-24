// Package stats turns raw session history into the numbers behind the /stats
// overview — a GitHub-style activity heatmap plus headline figures (sessions,
// streaks, active days, favorite model, …), modeled on Claude Code's Stats
// panel.
//
// Computation is deliberately separated from rendering (see render.go) and from
// I/O (callers gather session metadata and token totals): everything here is a
// pure function of its inputs, so it is exhaustively unit-testable without a
// terminal or the filesystem.
package stats

import (
	"sort"
	"time"
)

// bookTokens approximates the token count of a short novel (The Catcher in the
// Rye, ~73k words), used for the playful "you've read N books' worth" line.
// Claude Code uses the same comparison.
const bookTokens = 95_000

// Session is the minimal view of a recorded session that the stats need. It
// mirrors session.Meta but keeps this package free of that dependency (and thus
// trivially testable).
type Session struct {
	Model     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Range is the time window the headline figures are computed over. The heatmap
// itself always spans the trailing year regardless of range.
type Range int

const (
	AllTime Range = iota
	Last7
	Last30
)

// Ranges lists the selectable windows in cycle order.
var Ranges = []Range{AllTime, Last7, Last30}

// Label is the human name shown in the range selector.
func (r Range) Label() string {
	switch r {
	case Last7:
		return "Last 7 days"
	case Last30:
		return "Last 30 days"
	default:
		return "All time"
	}
}

// days is the window length; 0 means unbounded (all time).
func (r Range) days() int {
	switch r {
	case Last7:
		return 7
	case Last30:
		return 30
	default:
		return 0
	}
}

// heatmapWeeks is the number of week-columns in the activity grid (~1 year).
const heatmapWeeks = 53

// Summary is the fully computed stats for one range, ready to render.
type Summary struct {
	Range Range

	Sessions        int
	ActiveDays      int // distinct days with activity in the window
	TotalDays       int // window length in days (denominator for ActiveDays)
	LongestStreak   int
	CurrentStreak   int
	MostActiveDay   time.Time // zero when there is no activity
	MostActiveCount int
	LongestSession  time.Duration

	// FavoriteModel and TotalTokens are all-time figures (per-day token history
	// isn't recorded), shown regardless of Range.
	FavoriteModel string
	TotalTokens   int

	Heatmap Heatmap
}

// BookEquivalent is how many short novels TotalTokens is worth (>=0). Zero
// tokens yields 0, so the caller can suppress the line.
func (s Summary) BookEquivalent() int { return s.TotalTokens / bookTokens }

// Heatmap is a GitHub-style contribution grid: columns are ISO-ish weeks (each
// starting Sunday) and rows are weekdays (Sun..Sat). It always spans the
// trailing year ending at the reference day.
type Heatmap struct {
	// Counts[week][weekday] is the session count for that day. A negative value
	// marks a cell outside the covered range (a future day in the current
	// week), which renders blank.
	Counts [heatmapWeeks][7]int
	Max    int
	// Start is the date of Counts[0][0] (the Sunday of the leftmost column).
	Start time.Time
}

// Compute derives the full summary for a range from all sessions plus the
// all-time token total. now anchors "today" (callers pass time.Now; tests pin
// it). favoriteModel and totalTokens are all-time and passed through.
func Compute(sessions []Session, totalTokens int, now time.Time) map[Range]Summary {
	out := make(map[Range]Summary, len(Ranges))
	fav := favoriteModel(sessions)
	hm := buildHeatmap(sessions, now)
	for _, r := range Ranges {
		s := computeRange(sessions, r, now)
		s.FavoriteModel = fav
		s.TotalTokens = totalTokens
		s.Heatmap = hm
		out[r] = s
	}
	return out
}

// computeRange computes the window-dependent figures for one range.
func computeRange(sessions []Session, r Range, now time.Time) Summary {
	today := dayOf(now)
	s := Summary{Range: r}

	var cutoff time.Time
	if d := r.days(); d > 0 {
		cutoff = today.AddDate(0, 0, -(d - 1)) // inclusive window of d days ending today
	}

	perDay := map[time.Time]int{}
	var earliest time.Time
	for _, sess := range sessions {
		day := dayOf(sess.CreatedAt)
		if !cutoff.IsZero() && day.Before(cutoff) {
			continue
		}
		if day.After(today) {
			continue // ignore clock-skewed future sessions
		}
		s.Sessions++
		perDay[day]++
		if dur := sess.UpdatedAt.Sub(sess.CreatedAt); dur > s.LongestSession {
			s.LongestSession = dur
		}
		if earliest.IsZero() || day.Before(earliest) {
			earliest = day
		}
	}

	s.ActiveDays = len(perDay)
	for day, n := range perDay {
		if n > s.MostActiveCount || (n == s.MostActiveCount && day.After(s.MostActiveDay)) {
			s.MostActiveCount = n
			s.MostActiveDay = day
		}
	}

	switch {
	case r.days() > 0:
		s.TotalDays = r.days()
	case !earliest.IsZero():
		s.TotalDays = int(today.Sub(earliest).Hours()/24) + 1
	}

	s.LongestStreak, s.CurrentStreak = streaks(perDay, today)
	return s
}

// streaks returns the longest run of consecutive active days and the current
// run ending today (or yesterday — an active streak the user hasn't added to
// yet today still counts as current).
func streaks(perDay map[time.Time]int, today time.Time) (longest, current int) {
	if len(perDay) == 0 {
		return 0, 0
	}
	days := make([]time.Time, 0, len(perDay))
	for d := range perDay {
		days = append(days, d)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })

	run := 1
	longest = 1
	for i := 1; i < len(days); i++ {
		if days[i].Sub(days[i-1]) == 24*time.Hour {
			run++
		} else {
			run = 1
		}
		if run > longest {
			longest = run
		}
	}

	// Current streak: walk back from today while days are present. A gap of one
	// day (nothing yet today) is tolerated so an ongoing streak still shows.
	active := func(d time.Time) bool { _, ok := perDay[d]; return ok }
	cursor := today
	if !active(cursor) {
		cursor = today.AddDate(0, 0, -1) // allow "haven't logged today yet"
	}
	for active(cursor) {
		current++
		cursor = cursor.AddDate(0, 0, -1)
	}
	return longest, current
}

// buildHeatmap lays out the trailing-year grid of daily session counts.
func buildHeatmap(sessions []Session, now time.Time) Heatmap {
	today := dayOf(now)
	// Leftmost column starts on the Sunday (heatmapWeeks-1) weeks before this
	// week's Sunday, so the last column contains today.
	startOfWeek := today.AddDate(0, 0, -int(today.Weekday()))
	start := startOfWeek.AddDate(0, 0, -7*(heatmapWeeks-1))

	perDay := map[time.Time]int{}
	for _, s := range sessions {
		perDay[dayOf(s.CreatedAt)]++
	}

	hm := Heatmap{Start: start}
	for w := 0; w < heatmapWeeks; w++ {
		for d := 0; d < 7; d++ {
			day := start.AddDate(0, 0, w*7+d)
			if day.After(today) {
				hm.Counts[w][d] = -1 // beyond today: blank cell
				continue
			}
			n := perDay[day]
			hm.Counts[w][d] = n
			if n > hm.Max {
				hm.Max = n
			}
		}
	}
	return hm
}

// favoriteModel is the most-used model across all sessions (by session count),
// ties broken by name for determinism. Empty when no session names a model.
func favoriteModel(sessions []Session) string {
	counts := map[string]int{}
	for _, s := range sessions {
		if s.Model != "" {
			counts[s.Model]++
		}
	}
	best, bestN := "", 0
	for m, n := range counts {
		if n > bestN || (n == bestN && m < best) {
			best, bestN = m, n
		}
	}
	return best
}

// dayOf truncates a timestamp to midnight in its own location, so all
// per-day bucketing agrees on day boundaries.
func dayOf(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}
