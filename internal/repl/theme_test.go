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

func TestThemeArgumentCandidates(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	value := "/theme dr"
	cands := r.argumentCandidates(value, len("/theme "), len(value))
	var found bool
	for _, c := range cands {
		if c.text == "dracula" {
			found = true
		}
	}
	if !found {
		t.Errorf("dracula not offered for %q: %+v", value, cands)
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
