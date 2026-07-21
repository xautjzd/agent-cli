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

	// Nothing exists yet: the documented default is returned so new
	// installs create the singular directory.
	if got, want := Dir(), filepath.Join(base, ".agent"); got != want {
		t.Errorf("empty home: Dir() = %q, want %q", got, want)
	}

	// A populated ~/.agents is honored — silently ignoring it is what
	// leaves users staring at defaults.
	plural := filepath.Join(base, ".agents")
	if err := os.MkdirAll(plural, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Dir(); got != plural {
		t.Errorf("with ~/.agents present: Dir() = %q, want %q", got, plural)
	}

	// When both exist the singular form wins, so behavior stays
	// deterministic rather than depending on creation order.
	singular := filepath.Join(base, ".agent")
	if err := os.MkdirAll(singular, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Dir(); got != singular {
		t.Errorf("with both present: Dir() = %q, want %q", got, singular)
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

// A regular file named .agent must not be mistaken for the directory.
func TestDirIgnoresNonDirectories(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("AGENT_HOME", "")

	if err := os.WriteFile(filepath.Join(base, ".agent"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	plural := filepath.Join(base, ".agents")
	if err := os.MkdirAll(plural, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Dir(); got != plural {
		t.Errorf("Dir() = %q, want the real directory %q", got, plural)
	}
}
