package home

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirResolution(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("AGENT_HOME", "")

	// Nothing exists yet: new installs use the shared agent-skills directory.
	if got, want := Dir(), filepath.Join(base, ".agents"); got != want {
		t.Errorf("empty home: Dir() = %q, want %q", got, want)
	}

	// An existing legacy ~/.agent remains supported.
	singular := filepath.Join(base, ".agent")
	if err := os.MkdirAll(singular, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Dir(); got != singular {
		t.Errorf("with legacy ~/.agent present: Dir() = %q, want %q", got, singular)
	}

	// When both exist the standard plural form wins.
	plural := filepath.Join(base, ".agents")
	if err := os.MkdirAll(plural, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Dir(); got != plural {
		t.Errorf("with both present: Dir() = %q, want %q", got, plural)
	}

	// An explicit override beats both.
	t.Setenv("AGENT_HOME", "/custom/agent")
	if got := Dir(); got != "/custom/agent" {
		t.Errorf("AGENT_HOME ignored: Dir() = %q", got)
	}
	if got, want := Path("config.json"), "/custom/agent/config.json"; got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

// A regular file at the preferred path must not hide a valid legacy directory.
func TestDirIgnoresNonDirectories(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("AGENT_HOME", "")

	if err := os.WriteFile(filepath.Join(base, ".agents"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(base, ".agent")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Dir(); got != legacy {
		t.Errorf("Dir() = %q, want the real directory %q", got, legacy)
	}
}
