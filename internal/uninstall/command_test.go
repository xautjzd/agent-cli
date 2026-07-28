package uninstall

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"

	"github.com/xautjzd/agent-cli/internal/theme"
)

func TestRunUninstallChoiceKeepsData(t *testing.T) {
	executable, agentHome := testUninstallFiles(t)
	var out bytes.Buffer
	if err := run(nil, strings.NewReader(""), &out, executable, agentHome, true, "0.1.1", promptChoice(ChoiceKeep)); err != nil {
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
	if err := run(nil, strings.NewReader(""), &out, executable, agentHome, true, "0.1.1", promptChoice(ChoicePurge)); err != nil {
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
	if err := run(nil, strings.NewReader(""), &out, executable, agentHome, true, "0.1.1", promptChoice(ChoiceCancel)); err != nil {
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
	err := run(nil, strings.NewReader(""), &out, executable, agentHome, false, "0.1.1", promptChoice(ChoiceKeep))
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("error = %v, want --yes guidance", err)
	}
	assertUninstallExists(t, executable)
}

func TestRunUninstallPurgeYesDoesNotPrompt(t *testing.T) {
	executable, agentHome := testUninstallFiles(t)
	var out bytes.Buffer
	if err := Run(
		[]string{"--purge", "--yes"},
		strings.NewReader(""),
		&out,
		executable,
		agentHome,
		false,
		"0.1.1",
	); err != nil {
		t.Fatal(err)
	}
	assertUninstallMissing(t, executable)
	assertUninstallMissing(t, filepath.Join(agentHome, "config.json"))
	assertUninstallMissing(t, filepath.Join(agentHome, "projects"))
	assertUninstallExists(t, filepath.Join(agentHome, "skills", "review", "SKILL.md"))
}

func TestRunUninstallRejectsPositionalArguments(t *testing.T) {
	executable, agentHome := testUninstallFiles(t)
	var out bytes.Buffer
	if err := Run([]string{"extra"}, strings.NewReader(""), &out, executable, agentHome, true, "0.1.1"); err == nil {
		t.Fatal("positional arguments must fail")
	}
	assertUninstallExists(t, executable)
}

func TestPromptModelUsesArrowsEnterAndEscape(t *testing.T) {
	executable, agentHome := testUninstallFiles(t)
	remover, err := New(executable, agentHome)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		purgeOnly bool
		keys      []tea.KeyMsg
		choice    Choice
	}{
		{"default keeps data", false, []tea.KeyMsg{{Type: tea.KeyEnter}}, ChoiceKeep},
		{"down selects purge", false, []tea.KeyMsg{{Type: tea.KeyDown}, {Type: tea.KeyEnter}}, ChoicePurge},
		{"up wraps to cancel", false, []tea.KeyMsg{{Type: tea.KeyUp}, {Type: tea.KeyEnter}}, ChoiceCancel},
		{"escape cancels", false, []tea.KeyMsg{{Type: tea.KeyEsc}}, ChoiceCancel},
		{"purge continues", true, []tea.KeyMsg{{Type: tea.KeyEnter}}, ChoicePurge},
		{"purge down cancels", true, []tea.KeyMsg{{Type: tea.KeyDown}, {Type: tea.KeyEnter}}, ChoiceCancel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := newPromptModel(remover, tt.purgeOnly, "0.1.1")
			for _, key := range tt.keys {
				model.Update(key)
			}
			if !model.chosen || model.choice != tt.choice {
				t.Fatalf("chosen=%v choice=%v, want %v", model.chosen, model.choice, tt.choice)
			}
		})
	}
}

func TestPromptModelIgnoresNumberKeys(t *testing.T) {
	executable, agentHome := testUninstallFiles(t)
	remover, err := New(executable, agentHome)
	if err != nil {
		t.Fatal(err)
	}
	model := newPromptModel(remover, false, "0.1.1")
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	if model.chosen || model.selected != 0 {
		t.Fatalf("number key changed prompt: chosen=%v selected=%d", model.chosen, model.selected)
	}
}

func TestPromptViewHasKeyboardInstructionsWithoutNumbers(t *testing.T) {
	theme.SetColorProfile(termenv.Ascii)
	executable, agentHome := testUninstallFiles(t)
	remover, err := New(executable, agentHome)
	if err != nil {
		t.Fatal(err)
	}
	view := newPromptModel(remover, false, "0.1.1").View()
	for _, want := range []string{
		"Uninstall agent-cli 0.1.1",
		"Uninstall and keep all user data",
		"Uninstall and remove config + project cache",
		"↑↓ select · enter confirm · esc cancel",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("prompt missing %q:\n%s", want, view)
		}
	}
	if regexp.MustCompile(`(?m)^\s*[123]\.`).MatchString(view) {
		t.Errorf("prompt contains numbered options:\n%s", view)
	}
	if strings.Contains(view, "Choose 1") {
		t.Errorf("prompt asks for a numeric choice:\n%s", view)
	}
}

func promptChoice(choice Choice) promptFunc {
	return func(io.Reader, io.Writer, Uninstaller, bool, string) (Choice, error) {
		return choice, nil
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
