package config

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSaveProviderKeyProfile(t *testing.T) {
	dir := t.TempDir()
	if err := SaveProviderKey(ScopeProject, dir, "siliconflow", "", "sk-xyz"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(ProjectPath(dir))
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	p, ok := cfg.Providers["siliconflow"]
	if !ok || p.APIKey != "sk-xyz" {
		t.Fatalf("profile wrong: %+v ok=%v", p, ok)
	}
	if p.BaseURL == "" || p.Format == "" {
		t.Errorf("connection details not filled from catalog: %+v", p)
	}
}

// A key saved while a vendor is addressed over its Anthropic wire must pin
// that endpoint, not the vendor's primary one — otherwise the profile it
// writes would reconnect to the wrong wire.
func TestSaveProviderKeyPinsTheActiveWire(t *testing.T) {
	dir := t.TempDir()
	if err := SaveProviderKey(ScopeProject, dir, "deepseek", "anthropic", "sk-xyz"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(ProjectPath(dir))
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	p := cfg.Providers["deepseek"]
	if p.BaseURL != "https://api.deepseek.com/anthropic" || p.Format != "anthropic" || p.Auth != "bearer" {
		t.Errorf("profile did not follow the anthropic wire: %+v", p)
	}
}
