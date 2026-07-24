package stats

import (
	"testing"
	"time"
)

// day builds a midnight timestamp in UTC for a given year/month/day.
func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// sessOn is a session created at h:00 on the given day, lasting dur.
func sessOn(t time.Time, model string, dur time.Duration) Session {
	return Session{Model: model, CreatedAt: t, UpdatedAt: t.Add(dur)}
}

func TestComputeAllTimeBasics(t *testing.T) {
	now := day(2026, time.July, 24).Add(15 * time.Hour)
	sessions := []Session{
		sessOn(day(2026, time.July, 20), "kimi-k2.5", 90*time.Minute),
		sessOn(day(2026, time.July, 21), "kimi-k2.5", 30*time.Minute),
		sessOn(day(2026, time.July, 22), "claude-opus-4-8", 2*time.Hour),
		sessOn(day(2026, time.July, 22), "kimi-k2.5", 10*time.Minute), // two on the 22nd
		sessOn(day(2026, time.July, 24), "kimi-k2.5", 5*time.Minute),  // today
	}
	got := Compute(sessions, 200_000, now)
	all := got[AllTime]

	if all.Sessions != 5 {
		t.Errorf("Sessions = %d, want 5", all.Sessions)
	}
	if all.ActiveDays != 4 {
		t.Errorf("ActiveDays = %d, want 4", all.ActiveDays)
	}
	if all.FavoriteModel != "kimi-k2.5" {
		t.Errorf("FavoriteModel = %q, want kimi-k2.5", all.FavoriteModel)
	}
	if all.TotalTokens != 200_000 {
		t.Errorf("TotalTokens = %d, want 200000", all.TotalTokens)
	}
	if want := 2 * time.Hour; all.LongestSession != want {
		t.Errorf("LongestSession = %v, want %v", all.LongestSession, want)
	}
	if !all.MostActiveDay.Equal(day(2026, time.July, 22)) || all.MostActiveCount != 2 {
		t.Errorf("MostActiveDay = %v (%d), want Jul 22 (2)", all.MostActiveDay, all.MostActiveCount)
	}
	// TotalDays spans Jul 20 → Jul 24 inclusive = 5.
	if all.TotalDays != 5 {
		t.Errorf("TotalDays = %d, want 5", all.TotalDays)
	}
	// BookEquivalent: 200000 / 95000 = 2.
	if all.BookEquivalent() != 2 {
		t.Errorf("BookEquivalent = %d, want 2", all.BookEquivalent())
	}
}

func TestStreaks(t *testing.T) {
	now := day(2026, time.July, 24).Add(9 * time.Hour)
	// Active: Jul 10,11,12 (streak 3), gap, Jul 22,23,24 (current 3 ending today).
	sessions := []Session{
		sessOn(day(2026, time.July, 10), "m", time.Minute),
		sessOn(day(2026, time.July, 11), "m", time.Minute),
		sessOn(day(2026, time.July, 12), "m", time.Minute),
		sessOn(day(2026, time.July, 22), "m", time.Minute),
		sessOn(day(2026, time.July, 23), "m", time.Minute),
		sessOn(day(2026, time.July, 24), "m", time.Minute),
	}
	all := Compute(sessions, 0, now)[AllTime]
	if all.LongestStreak != 3 {
		t.Errorf("LongestStreak = %d, want 3", all.LongestStreak)
	}
	if all.CurrentStreak != 3 {
		t.Errorf("CurrentStreak = %d, want 3", all.CurrentStreak)
	}
}

// A streak that ended yesterday (nothing logged today yet) still counts as
// current, so the number doesn't reset to 0 mid-morning.
func TestCurrentStreakToleratesToday(t *testing.T) {
	now := day(2026, time.July, 24).Add(9 * time.Hour)
	sessions := []Session{
		sessOn(day(2026, time.July, 22), "m", time.Minute),
		sessOn(day(2026, time.July, 23), "m", time.Minute),
	}
	all := Compute(sessions, 0, now)[AllTime]
	if all.CurrentStreak != 2 {
		t.Errorf("CurrentStreak = %d, want 2 (ended yesterday)", all.CurrentStreak)
	}
}

// A stale streak (last activity 3 days ago) is not current.
func TestCurrentStreakBroken(t *testing.T) {
	now := day(2026, time.July, 24).Add(9 * time.Hour)
	sessions := []Session{sessOn(day(2026, time.July, 20), "m", time.Minute)}
	all := Compute(sessions, 0, now)[AllTime]
	if all.CurrentStreak != 0 {
		t.Errorf("CurrentStreak = %d, want 0", all.CurrentStreak)
	}
}

func TestRangeFiltering(t *testing.T) {
	now := day(2026, time.July, 24).Add(9 * time.Hour)
	sessions := []Session{
		sessOn(day(2026, time.June, 1), "old", time.Minute),    // >30d ago
		sessOn(day(2026, time.July, 10), "mid", time.Minute),   // within 30d, outside 7d
		sessOn(day(2026, time.July, 20), "recent", time.Minute), // within 7d
		sessOn(day(2026, time.July, 24), "recent", time.Minute), // today
	}
	got := Compute(sessions, 0, now)

	if n := got[AllTime].Sessions; n != 4 {
		t.Errorf("AllTime sessions = %d, want 4", n)
	}
	if n := got[Last30].Sessions; n != 3 {
		t.Errorf("Last30 sessions = %d, want 3", n)
	}
	if n := got[Last7].Sessions; n != 2 {
		t.Errorf("Last7 sessions = %d, want 2", n)
	}
	if d := got[Last7].TotalDays; d != 7 {
		t.Errorf("Last7 TotalDays = %d, want 7", d)
	}
	if d := got[Last30].TotalDays; d != 30 {
		t.Errorf("Last30 TotalDays = %d, want 30", d)
	}
}

func TestHeatmapLayout(t *testing.T) {
	now := day(2026, time.July, 24).Add(9 * time.Hour) // a Friday
	sessions := []Session{
		sessOn(day(2026, time.July, 24), "m", time.Minute),
		sessOn(day(2026, time.July, 24), "m", time.Minute), // 2 today
		sessOn(day(2026, time.July, 20), "m", time.Minute), // a Monday
	}
	hm := Compute(sessions, 0, now)[AllTime].Heatmap

	// Start is a Sunday, heatmapWeeks*7 days back and aligned to week start.
	if hm.Start.Weekday() != time.Sunday {
		t.Errorf("Heatmap.Start weekday = %v, want Sunday", hm.Start.Weekday())
	}
	// The last column, Friday row (weekday 5), is today with 2 sessions.
	if got := hm.Counts[heatmapWeeks-1][int(time.Friday)]; got != 2 {
		t.Errorf("today cell = %d, want 2", got)
	}
	if hm.Max != 2 {
		t.Errorf("Heatmap.Max = %d, want 2", hm.Max)
	}
	// Monday of the last week (Jul 20) had 1 session.
	if got := hm.Counts[heatmapWeeks-1][int(time.Monday)]; got != 1 {
		t.Errorf("Monday cell = %d, want 1", got)
	}
	// Days after today in the current week are blank (negative sentinel).
	if got := hm.Counts[heatmapWeeks-1][int(time.Saturday)]; got != -1 {
		t.Errorf("future cell = %d, want -1 (blank)", got)
	}
}

func TestEmptyIsSafe(t *testing.T) {
	now := day(2026, time.July, 24)
	all := Compute(nil, 0, now)[AllTime]
	if all.Sessions != 0 || all.FavoriteModel != "" || all.CurrentStreak != 0 {
		t.Errorf("empty summary not zero-valued: %+v", all)
	}
	// Render must not panic on empty input.
	_ = Render(all, Style{}, 80)
}
