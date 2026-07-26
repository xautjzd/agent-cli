package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// A key is not a provider definition. Saving one for a built-in must not copy
// the preset into the providers map — that turned the vendor into a custom
// profile shadowing itself, listed twice and frozen at the endpoint it was
// copied from.
func TestSaveProviderKeyDoesNotCreateAProfile(t *testing.T) {
	dir := t.TempDir()
	if err := SaveProviderKey(ScopeProject, dir, "siliconflow", "", "sk-xyz"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(ProjectPath(dir))
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Providers["siliconflow"]; ok {
		t.Errorf("a saved key must not become a provider profile: %+v", cfg.Providers)
	}
	if cfg.APIKeys["siliconflow"] != "sk-xyz" {
		t.Errorf("key not stored under api_keys: %+v", cfg.APIKeys)
	}
}

// A name that *is* a profile keeps its definition and just gains the key.
func TestSaveProviderKeyUpdatesAnExistingProfile(t *testing.T) {
	dir := t.TempDir()
	seed := &Config{Providers: map[string]ProviderConfig{
		"work": {BaseURL: "https://llm.internal/v1", Model: "internal-v2"},
	}}
	if err := seed.saveTo(ProjectPath(dir)); err != nil {
		t.Fatal(err)
	}
	if err := SaveProviderKey(ScopeProject, dir, "work", "", "sk-work"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(ProjectPath(dir))
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	p := cfg.Providers["work"]
	if p.APIKey != "sk-work" || p.BaseURL != "https://llm.internal/v1" || p.Model != "internal-v2" {
		t.Errorf("profile lost its definition or the key: %+v", p)
	}
	if _, stray := cfg.APIKeys["work"]; stray {
		t.Error("a profile's key must live in the profile, not in api_keys")
	}
}

// The stored key is a backstop: it configures a preset that has nothing else
// set, while an environment variable still wins.
func TestStoredKeyResolvesForAPreset(t *testing.T) {
	isolateHome(t)
	if err := SaveProviderKey(ScopeGlobal, "", "deepseek", "", "sk-stored"); err != nil {
		t.Fatal(err)
	}
	if err := Set("provider", "deepseek"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "sk-stored" {
		t.Errorf("stored key not resolved: %q", cfg.APIKey)
	}
	if _, err := cfg.BuildProvider(); err != nil {
		t.Errorf("a preset with a stored key should build: %v", err)
	}
	// The preset still supplies the endpoint — the key did not freeze a copy.
	if cfg.BaseURL != "https://api.deepseek.com" {
		t.Errorf("preset endpoint lost: %q", cfg.BaseURL)
	}

	t.Setenv("DEEPSEEK_API_KEY", "sk-from-env")
	if cfg, err = Load(); err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "sk-from-env" {
		t.Errorf("environment must win over a stored key, got %q", cfg.APIKey)
	}
}

// A custom provider is defined once and then behaves like any other: it is
// stored under "providers", and its key can stay in the shell under a name
// derived from the provider's own, so nothing secret has to be written down.
func TestCustomProviderDefinitionAndEnvKey(t *testing.T) {
	isolateHome(t)
	def := ProviderConfig{BaseURL: "https://llm.internal/v1", Model: "internal-v2"}
	if err := SaveProviderProfile(ScopeGlobal, "", "my-gw", def); err != nil {
		t.Fatal(err)
	}
	if err := Set("provider", "my-gw"); err != nil {
		t.Fatal(err)
	}

	// No key anywhere: the error names the variable to export.
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.BuildProvider(); err == nil || !strings.Contains(err.Error(), "MY_GW_API_KEY") {
		t.Errorf("error should name the derived variable, got %v", err)
	}

	// Exporting it is enough — the definition needs no secret.
	t.Setenv("MY_GW_API_KEY", "sk-env")
	if cfg, err = Load(); err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "sk-env" || cfg.BaseURL != "https://llm.internal/v1" || cfg.Model != "internal-v2" {
		t.Fatalf("custom provider not resolved: %+v", cfg)
	}
	if _, err := cfg.BuildProvider(); err != nil {
		t.Errorf("custom provider should build: %v", err)
	}

	// An explicit env_key wins over the derived one.
	def.EnvKey = "OTHER_TOKEN"
	if err := SaveProviderProfile(ScopeGlobal, "", "my-gw", def); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OTHER_TOKEN", "sk-other")
	if cfg, err = Load(); err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "sk-other" {
		t.Errorf("env_key should take precedence: %q", cfg.APIKey)
	}

	// An anthropic-wire gateway defaults to bearer auth — the classic 401.
	if err := SaveProviderProfile(ScopeGlobal, "", "gw2", ProviderConfig{
		BaseURL: "https://gw2/anthropic", Format: "anthropic",
	}); err != nil {
		t.Fatal(err)
	}
	saved, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Providers["gw2"].Auth != "bearer" {
		t.Errorf("anthropic gateway should default to bearer: %+v", saved.Providers["gw2"])
	}

	// Removing it takes the definition with it.
	if err := RemoveProviderProfile(ScopeGlobal, "", "gw2"); err != nil {
		t.Fatal(err)
	}
	if saved, err = Load(); err != nil {
		t.Fatal(err)
	}
	if _, still := saved.Providers["gw2"]; still {
		t.Error("removed provider is still in the config")
	}
	if err := RemoveProviderProfile(ScopeGlobal, "", "gw2"); err == nil {
		t.Error("removing a provider that is not there should be an error")
	}
}

func TestEnvKeyName(t *testing.T) {
	cases := map[string]string{
		"my-gw": "MY_GW_API_KEY", "work": "WORK_API_KEY",
		"llm.internal": "LLM_INTERNAL_API_KEY", "gw2": "GW2_API_KEY", "": "",
	}
	for in, want := range cases {
		if got := EnvKeyName(in); got != want {
			t.Errorf("EnvKeyName(%q) = %q, want %q", in, got, want)
		}
	}
}
