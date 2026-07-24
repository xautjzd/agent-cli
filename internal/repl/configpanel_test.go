package repl

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xautjzd/agent-cli/internal/config"
	"github.com/xautjzd/agent-cli/internal/permission"
)

// newConfigPanel builds a panel over an isolated config home so persistence
// writes don't touch the real files.
func newConfigPanel(t *testing.T) *configModel {
	t.Helper()
	isolateEnv(t)
	r, _, _ := newTestRepl(t, "")
	return newConfigModel(r, context.Background())
}

// find moves the selection to the setting with the given key.
func (m *configModel) find(t *testing.T, key string) {
	t.Helper()
	for i, vi := range m.visible {
		if configSettings[vi].key == key {
			m.sel = i
			return
		}
	}
	t.Fatalf("setting %q not visible", key)
}

func TestConfigPanelSpaceTogglesEnum(t *testing.T) {
	m := newConfigPanel(t)
	m.find(t, "permission_mode")

	before := m.repl.permMode()
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if m.repl.permMode() == before {
		t.Errorf("space did not toggle permission_mode (still %s)", before)
	}
	// It cycled to the other choice and applied live.
	if m.repl.permMode() != permission.ModeBypass && before == permission.ModeHITL {
		t.Errorf("toggle wrong: %s -> %s", before, m.repl.permMode())
	}
	// A second toggle returns to the original.
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if m.repl.permMode() != before {
		t.Errorf("second toggle did not cycle back: %s", m.repl.permMode())
	}
	// And it persisted to the global config file.
	cfg, err := config.LoadIn("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PermissionMode != string(before) {
		t.Errorf("permission_mode not persisted: %q", cfg.PermissionMode)
	}
	// The panel stays open (not aborted) so the next setting can be edited.
	if m.abort {
		t.Error("panel closed after a toggle; it should stay open")
	}
}

func TestConfigPanelThinkingToggle(t *testing.T) {
	m := newConfigPanel(t)
	m.find(t, "thinking")

	// The effort ladder cycles off → low → medium → high → adaptive → off.
	// Starting from the default (adaptive), the first toggle lands on off.
	want := []string{"off", "low", "medium", "high", "adaptive"}
	for _, w := range want {
		m.Update(tea.KeyMsg{Type: tea.KeySpace})
		if m.repl.Cfg.Thinking != w {
			t.Fatalf("thinking toggle = %q, want %q", m.repl.Cfg.Thinking, w)
		}
	}
}

func TestConfigPanelAutoCompactToggle(t *testing.T) {
	m := newConfigPanel(t)
	m.find(t, "auto_compact")

	// Default resolves to "on"; toggling flips it to "off" and mirrors it
	// onto the running agent.
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if m.repl.Cfg.AutoCompact != "off" || m.repl.Agent.AutoCompact {
		t.Errorf("auto_compact toggle = %q agent=%v, want off/false", m.repl.Cfg.AutoCompact, m.repl.Agent.AutoCompact)
	}
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if m.repl.Cfg.AutoCompact != "on" || !m.repl.Agent.AutoCompact {
		t.Errorf("auto_compact toggle back = %q agent=%v, want on/true", m.repl.Cfg.AutoCompact, m.repl.Agent.AutoCompact)
	}
}

func TestConfigPanelEnterEditsText(t *testing.T) {
	m := newConfigPanel(t)
	m.find(t, "model")

	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // open editor
	if !m.editing {
		t.Fatal("Enter did not open the inline editor")
	}
	// The editor is pre-filled with the current value.
	if m.editor.Value() == "" {
		t.Error("editor should pre-fill the current model")
	}
	m.editor.SetValue("deepseek-reasoner")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // commit
	if m.editing {
		t.Error("editor still open after commit")
	}
	if m.repl.Cfg.Model != "deepseek-reasoner" {
		t.Errorf("model = %q, want deepseek-reasoner", m.repl.Cfg.Model)
	}
	cfg, _ := config.LoadIn("")
	if cfg.Model != "deepseek-reasoner" {
		t.Errorf("model not persisted: %q", cfg.Model)
	}

	// Esc cancels an edit without changing anything.
	m.find(t, "model")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.editor.SetValue("scrapped")
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.repl.Cfg.Model != "deepseek-reasoner" {
		t.Errorf("Esc did not cancel the edit: %q", m.repl.Cfg.Model)
	}
}

func TestConfigPanelSearchFilters(t *testing.T) {
	m := newConfigPanel(t)
	all := len(m.visible)
	// Typing narrows the list.
	for _, r := range "vision" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if len(m.visible) == 0 || len(m.visible) >= all {
		t.Fatalf("search did not filter: %d of %d", len(m.visible), all)
	}
	for _, vi := range m.visible {
		if !strings.Contains(strings.ToLower(configSettings[vi].label), "vision") {
			t.Errorf("non-matching row survived filter: %s", configSettings[vi].label)
		}
	}
}

func TestConfigPanelIntValidation(t *testing.T) {
	m := newConfigPanel(t)
	m.find(t, "max_turns")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.editor.SetValue("not-a-number")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.status, "positive integer") {
		t.Errorf("invalid int should be rejected with a message: %q", m.status)
	}
	// A valid value applies.
	m.find(t, "max_turns")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.editor.SetValue("55")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.repl.Agent.MaxTurns != 55 {
		t.Errorf("max_turns not applied: %d", m.repl.Agent.MaxTurns)
	}
}

func TestConfigPanelEscExits(t *testing.T) {
	m := newConfigPanel(t)
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !m.abort {
		t.Error("Esc should exit the panel")
	}
}
