package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Credentials resolve per engine, so the same empty config must yield nothing
// for one provider and the right variable for another.
func TestWebSearchCredentialsFromEnv(t *testing.T) {
	t.Setenv("BRAVE_API_KEY", "brave-key")
	t.Setenv("TAVILY_API_KEY", "tavily-key")
	t.Setenv("GOOGLE_API_KEY", "google-key")
	t.Setenv("GOOGLE_SEARCH_ENGINE_ID", "cx-1")

	for _, tc := range []struct{ provider, wantKey, wantEngine string }{
		{"duckduckgo", "", "cx-1"}, // engine ID is provider-agnostic; unused here
		{"brave", "brave-key", "cx-1"},
		{"tavily", "tavily-key", "cx-1"},
		{"google", "google-key", "cx-1"},
		{"google.com", "google-key", "cx-1"}, // aliases resolve too
	} {
		cfg := &Config{WebSearch: WebSearchConfig{Provider: tc.provider}}
		creds := cfg.WebSearchCredentials()
		if creds.APIKey != tc.wantKey {
			t.Errorf("%s key = %q, want %q", tc.provider, creds.APIKey, tc.wantKey)
		}
		if creds.EngineID != tc.wantEngine {
			t.Errorf("%s engine id = %q, want %q", tc.provider, creds.EngineID, tc.wantEngine)
		}
	}
}

// GOOGLE_CSE_ID is the name the Google docs use in places, so it is accepted
// as a fallback.
func TestWebSearchEngineIDFallbacks(t *testing.T) {
	t.Setenv("GOOGLE_SEARCH_ENGINE_ID", "")
	t.Setenv("GOOGLE_CSE_ID", "cx-fallback")
	cfg := &Config{WebSearch: WebSearchConfig{Provider: "google"}}
	if got := cfg.WebSearchEngineID(); got != "cx-fallback" {
		t.Errorf("engine id = %q, want the GOOGLE_CSE_ID fallback", got)
	}

	// An explicit config value wins over the environment.
	cfg.WebSearch.EngineID = "cx-explicit"
	if got := cfg.WebSearchEngineID(); got != "cx-explicit" {
		t.Errorf("engine id = %q, want the configured value to win", got)
	}
}

// The API key follows the same precedence as everywhere else: explicit value,
// then the configured env var, then the vendor's standard one.
func TestWebSearchKeyPrecedence(t *testing.T) {
	t.Setenv("GOOGLE_API_KEY", "from-standard-var")
	t.Setenv("MY_GOOGLE_KEY", "from-custom-var")

	cfg := &Config{WebSearch: WebSearchConfig{Provider: "google"}}
	if got := cfg.WebSearchKey(); got != "from-standard-var" {
		t.Errorf("key = %q, want the standard variable", got)
	}

	cfg.WebSearch.EnvKey = "MY_GOOGLE_KEY"
	if got := cfg.WebSearchKey(); got != "from-custom-var" {
		t.Errorf("key = %q, want the configured env_key to win", got)
	}

	cfg.WebSearch.APIKey = "inline"
	if got := cfg.WebSearchKey(); got != "inline" {
		t.Errorf("key = %q, want the inline key to win", got)
	}
}

// engine_id has to survive the config layering, or a project layer that only
// switches the provider would silently drop the global engine ID. Going
// through a real file also pins the JSON tag.
func TestWebSearchEngineIDMerges(t *testing.T) {
	write := func(name, body string) string {
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	cfg := &Config{WebSearch: WebSearchConfig{Provider: "duckduckgo", EngineID: "cx-global"}}
	// A layer that names only the provider must leave the engine ID alone.
	if err := mergeFile(cfg, write("a.json", `{"web_search":{"provider":"google"}}`)); err != nil {
		t.Fatal(err)
	}
	if cfg.WebSearch.Provider != "google" {
		t.Errorf("provider = %q, want the layer to win", cfg.WebSearch.Provider)
	}
	if cfg.WebSearch.EngineID != "cx-global" {
		t.Errorf("engine id = %q, want the global value kept", cfg.WebSearch.EngineID)
	}

	// A layer that does name it overrides.
	if err := mergeFile(cfg, write("b.json", `{"web_search":{"engine_id":"cx-project"}}`)); err != nil {
		t.Fatal(err)
	}
	if cfg.WebSearch.EngineID != "cx-project" {
		t.Errorf("engine id = %q, want the layer to override", cfg.WebSearch.EngineID)
	}
}
