package repl

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/xautjzd/agent-cli/internal/config"
	"github.com/xautjzd/agent-cli/internal/webtool"
)

func TestSwitchSearchProviderAppliesLive(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	r.Search = webtool.NewSwitchable(webtool.NewSearcher("duckduckgo", webtool.Credentials{}, nil))

	if err := r.switchSearchProvider("bing-cn"); err != nil {
		t.Fatal(err)
	}
	if got := r.Search.Name(); got != "bing-cn" {
		t.Errorf("live searcher = %q, want bing-cn", got)
	}
	if got := r.Cfg.WebSearch.Provider; got != "bing-cn" {
		t.Errorf("cfg provider = %q, want bing-cn", got)
	}
}

// An alias must land in the config as the canonical name, so the file
// documents what is actually in use.
func TestSwitchSearchProviderCanonicalizes(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	r.Search = webtool.NewSwitchable(webtool.NewSearcher("", webtool.Credentials{}, nil))

	if err := r.switchSearchProvider("cn.bing.com"); err != nil {
		t.Fatal(err)
	}
	if got := r.Cfg.WebSearch.Provider; got != "bing-cn" {
		t.Errorf("cfg provider = %q, want the canonical bing-cn", got)
	}
}

func TestSwitchSearchProviderRejectsUnknown(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	r.Search = webtool.NewSwitchable(webtool.NewSearcher("duckduckgo", webtool.Credentials{}, nil))

	err := r.switchSearchProvider("gogle")
	if err == nil {
		t.Fatal("unknown engine should error")
	}
	if !strings.Contains(err.Error(), "duckduckgo") {
		t.Errorf("error should list the valid engines: %v", err)
	}
	if r.Search.Name() != "duckduckgo" || r.Cfg.WebSearch.Provider != "" {
		t.Error("a rejected switch must leave the session untouched")
	}
}

// Switching to a key-only backend with no key would persist a config whose
// every search fails, so it is refused and nothing is left half-changed.
func TestSwitchSearchProviderNeedsKey(t *testing.T) {
	t.Setenv("BRAVE_API_KEY", "")
	r, _, _ := newTestRepl(t, "")
	r.Cfg.WebSearch.Provider = "bing"
	r.Search = webtool.NewSwitchable(webtool.NewSearcher("bing", webtool.Credentials{}, nil))

	err := r.switchSearchProvider("brave")
	if err == nil {
		t.Fatal("brave without a key should error")
	}
	if !strings.Contains(err.Error(), "BRAVE_API_KEY") {
		t.Errorf("error should name the env var: %v", err)
	}
	if r.Cfg.WebSearch.Provider != "bing" {
		t.Errorf("provider rolled forward on failure: %q", r.Cfg.WebSearch.Provider)
	}
	if r.Search.Name() != "bing" {
		t.Errorf("live searcher changed on failure: %q", r.Search.Name())
	}
}

func TestSwitchSearchProviderWithKeyFromEnv(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "tvly-test")
	r, _, _ := newTestRepl(t, "")
	r.Search = webtool.NewSwitchable(webtool.NewSearcher("duckduckgo", webtool.Credentials{}, nil))

	if err := r.switchSearchProvider("tavily"); err != nil {
		t.Fatal(err)
	}
	if got := r.Search.Name(); got != "tavily" {
		t.Errorf("live searcher = %q, want tavily", got)
	}
}

// Google is the only backend needing two credentials, so a switch must check
// both — a key alone gets HTTP 400 on every search.
func TestSwitchSearchProviderGoogleNeedsEngineID(t *testing.T) {
	t.Setenv("GOOGLE_API_KEY", "key-test")
	t.Setenv("GOOGLE_SEARCH_ENGINE_ID", "")
	t.Setenv("GOOGLE_CSE_ID", "")
	r, _, _ := newTestRepl(t, "")
	r.Search = webtool.NewSwitchable(webtool.NewSearcher("duckduckgo", webtool.Credentials{}, nil))

	err := r.switchSearchProvider("google")
	if err == nil {
		t.Fatal("google without an engine ID should error")
	}
	if !strings.Contains(err.Error(), "GOOGLE_SEARCH_ENGINE_ID") {
		t.Errorf("error should name the missing piece: %v", err)
	}
	if r.Search.Name() != "duckduckgo" || r.Cfg.WebSearch.Provider != "" {
		t.Error("a rejected switch must leave the session untouched")
	}

	// With both halves present it goes through.
	t.Setenv("GOOGLE_SEARCH_ENGINE_ID", "cx-test")
	if err := r.switchSearchProvider("google.com"); err != nil {
		t.Fatal(err)
	}
	if r.Search.Name() != "google" {
		t.Errorf("live searcher = %q, want google", r.Search.Name())
	}
	if r.Cfg.WebSearch.Provider != "google" {
		t.Errorf("cfg provider = %q, want the canonical google", r.Cfg.WebSearch.Provider)
	}
}

// The whole point of the setting: the choice survives a restart.
func TestSearchProviderPersistsToConfigFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENT_HOME", home)

	r, _, _ := newTestRepl(t, "")
	r.Search = webtool.NewSwitchable(webtool.NewSearcher("duckduckgo", webtool.Credentials{}, nil))

	if err := r.setConfigValue(context.Background(), "web_search_provider", "baidu"); err != nil {
		t.Fatal(err)
	}
	if got := r.Search.Name(); got != "baidu" {
		t.Errorf("live searcher = %q, want baidu", got)
	}

	path, err := config.Path()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	var saved struct {
		WebSearch struct {
			Provider string `json:"provider"`
		} `json:"web_search"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.WebSearch.Provider != "baidu" {
		t.Errorf("saved provider = %q, want baidu (file: %s)", saved.WebSearch.Provider, data)
	}
}

// The panel row and the config key must agree, or the setting shows one engine
// and saves another.
func TestSearchProviderPanelRow(t *testing.T) {
	var row setting
	for _, s := range configSettings {
		if s.key == "web_search_provider" {
			row = s
		}
	}
	if row.key == "" {
		t.Fatal("web_search_provider is missing from the /config panel")
	}
	if row.kind != kindEnum || len(row.choices) == 0 {
		t.Fatal("the engine row should offer a fixed list of choices")
	}
	for _, c := range row.choices {
		if _, ok := webtool.CanonicalProvider(c); !ok {
			t.Errorf("panel offers %q, which is not a real backend", c)
		}
	}

	r, _, _ := newTestRepl(t, "")
	if got := r.currentValue("web_search_provider"); got != "duckduckgo" {
		t.Errorf("unset engine shows %q, want the duckduckgo default", got)
	}
}
