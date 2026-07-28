package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunUninstallChoiceKeepsData(t *testing.T) {
	executable, agentHome := testUninstallFiles(t)
	var out bytes.Buffer
	if err := runUninstallWithIO(nil, strings.NewReader("1\n"), &out, executable, agentHome, true); err != nil {
		t.Fatal(err)
	}
	assertUninstallMissing(t, executable)
	assertUninstallExists(t, filepath.Join(agentHome, "config.json"))
	assertUninstallExists(t, filepath.Join(agentHome, "projects", "session.json"))
	if !strings.Contains(out.String(), "Preserved all user data") {
		t.Fatalf("output does not explain preservation:\n%s", out.String())
	}
}

func TestRunUninstallChoicePurgesOnlySelectedData(t *testing.T) {
	executable, agentHome := testUninstallFiles(t)
	var out bytes.Buffer
	if err := runUninstallWithIO(nil, strings.NewReader("2\n"), &out, executable, agentHome, true); err != nil {
		t.Fatal(err)
	}
	assertUninstallMissing(t, executable)
	assertUninstallMissing(t, filepath.Join(agentHome, "config.json"))
	assertUninstallMissing(t, filepath.Join(agentHome, "projects"))
	assertUninstallExists(t, filepath.Join(agentHome, "skills", "review", "SKILL.md"))
	if !strings.Contains(out.String(), "Preserved all other data") {
		t.Fatalf("output does not explain preservation:\n%s", out.String())
	}
}

func TestRunUninstallCancelLeavesEverything(t *testing.T) {
	executable, agentHome := testUninstallFiles(t)
	var out bytes.Buffer
	if err := runUninstallWithIO(nil, strings.NewReader("3\n"), &out, executable, agentHome, true); err != nil {
		t.Fatal(err)
	}
	assertUninstallExists(t, executable)
	assertUninstallExists(t, filepath.Join(agentHome, "config.json"))
	if !strings.Contains(out.String(), "cancelled") {
		t.Fatalf("output does not report cancellation:\n%s", out.String())
	}
}

func TestRunUninstallRequiresYesWithoutTTY(t *testing.T) {
	executable, agentHome := testUninstallFiles(t)
	var out bytes.Buffer
	err := runUninstallWithIO(nil, strings.NewReader("1\n"), &out, executable, agentHome, false)
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("error = %v, want --yes guidance", err)
	}
	assertUninstallExists(t, executable)
}

func TestRunUninstallPurgeYesDoesNotPrompt(t *testing.T) {
	executable, agentHome := testUninstallFiles(t)
	var out bytes.Buffer
	if err := runUninstallWithIO(
		[]string{"--purge", "--yes"},
		strings.NewReader(""),
		&out,
		executable,
		agentHome,
		false,
	); err != nil {
		t.Fatal(err)
	}
	assertUninstallMissing(t, executable)
	assertUninstallMissing(t, filepath.Join(agentHome, "config.json"))
	assertUninstallMissing(t, filepath.Join(agentHome, "projects"))
	assertUninstallExists(t, filepath.Join(agentHome, "skills", "review", "SKILL.md"))
}

func TestRunUninstallRejectsInvalidChoiceAndArguments(t *testing.T) {
	executable, agentHome := testUninstallFiles(t)
	var out bytes.Buffer
	if err := runUninstallWithIO(nil, strings.NewReader("delete all\n"), &out, executable, agentHome, true); err == nil {
		t.Fatal("invalid choice must fail")
	}
	assertUninstallExists(t, executable)

	if err := runUninstallWithIO([]string{"extra"}, strings.NewReader(""), &out, executable, agentHome, true); err == nil {
		t.Fatal("positional arguments must fail")
	}
}

func testUninstallFiles(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	executable := filepath.Join(root, "bin", "agent")
	agentHome := filepath.Join(root, ".agents")
	writeUninstallFile(t, executable, "binary", 0o755)
	writeUninstallFile(t, filepath.Join(agentHome, "config.json"), "config", 0o600)
	writeUninstallFile(t, filepath.Join(agentHome, "projects", "session.json"), "session", 0o600)
	writeUninstallFile(t, filepath.Join(agentHome, "skills", "review", "SKILL.md"), "skill", 0o600)
	return executable, agentHome
}

func writeUninstallFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func assertUninstallExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("%s should exist: %v", path, err)
	}
}

func assertUninstallMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s should not exist, err=%v", path, err)
	}
}
