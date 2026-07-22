package config

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSaveProviderKeyProfile(t *testing.T) {
	dir := t.TempDir()
	if err := SaveProviderKey(ScopeProject, dir, "siliconflow", "sk-xyz"); err != nil {
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
