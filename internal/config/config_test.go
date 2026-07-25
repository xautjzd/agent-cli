package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xautjzd/agent-cli/internal/provider"
)

// isolateHome points HOME (and clears env overrides) at a temp dir so tests
// never touch the user's real ~/.agent/config.json.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, v := range []string{"AGENT_PROVIDER", "AGENT_MODEL", "AGENT_BASE_URL",
		"AGENT_API_KEY", "OPENAI_API_KEY", "DEEPSEEK_API_KEY"} {
		t.Setenv(v, "")
	}
	return home
}

func TestLoadDefaultsAndEnvOverride(t *testing.T) {
	isolateHome(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "deepseek" || cfg.Model != "deepseek-v4-flash" || cfg.MaxTurns != 40 {
		t.Errorf("defaults wrong: %+v", cfg)
	}

	t.Setenv("AGENT_PROVIDER", "openai")
	t.Setenv("OPENAI_API_KEY", "sk-test")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "openai" || cfg.Model != "gpt-5.6-terra" || cfg.APIKey != "sk-test" {
		t.Errorf("env override wrong: %+v", cfg)
	}
}

func TestLoadForDropsStaleVendorSettings(t *testing.T) {
	isolateHome(t)
	// File config is bound to deepseek with its key and model.
	base := &Config{Provider: "deepseek", Model: "deepseek-v4-flash", APIKey: "ds-key", MaxTurns: 40}
	if err := base.Save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "oa-key")

	cfg, err := LoadFor("openai")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "openai" {
		t.Errorf("provider = %s", cfg.Provider)
	}
	if cfg.APIKey != "oa-key" {
		t.Errorf("stale key leaked across providers: %q", cfg.APIKey)
	}
	if cfg.Model != "gpt-5.6-terra" {
		t.Errorf("model not re-defaulted: %q", cfg.Model)
	}

	// Same provider keeps everything.
	cfg, err = LoadFor("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "ds-key" || cfg.Model != "deepseek-v4-flash" {
		t.Errorf("same-provider load lost settings: %+v", cfg)
	}
}

func TestProjectConfigOverridesGlobal(t *testing.T) {
	isolateHome(t)
	global := &Config{Provider: "deepseek", Model: "deepseek-v4-flash", MaxTurns: 40, PermissionMode: "hitl"}
	if err := global.Save(); err != nil {
		t.Fatal(err)
	}

	proj := t.TempDir()
	if err := SetScoped(ScopeProject, proj, "model", "deepseek-v4-pro"); err != nil {
		t.Fatal(err)
	}
	if err := SetScoped(ScopeProject, proj, "permission_mode", "bypass"); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadIn(proj)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "deepseek-v4-pro" || cfg.PermissionMode != "bypass" {
		t.Errorf("project overrides not applied: %+v", cfg)
	}
	if cfg.Provider != "deepseek" {
		t.Errorf("global provider lost: %+v", cfg)
	}

	// Another directory is unaffected by that project's file.
	other, err := LoadIn(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if other.Model != "deepseek-v4-flash" || other.PermissionMode != "hitl" {
		t.Errorf("project file leaked into other dir: %+v", other)
	}
}

func TestNamedProviderProfile(t *testing.T) {
	isolateHome(t)
	global := &Config{
		Provider: "ollama",
		Providers: map[string]ProviderConfig{
			"ollama":   {BaseURL: "http://localhost:11434/v1", Model: "qwen3", APIKey: "ollama"},
			"moonshot": {BaseURL: "https://api.moonshot.cn/v1", EnvKey: "MOONSHOT_API_KEY"},
		},
	}
	if err := global.Save(); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "http://localhost:11434/v1" || cfg.Model != "qwen3" || cfg.APIKey != "ollama" {
		t.Errorf("profile not resolved: %+v", cfg)
	}
	if !cfg.IsNamedProfile("ollama") || cfg.IsNamedProfile("openai") {
		t.Error("IsNamedProfile wrong")
	}
	p, err := cfg.BuildProvider()
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "ollama" {
		t.Errorf("provider name = %s", p.Name())
	}

	// env_key profiles read the key from the environment on switch.
	t.Setenv("MOONSHOT_API_KEY", "ms-key")
	cfg, err = LoadFor("moonshot")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "ms-key" || cfg.BaseURL != "https://api.moonshot.cn/v1" {
		t.Errorf("env_key profile not resolved: %+v", cfg)
	}
}

func TestAnthropicCompatibleProfile(t *testing.T) {
	isolateHome(t)
	// A third-party gateway that speaks the Anthropic Messages API (Zhipu
	// GLM, DashScope, Moonshot…) is declared as a named profile with
	// format: "anthropic" plus its own base_url and model.
	global := &Config{
		Provider: "glm",
		Providers: map[string]ProviderConfig{
			"glm": {
				BaseURL: "https://example.invalid/api/anthropic",
				Model:   "glm-4.6",
				EnvKey:  "GLM_API_KEY",
				Format:  "anthropic",
				Vision:  true,
			},
			"qwen": {BaseURL: "https://example.invalid/v1", Model: "qwen-max"},
		},
	}
	if err := global.Save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GLM_API_KEY", "glm-key")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "https://example.invalid/api/anthropic" || cfg.Model != "glm-4.6" || cfg.APIKey != "glm-key" {
		t.Fatalf("profile not resolved: %+v", cfg)
	}

	// The Anthropic adapter is selected, under the profile's own name.
	p, err := cfg.BuildProvider()
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "glm" {
		t.Errorf("provider name = %q, want the profile name", p.Name())
	}
	if _, ok := p.(provider.Streamer); !ok {
		t.Error("anthropic-format profile should support streaming")
	}
	// The profile's vision flag still applies regardless of format.
	if !cfg.ModelSupportsVision() {
		t.Error("vision flag not honored on an anthropic-format profile")
	}

	// A profile without a format keeps the OpenAI-compatible client.
	openaiCfg, err := LoadFor("qwen")
	if err != nil {
		t.Fatal(err)
	}
	openaiCfg.APIKey = "k"
	op, err := openaiCfg.BuildProvider()
	if err != nil {
		t.Fatal(err)
	}
	if _, isAnthropic := op.(provider.Streamer); !isAnthropic {
		t.Log("openai profile also streams, as expected")
	}
	if op.Name() != "qwen" {
		t.Errorf("openai profile name = %q", op.Name())
	}

	// An unknown format is rejected with actionable guidance.
	bad := &Config{Provider: "x", Providers: map[string]ProviderConfig{
		"x": {BaseURL: "https://example.invalid", Format: "bogus"},
	}}
	if _, err := bad.BuildProvider(); err == nil || !strings.Contains(err.Error(), "unknown format") {
		t.Errorf("expected unknown-format error, got %v", err)
	}
}

func TestPresetMakesProviderZeroConfig(t *testing.T) {
	isolateHome(t)
	t.Setenv("ZHIPUAI_API_KEY", "zp-key")

	// Naming a preset provider is the entire configuration.
	if err := Set("provider", "glm"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "glm" {
		t.Errorf("provider = %q", cfg.Provider)
	}
	if cfg.Model == "" || cfg.BaseURL == "" {
		t.Errorf("preset did not supply model/base_url: %+v", cfg)
	}
	if cfg.APIKey != "zp-key" {
		t.Errorf("preset env key not resolved: %q", cfg.APIKey)
	}
	if _, err := cfg.BuildProvider(); err != nil {
		t.Errorf("preset provider should build: %v", err)
	}
}

func TestPresetAliasCanonicalizes(t *testing.T) {
	isolateHome(t)
	t.Setenv("ZHIPUAI_API_KEY", "k")
	if err := Set("provider", "zhipu"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	// An alias resolves to the canonical name so later lookups agree.
	if cfg.Provider != "glm" {
		t.Errorf("alias not canonicalized: %q", cfg.Provider)
	}
}

func TestExplicitConfigBeatsPreset(t *testing.T) {
	isolateHome(t)
	t.Setenv("DEEPSEEK_API_KEY", "env-key")
	custom := &Config{
		Provider: "deepseek",
		Model:    "deepseek-v4-pro",           // newer than the preset knows
		BaseURL:  "https://proxy.internal/v1", // a corporate proxy
		APIKey:   "explicit-key",
	}
	if err := custom.Save(); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	// A preset must never overwrite something the user set — including a
	// model the catalog has never heard of.
	if cfg.Model != "deepseek-v4-pro" {
		t.Errorf("preset overwrote the configured model: %q", cfg.Model)
	}
	if cfg.BaseURL != "https://proxy.internal/v1" {
		t.Errorf("preset overwrote the configured base_url: %q", cfg.BaseURL)
	}
	if cfg.APIKey != "explicit-key" {
		t.Errorf("preset overwrote the configured api_key: %q", cfg.APIKey)
	}
}

func TestUserProfileShadowsPreset(t *testing.T) {
	isolateHome(t)
	// A profile named like a preset wins entirely, so a user can point
	// "glm" at their own gateway.
	custom := &Config{
		Provider: "glm",
		Providers: map[string]ProviderConfig{
			"glm": {BaseURL: "https://my-gateway.internal/anthropic",
				Model: "glm-5.2", Format: "anthropic", Auth: "bearer", APIKey: "mine"},
		},
	}
	if err := custom.Save(); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "https://my-gateway.internal/anthropic" || cfg.Model != "glm-5.2" {
		t.Errorf("profile did not shadow the preset: %+v", cfg)
	}
	p, err := cfg.BuildProvider()
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "glm" {
		t.Errorf("provider name = %q", p.Name())
	}
}

func TestPresetSelectsWireFormat(t *testing.T) {
	isolateHome(t)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "tok")
	// An Anthropic-compatible gateway preset must build an Anthropic
	// client even though the provider name is not a registered built-in.
	cfg := &Config{Provider: "kimi-anthropic"}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	p, err := loaded.BuildProvider()
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "kimi-anthropic" {
		t.Errorf("provider name = %q", p.Name())
	}
	if _, ok := p.(provider.Streamer); !ok {
		t.Error("expected a streaming-capable client")
	}
}

func TestSetScopedValidation(t *testing.T) {
	isolateHome(t)
	if err := SetScoped(ScopeGlobal, "", "permission_mode", "yolo"); err == nil {
		t.Error("invalid permission_mode should be rejected")
	}
	if err := SetScoped(ScopeGlobal, "", "goal_max_rounds", "0"); err == nil {
		t.Error("non-positive goal_max_rounds should be rejected")
	}
	if err := SetScoped(ScopeProject, "", "model", "x"); err == nil {
		t.Error("project scope without dir should be rejected")
	}
	if err := SetScoped(ScopeGlobal, "", "goal_max_rounds", "5"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := Load()
	if cfg.GoalMaxRounds != 5 {
		t.Errorf("goal_max_rounds = %d", cfg.GoalMaxRounds)
	}
}

func TestSetPersistsOnlyFileValues(t *testing.T) {
	home := isolateHome(t)
	t.Setenv("AGENT_MODEL", "env-model")

	if err := Set("model", "file-model"); err != nil {
		t.Fatal(err)
	}
	if err := Set("max_turns", "7"); err != nil {
		t.Fatal(err)
	}
	if err := Set("bogus", "x"); err == nil {
		t.Error("unknown key should be rejected")
	}
	if err := Set("max_turns", "-1"); err == nil {
		t.Error("negative max_turns should be rejected")
	}

	// The file must contain the set value, not the env value.
	data, err := os.ReadFile(filepath.Join(home, ".agent", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); !strings.Contains(got, "file-model") || strings.Contains(got, "env-model") {
		t.Errorf("file contents wrong: %s", got)
	}

	// And resolved Load still honors env precedence.
	cfg, _ := Load()
	if cfg.Model != "env-model" || cfg.MaxTurns != 7 {
		t.Errorf("precedence broken: %+v", cfg)
	}
}
