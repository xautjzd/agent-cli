package repl

import (
	"context"
	"strings"
	"testing"

	"github.com/xautjzd/agent-cli/internal/theme"
)

func TestThemeCommandSwitches(t *testing.T) {
	// SetScoped writes to the global config; redirect the agent home so the
	// test never touches the user's real config.
	t.Setenv("AGENT_HOME", t.TempDir())
	t.Cleanup(func() { theme.Set("dark") })

	r, _, out := newTestRepl(t, "")
	if err := r.dispatch(context.Background(), "/theme dracula"); err != nil {
		t.Fatal(err)
	}
	if theme.Current().Name != "dracula" {
		t.Errorf("active theme = %q, want dracula", theme.Current().Name)
	}
	if r.Cfg.Theme != "dracula" {
		t.Errorf("cfg theme = %q, want dracula", r.Cfg.Theme)
	}
	if !strings.Contains(out.String(), "theme set to dracula") {
		t.Errorf("missing confirmation: %q", out.String())
	}
}

func TestThemeCommandUnknown(t *testing.T) {
	t.Cleanup(func() { theme.Set("dark") })
	r, _, _ := newTestRepl(t, "")
	if err := r.cmdTheme(context.Background(), "bogus"); err == nil {
		t.Fatal("unknown theme should error")
	}
	if theme.Current().Name == "bogus" {
		t.Fatal("unknown theme was applied")
	}
}

// TestThemeHasNoInlineCompletion documents that /theme deliberately offers no
// inline value completion — theme selection goes through its own full-screen
// picker, which previews each theme live as the highlight moves (the inline
// popup only applies on submit, so it cannot preview).
func TestThemeHasNoInlineCompletion(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	value := "/theme dr"
	if cands := r.argumentCandidates(value, len("/theme "), len(value)); cands != nil {
		t.Errorf("theme should have no inline candidates, got %+v", cands)
	}
	if r.commandCompletesArgs("theme") {
		t.Error("commandCompletesArgs(theme) = true; theme must route to its live-preview picker")
	}
}

// switchTheme, when a scrollback is active, clears it and replays the banner so
// the whole transcript is re-colored.
func TestSwitchThemeReRendersScrollback(t *testing.T) {
	t.Cleanup(func() { theme.Set("dark") })
	r, _, _ := newTestRepl(t, "")

	sb := &scrollback{}
	sb.Write([]byte("STALE OLD OUTPUT\n"))
	r.sb = sb
	r.Out = sb

	r.switchTheme("nord")

	got := sb.String()
	if strings.Contains(got, "STALE OLD OUTPUT") {
		t.Errorf("scrollback not reset before replay: %q", got)
	}
	if !strings.Contains(got, "agent-cli") {
		t.Errorf("banner not reprinted after switch: %q", got)
	}
}

func TestRedrawTranscriptKeepsOneCurrentBanner(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	sb := &scrollback{}
	r.sb = sb
	r.Out = sb

	// Simulate successive provider/model/effort/context changes. Every redraw
	// must replace the prior header rather than append another one.
	r.Cfg.Provider = "anthropic"
	r.Cfg.Model = "claude-old"
	r.Cfg.Thinking = "low"
	r.Cfg.ContextLimit = 128000
	r.redrawTranscript()

	r.Cfg.Provider = "openai"
	r.Cfg.Model = "gpt-current"
	r.Cfg.Thinking = "high"
	r.Cfg.ContextLimit = 200000
	r.redrawTranscript()
	r.redrawTranscript() // theme previews may redraw the same state repeatedly

	got := stripANSI(sb.String())
	if count := strings.Count(got, "✻ agent-cli"); count != 1 {
		t.Fatalf("banner count = %d, want exactly 1:\n%s", count, got)
	}
	for _, want := range []string{"provider openai", "model    gpt-current", "effort   high", "context  200K tokens"} {
		if !strings.Contains(got, want) {
			t.Errorf("updated banner missing %q:\n%s", want, got)
		}
	}
	for _, stale := range []string{"anthropic", "claude-old", "128K tokens"} {
		if strings.Contains(got, stale) {
			t.Errorf("updated banner retains stale value %q:\n%s", stale, got)
		}
	}
}
