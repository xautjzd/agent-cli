package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestThemeValidator(t *testing.T) {
	v := validKeys["theme"]
	if v == nil {
		t.Fatal("theme has no validator")
	}
	if err := v("dracula"); err != nil {
		t.Errorf("dracula should be valid: %v", err)
	}
	if err := v("not-a-theme"); err == nil {
		t.Error("unknown theme accepted")
	}
}

func TestThemeInKeys(t *testing.T) {
	found := false
	for _, k := range Keys() {
		if k == "theme" {
			found = true
		}
	}
	if !found {
		t.Error("theme missing from Keys()")
	}
}

func TestThemeMergesFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"theme":"nord"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{}
	if err := mergeFile(cfg, path); err != nil {
		t.Fatal(err)
	}
	if cfg.Theme != "nord" {
		t.Errorf("theme not merged: %q", cfg.Theme)
	}
}
