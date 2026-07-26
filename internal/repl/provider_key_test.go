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

// TestProviderSwitchReconcilesBaseURL verifies a stale top-level base_url from a
// previous provider is cleared when switching to a preset that owns its own
// endpoint, so the preset's base_url resolves on the next load instead of the
// old one shadowing it.
func TestProviderSwitchReconcilesBaseURL(t *testing.T) {
	isolateEnv(t)
	// Seed a stale OpenAI-style base_url as if a prior provider had set it.
	if err := config.SetScoped(config.ScopeGlobal, "", "base_url", "https://api.deepseek.com"); err != nil {
		t.Fatal(err)
	}
	// deepseek-anthropic is a bearer-token Anthropic-wire preset, so it still
	// needs a key: feed one, then skip saving.
	r, _, _ := newTestRepl(t, "sk-test-key\nn\n")

	if err := r.dispatch(context.Background(), "/provider deepseek-anthropic"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadIn("")
	if err != nil {
		t.Fatal(err)
	}
	// The legacy name normalizes to the vendor plus its anthropic wire.
	if cfg.Provider != "deepseek" || cfg.Format != "anthropic" {
		t.Fatalf("provider not persisted: %q format=%q", cfg.Provider, cfg.Format)
	}
	if cfg.BaseURL != "https://api.deepseek.com/anthropic" {
		t.Errorf("base_url did not follow the new provider: got %q", cfg.BaseURL)
	}
}

// TestProviderBearerAnthropicPromptsForKey guards the reported regression:
// switching to a third-party Anthropic-wire preset (bearer auth) with no
// resolvable credential must prompt for the key rather than silently switching
// and persisting a keyless provider that then fails at request time.
func TestProviderBearerAnthropicPromptsForKey(t *testing.T) {
	isolateEnv(t)
	r, _, out := newTestRepl(t, "sk-ds-key\ny\n") // key, then save

	if err := r.dispatch(context.Background(), "/provider deepseek-anthropic"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Enter API key for deepseek-anthropic") {
		t.Errorf("expected an API-key prompt, got:\n%s", out.String())
	}
	if r.Cfg.Provider != "deepseek" || r.Cfg.Format != "anthropic" || r.Cfg.APIKey != "sk-ds-key" {
		t.Errorf("config not updated: provider=%s format=%s key=%q", r.Cfg.Provider, r.Cfg.Format, r.Cfg.APIKey)
	}
	// The key is stored on its own — not as a profile that would take over
	// the preset's name — and the wire is carried by the top-level format.
	cfg, err := config.LoadIn("")
	if err != nil {
		t.Fatal(err)
	}
	if _, isProfile := cfg.Providers["deepseek"]; isProfile {
		t.Errorf("saving a key must not create a profile: %+v", cfg.Providers)
	}
	if cfg.APIKeys["deepseek"] != "sk-ds-key" {
		t.Errorf("key not stored: %+v", cfg.APIKeys)
	}
	if cfg.Format != "anthropic" || cfg.BaseURL != "https://api.deepseek.com/anthropic" {
		t.Errorf("anthropic wire not persisted: format=%q base=%q", cfg.Format, cfg.BaseURL)
	}
}

// Switching wires with the flag must be equivalent to the legacy
// "<vendor>-anthropic" name, and switching back must clear the wire rather
// than leaving the previous endpoint pinned.
func TestProviderWireFlagSwitchesEndpoint(t *testing.T) {
	isolateEnv(t)
	t.Setenv("DEEPSEEK_API_KEY", "sk-ds")
	r, _, _ := newTestRepl(t, "")

	if err := r.dispatch(context.Background(), "/provider deepseek --anthropic"); err != nil {
		t.Fatal(err)
	}
	if r.Cfg.Format != "anthropic" || r.Cfg.BaseURL != "https://api.deepseek.com/anthropic" {
		t.Fatalf("--anthropic did not select the anthropic endpoint: format=%q base=%q", r.Cfg.Format, r.Cfg.BaseURL)
	}
	if err := r.dispatch(context.Background(), "/provider deepseek"); err != nil {
		t.Fatal(err)
	}
	if r.Cfg.Format != "" || r.Cfg.BaseURL != "https://api.deepseek.com" {
		t.Errorf("switching back kept the anthropic wire: format=%q base=%q", r.Cfg.Format, r.Cfg.BaseURL)
	}
	cfg, err := config.LoadIn("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Format != "" {
		t.Errorf("stale format persisted: %q", cfg.Format)
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
	// The key is persisted for that provider without duplicating the preset
	// into the providers map.
	cfg, err := config.LoadIn("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKeys["siliconflow"] != "sk-persist" {
		t.Errorf("key not persisted: %+v", cfg.APIKeys)
	}
	if _, isProfile := cfg.Providers["siliconflow"]; isProfile {
		t.Error("a saved key must not turn a built-in into a profile")
	}
	// It resolves on the next load, so the session reconnects.
	if cfg.APIKey != "sk-persist" {
		t.Errorf("stored key not resolved on load: %q", cfg.APIKey)
	}
}

// The config file must say what the session is running. Leaving the model out
// because "the preset supplies it anyway" made the file disagree with the
// banner, and a /model switch that vanished on restart is a setting the user
// has to make twice.
func TestProviderAndModelSwitchesArePersisted(t *testing.T) {
	isolateEnv(t)
	t.Setenv("DEEPSEEK_API_KEY", "sk-ds")
	r, _, _ := newTestRepl(t, "")

	// Switching provider without naming a model still records the default.
	if err := r.dispatch(context.Background(), "/provider deepseek"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadIn("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "deepseek-v4-flash" {
		t.Errorf("preset default model not persisted: %q", cfg.Model)
	}

	// /model persists on its own.
	if err := r.dispatch(context.Background(), "/model deepseek-v4-pro"); err != nil {
		t.Fatal(err)
	}
	if cfg, err = config.LoadIn(""); err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "deepseek-v4-pro" {
		t.Errorf("/model not persisted: %q", cfg.Model)
	}

	// Switching provider replaces it rather than carrying a foreign model over.
	t.Setenv("MOONSHOT_API_KEY", "sk-kimi")
	if err := r.dispatch(context.Background(), "/provider kimi"); err != nil {
		t.Fatal(err)
	}
	if cfg, err = config.LoadIn(""); err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "kimi-k3" {
		t.Errorf("model not re-pointed at the new provider: %q", cfg.Model)
	}
}

// A custom provider can be defined from inside the session and lands in the
// config's providers map, where it lists ahead of the built-ins.
func TestProviderAddDefinesACustomProvider(t *testing.T) {
	isolateEnv(t)
	r, _, out := newTestRepl(t, "")

	err := r.dispatch(context.Background(), "/provider custom my-gw --base-url https://llm.internal/v1 --model internal-v2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "MY_GW_API_KEY") {
		t.Errorf("the derived credential variable should be named:\n%s", out.String())
	}
	cfg, err := config.LoadIn("")
	if err != nil {
		t.Fatal(err)
	}
	p, ok := cfg.Providers["my-gw"]
	if !ok || p.BaseURL != "https://llm.internal/v1" || p.Model != "internal-v2" {
		t.Fatalf("custom provider not persisted: %+v ok=%v", p, ok)
	}
	if p.APIKey != "" {
		t.Error("no key was given; none should have been invented")
	}

	// It is usable straight away once the key is in the environment.
	t.Setenv("MY_GW_API_KEY", "sk-env")
	if err := r.dispatch(context.Background(), "/provider my-gw"); err != nil {
		t.Fatal(err)
	}
	if r.Cfg.Provider != "my-gw" || r.Cfg.BaseURL != "https://llm.internal/v1" {
		t.Errorf("switch to the custom provider failed: %+v", r.Cfg)
	}

	// An anthropic-wire definition records the wire and its bearer auth.
	if err := r.dispatch(context.Background(), "/provider custom gw2 --base-url https://gw2/anthropic --model m --anthropic --api-key sk"); err != nil {
		t.Fatal(err)
	}
	if cfg, err = config.LoadIn(""); err != nil {
		t.Fatal(err)
	}
	if g := cfg.Providers["gw2"]; g.Format != "anthropic" || g.Auth != "bearer" || g.APIKey != "sk" {
		t.Errorf("anthropic definition wrong: %+v", g)
	}

	// Removing it is symmetric.
	if err := r.dispatch(context.Background(), "/provider remove gw2"); err != nil {
		t.Fatal(err)
	}
	if cfg, err = config.LoadIn(""); err != nil {
		t.Fatal(err)
	}
	if _, still := cfg.Providers["gw2"]; still {
		t.Error("removed provider still in config")
	}

	// A definition without an endpoint is rejected rather than half-saved.
	if err := r.dispatch(context.Background(), "/provider custom broken --model m"); err == nil {
		t.Error("a provider without --base-url must be rejected")
	}
	if cfg, err = config.LoadIn(""); err != nil {
		t.Fatal(err)
	}
	if _, saved := cfg.Providers["broken"]; saved {
		t.Error("a rejected definition must not be persisted")
	}
}

// A blank answer to a required field is a slip, not a decision to abandon the
// whole definition: the prompt says what is missing and asks again. Only Esc
// (or end of input) cancels.
func TestProviderAddRepromptsForRequiredFields(t *testing.T) {
	isolateEnv(t)
	// name, two empty base URLs, then a real one; blank style/model/key.
	r, _, out := newTestRepl(t, "my-gw\n\n\nhttps://llm.internal/v1\n\n\n\n")

	if err := r.dispatch(context.Background(), "/provider custom"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Count(got, "A base URL is required") != 2 {
		t.Errorf("each blank answer should be answered with the requirement:\n%s", got)
	}
	cfg, err := config.LoadIn("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Providers["my-gw"].BaseURL != "https://llm.internal/v1" {
		t.Errorf("definition not completed after the re-prompts: %+v", cfg.Providers)
	}

	// Running out of input cancels rather than looping forever.
	r2, _, _ := newTestRepl(t, "")
	if err := r2.dispatch(context.Background(), "/provider custom"); err == nil {
		t.Error("cancelling should report an error, not save a partial provider")
	}
}
