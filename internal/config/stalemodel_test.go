package config

import "testing"

// mirrors the user's config: provider=deepseek but a stale top-level
// model=glm-5.2 (which belongs to the glm profile).
func TestStaleModelReplacedByProfileModel(t *testing.T) {
	cfg := &Config{
		Provider: "deepseek",
		Model:    "glm-5.2",
		Providers: map[string]ProviderConfig{
			"deepseek": {BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-pro", Format: "openai"},
			"glm":      {BaseURL: "https://x/anthropic", Model: "glm-5.2", Format: "anthropic"},
		},
	}
	resolveProfile(cfg)
	if cfg.Model != "deepseek-v4-pro" {
		t.Errorf("stale glm-5.2 should be replaced by the deepseek profile model, got %q", cfg.Model)
	}
	if cfg.BaseURL != "https://api.deepseek.com" {
		t.Errorf("base_url = %q", cfg.BaseURL)
	}
}

// A model that legitimately belongs to the active provider is kept.
func TestOwnModelKept(t *testing.T) {
	cfg := &Config{
		Provider:  "deepseek",
		Model:     "deepseek-v4-flash",
		Providers: map[string]ProviderConfig{"deepseek": {BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-pro"}},
	}
	resolveProfile(cfg)
	if cfg.Model != "deepseek-v4-flash" {
		t.Errorf("an explicit valid model must be kept, got %q", cfg.Model)
	}
}

// A catalog-only provider (no profile) with a stale foreign model falls back to
// the provider default.
func TestStaleModelCatalogFallback(t *testing.T) {
	cfg := &Config{Provider: "deepseek", Model: "glm-5.2"}
	applyPreset(cfg)
	if cfg.Model == "glm-5.2" {
		t.Errorf("stale foreign model should be dropped for a catalog provider, got %q", cfg.Model)
	}
}
