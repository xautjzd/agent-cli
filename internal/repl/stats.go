package repl

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/xautjzd/agent-cli/internal/session"
	"github.com/xautjzd/agent-cli/internal/stats"
	"github.com/xautjzd/agent-cli/internal/theme"
	"github.com/xautjzd/agent-cli/internal/usage"
)

// statsData is the precomputed overview for every range, gathered once when
// /stats opens and handed to the overlay for instant range-switching.
type statsData struct {
	byRange map[stats.Range]stats.Summary
}

// cmdStats renders the cross-project activity overview — a contribution
// heatmap plus headline figures (sessions, streaks, favorite model, …),
// modeled on Claude Code's Stats panel. In the full-screen TUI it opens an
// interactive overlay (cycle ranges, copy); otherwise it prints once.
func (r *Repl) cmdStats(_ context.Context, _ string) error {
	metas, err := session.ScanAllMeta()
	if err != nil {
		return fmt.Errorf("read session history: %w", err)
	}
	sessions := make([]stats.Session, len(metas))
	for i, m := range metas {
		sessions[i] = stats.Session{Model: m.Model, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
	}
	tokens := usage.SumTokens(session.AllUsagePaths())
	data := statsData{byRange: stats.Compute(sessions, tokens, time.Now())}

	if len(metas) == 0 && tokens == 0 {
		fmt.Fprintln(r.Out, "No activity yet — run a few sessions and check back.")
		return nil
	}

	// Interactive overlay when the full-screen TUI is running; otherwise a
	// one-shot print (piped output, plain-TTY fallback).
	if r.tuiStats != nil {
		r.tuiStats(data)
		return nil
	}
	fmt.Fprintln(r.Out)
	fmt.Fprintln(r.Out, stats.Render(data.byRange[stats.AllTime], statsStyle(), r.terminalWidth()))
	return nil
}

// statsStyle maps the active theme onto the colors the stats renderer paints
// with (bold is added for headline values when color is enabled at all).
func statsStyle() stats.Style {
	th := theme.Current()
	bold := ""
	if th.Reset != "" {
		bold = "\033[1m"
	}
	return stats.Style{Accent: th.Accent, Muted: th.Muted, Bold: bold, Reset: th.Reset}
}

// statsState is the live state of the open /stats overlay: the precomputed
// summaries and which range is currently shown.
type statsState struct {
	data   statsData
	idx    int    // index into stats.Ranges
	status string // transient feedback (e.g. "copied")
	reply  chan struct{}
}

func newStatsState(data statsData, reply chan struct{}) *statsState {
	return &statsState{data: data, reply: reply}
}

func (s *statsState) current() stats.Summary {
	return s.data.byRange[stats.Ranges[s.idx]]
}

// cycle advances to the next time range.
func (s *statsState) cycle() {
	s.idx = (s.idx + 1) % len(stats.Ranges)
	s.status = ""
}

// copy puts an uncolored render of the current view on the system clipboard.
func (s *statsState) copy() {
	plain := stats.Render(s.current(), stats.Style{}, 0)
	if err := clipboard.WriteAll(plain); err != nil {
		s.status = "⚠ copy failed: " + err.Error()
		return
	}
	s.status = "✓ copied to clipboard"
}

// view renders the overlay: the overview plus a footer hint line.
func (s *statsState) view(width int) string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(stats.Render(s.current(), statsStyle(), width))
	b.WriteString("\n\n")
	hint := "  r cycle ranges · ctrl+s copy · esc close"
	if s.status != "" {
		hint = "  " + s.status
	}
	b.WriteString(styleHint.Render(hint))
	return b.String()
}

// handleStatsKey drives the /stats overlay: 'r' cycles the range, Ctrl-S
// copies, and Esc/q/Ctrl-C close it.
func (m *tuiModel) handleStatsKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Type == tea.KeyEsc, key.Type == tea.KeyCtrlC,
		key.Type == tea.KeyRunes && string(key.Runes) == "q":
		m.stats.reply <- struct{}{}
		m.stats = nil
		m.refreshViewport()
		return m, nil
	case key.Type == tea.KeyCtrlS:
		m.stats.copy()
		return m, nil
	case key.Type == tea.KeyRunes && string(key.Runes) == "r":
		m.stats.cycle()
		return m, nil
	}
	return m, nil
}
