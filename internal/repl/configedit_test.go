package repl

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/xautjzd/agent-cli/internal/config"
	"github.com/xautjzd/agent-cli/internal/permission"
)

// isolateEnv keeps config writes away from the user's real home and shell.
func isolateEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	for _, v := range []string{"AGENT_PROVIDER", "AGENT_MODEL", "AGENT_BASE_URL",
		"AGENT_API_KEY", "OPENAI_API_KEY", "DEEPSEEK_API_KEY"} {
		t.Setenv(v, "")
	}
}

func TestConfigSetSessionOnly(t *testing.T) {
	isolateEnv(t)
	r, _, out := newTestRepl(t, "")

	if err := r.dispatch(context.Background(), "/config set model new-model session"); err != nil {
		t.Fatal(err)
	}
	if r.Cfg.Model != "new-model" || r.Agent.Model != "new-model" {
		t.Error("session-only set did not apply live")
	}
	if !strings.Contains(out.String(), "session only") {
		t.Errorf("missing session-only note: %s", out.String())
	}
	// Nothing persisted.
	if path, _ := config.Path(); path != "" {
		if _, err := os.Stat(path); err == nil {
			t.Error("session-only set must not write the global file")
		}
	}
}

func TestConfigSetPersistsScopes(t *testing.T) {
	isolateEnv(t)
	r, _, _ := newTestRepl(t, "")

	if err := r.dispatch(context.Background(), "/config set max_turns 7 global"); err != nil {
		t.Fatal(err)
	}
	if r.Agent.MaxTurns != 7 {
		t.Error("max_turns not applied live")
	}
	cfg, err := config.LoadIn("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxTurns != 7 {
		t.Errorf("global persist failed: %+v", cfg)
	}

	if err := r.dispatch(context.Background(), "/config set permission_mode bypass project"); err != nil {
		t.Fatal(err)
	}
	if r.permMode() != permission.ModeBypass {
		t.Error("permission_mode not applied live")
	}
	proj, err := config.LoadIn(r.WorkDir)
	if err != nil {
		t.Fatal(err)
	}
	if proj.PermissionMode != "bypass" {
		t.Errorf("project persist failed: %+v", proj)
	}
	// Invalid values are rejected before persisting.
	if err := r.dispatch(context.Background(), "/config set permission_mode yolo session"); err == nil {
		t.Error("invalid permission_mode should error")
	}
}

func TestConfigEditNumberedFlow(t *testing.T) {
	isolateEnv(t)
	// Select setting #2 (model), enter a value, keep it session-only.
	r, _, out := newTestRepl(t, "2\nedited-model\ns\n")

	if err := r.dispatch(context.Background(), "/config"); err != nil {
		t.Fatal(err)
	}
	if r.Cfg.Model != "edited-model" {
		t.Errorf("edit flow did not apply model, got %q", r.Cfg.Model)
	}
	if !strings.Contains(out.String(), "model") || !strings.Contains(out.String(), "session only") {
		t.Errorf("edit output wrong:\n%s", out.String())
	}
}

func TestConfigEditCancel(t *testing.T) {
	isolateEnv(t)
	r, _, _ := newTestRepl(t, "\n") // Enter at the selection prompt cancels
	if err := r.dispatch(context.Background(), "/config"); err != nil {
		t.Fatal(err)
	}
	if r.Cfg.Model != "m1" {
		t.Error("cancel must not change anything")
	}
}

func TestBareConfigOpensEditor(t *testing.T) {
	isolateEnv(t)
	// Bare /config now IS the editor (view + edit merged) — there is no
	// separate read-only dump. Piped: same numbered flow as /config edit.
	r, _, _ := newTestRepl(t, "2\nfrom-bare-config\ns\n")
	if err := r.dispatch(context.Background(), "/config"); err != nil {
		t.Fatal(err)
	}
	if r.Cfg.Model != "from-bare-config" {
		t.Errorf("bare /config did not open the editor, model=%q", r.Cfg.Model)
	}

	// An unknown subcommand is a usage error.
	r2, _, _ := newTestRepl(t, "")
	if err := r2.dispatch(context.Background(), "/config bogus"); err == nil {
		t.Error("unknown /config subcommand should error")
	}
}
