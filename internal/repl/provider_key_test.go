package repl

import (
	"context"
	"strings"
	"testing"

	"github.com/xautjzd/agent-cli/internal/config"
)

// TestProviderPromptsForKeyInsteadOfError verifies that switching to a provider
// with no configured credential prompts for the API key (and retries) instead
// of failing.
func TestProviderPromptsForKeyInsteadOfError(t *testing.T) {
	isolateEnv(t)
	// stdin feeds the key, then "n" to skip saving.
	r, _, out := newTestRepl(t, "sk-test-key\nn\n")

	if err := r.dispatch(context.Background(), "/provider siliconflow"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "Enter API key for siliconflow") {
		t.Errorf("expected an API-key prompt, got:\n%s", got)
	}
	if !strings.Contains(got, "Switched to provider=siliconflow") {
		t.Errorf("provider should switch after the key is entered:\n%s", got)
	}
	if r.Cfg.Provider != "siliconflow" || r.Cfg.APIKey != "sk-test-key" {
		t.Errorf("config not updated: provider=%s key=%q", r.Cfg.Provider, r.Cfg.APIKey)
	}
}

// TestProviderSwitchPersistsToGlobal verifies a successful switch is written to
// the global config so it survives a restart, matching /effort's behavior.
func TestProviderSwitchPersistsToGlobal(t *testing.T) {
	isolateEnv(t)
	r, _, out := newTestRepl(t, "sk-test-key\nn\n")

	if err := r.dispatch(context.Background(), "/provider siliconflow glm-not-real"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "saved") {
		t.Errorf("switch should report it was saved:\n%s", out.String())
	}
	cfg, err := config.LoadIn("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "siliconflow" {
		t.Errorf("provider not persisted: got %q", cfg.Provider)
	}
	if cfg.Model != "glm-not-real" {
		t.Errorf("explicit model not persisted: got %q", cfg.Model)
	}
}

// TestProviderKeyPromptCancel confirms an empty key cancels the switch.
func TestProviderKeyPromptCancel(t *testing.T) {
	isolateEnv(t)
	r, _, out := newTestRepl(t, "\n") // empty key = cancel

	if err := r.dispatch(context.Background(), "/provider siliconflow"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "cancelled") {
		t.Errorf("expected a cancellation message:\n%s", out.String())
	}
	if r.Cfg.Provider == "siliconflow" {
		t.Error("provider must not switch when the key prompt is cancelled")
	}
}

// TestProviderKeySavedWhenConfirmed checks the key is persisted on confirmation.
func TestProviderKeySavedWhenConfirmed(t *testing.T) {
	isolateEnv(t)
	r, _, _ := newTestRepl(t, "sk-persist\ny\n") // key, then save

	if err := r.dispatch(context.Background(), "/provider siliconflow"); err != nil {
		t.Fatal(err)
	}
	// The saved global config now carries a siliconflow profile with the key.
	cfg, err := config.LoadIn("")
	if err != nil {
		t.Fatal(err)
	}
	p, ok := cfg.Providers["siliconflow"]
	if !ok || p.APIKey != "sk-persist" {
		t.Errorf("key not persisted as a profile: %+v ok=%v", p, ok)
	}
}
