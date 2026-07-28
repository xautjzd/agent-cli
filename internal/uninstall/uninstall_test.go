package uninstall

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveKeepsAllUserDataByDefault(t *testing.T) {
	executable, agentHome := testInstallation(t)
	u, err := New(executable, agentHome)
	if err != nil {
		t.Fatal(err)
	}
	if err := u.Remove(false); err != nil {
		t.Fatal(err)
	}

	assertMissing(t, executable)
	assertExists(t, filepath.Join(agentHome, "config.json"))
	assertExists(t, filepath.Join(agentHome, "projects", "project", "sessions", "one.json"))
	assertExists(t, filepath.Join(agentHome, "skills", "review", "SKILL.md"))
}

func TestRemovePurgeDeletesOnlyConfigAndProjectCache(t *testing.T) {
	executable, agentHome := testInstallation(t)
	u, err := New(executable, agentHome)
	if err != nil {
		t.Fatal(err)
	}
	if err := u.Remove(true); err != nil {
		t.Fatal(err)
	}

	assertMissing(t, executable)
	assertMissing(t, filepath.Join(agentHome, "config.json"))
	assertMissing(t, filepath.Join(agentHome, "projects"))
	assertExists(t, agentHome)
	assertExists(t, filepath.Join(agentHome, "skills", "review", "SKILL.md"))
}

func TestNewRejectsUnsafeTargets(t *testing.T) {
	executable, _ := testInstallation(t)
	if _, err := New(executable, string(filepath.Separator)); err == nil {
		t.Fatal("filesystem root must not be accepted as agent home")
	}

	dir := t.TempDir()
	if _, err := New(dir, t.TempDir()); err == nil {
		t.Fatal("directory must not be accepted as an executable")
	}
}

func TestRemoveRejectsTamperedTargets(t *testing.T) {
	executable, agentHome := testInstallation(t)
	u, err := New(executable, agentHome)
	if err != nil {
		t.Fatal(err)
	}
	u.projects = filepath.Dir(agentHome)
	if err := u.Remove(true); err == nil {
		t.Fatal("tampered project target must be rejected")
	}
	assertExists(t, executable)
	assertExists(t, filepath.Join(agentHome, "config.json"))
}

func testInstallation(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	executable := filepath.Join(root, "bin", "agent")
	agentHome := filepath.Join(root, ".agents")
	writeTestFile(t, executable, "binary", 0o755)
	writeTestFile(t, filepath.Join(agentHome, "config.json"), "config", 0o600)
	writeTestFile(t, filepath.Join(agentHome, "projects", "project", "sessions", "one.json"), "session", 0o600)
	writeTestFile(t, filepath.Join(agentHome, "skills", "review", "SKILL.md"), "skill", 0o600)
	return executable, agentHome
}

func writeTestFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func assertExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("%s should exist: %v", path, err)
	}
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s should not exist, err=%v", path, err)
	}
}
