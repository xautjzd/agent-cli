package update

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"

	"github.com/xautjzd/agent-cli/internal/theme"
)

func TestPromptShowsCurrentAndTargetVersions(t *testing.T) {
	theme.SetColorProfile(termenv.Ascii)
	model := newPromptModel("0.1.1", testRelease())
	view := model.View()
	for _, want := range []string{
		"Update available!",
		"0.1.1  →  0.2.0",
		"https://github.com/xautjzd/agent-cli/releases/tag/v0.2.0",
		"1. Update now",
		"2. Skip",
		"3. Exit",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("prompt missing %q:\n%s", want, view)
		}
	}
}

func TestPromptModelSupportsArrowsEnterAndNumbers(t *testing.T) {
	tests := []struct {
		name   string
		keys   []tea.KeyMsg
		action Action
	}{
		{"default update", []tea.KeyMsg{{Type: tea.KeyEnter}}, ActionUpdate},
		{"down to skip", []tea.KeyMsg{{Type: tea.KeyDown}, {Type: tea.KeyEnter}}, ActionSkip},
		{"up wraps to exit", []tea.KeyMsg{{Type: tea.KeyUp}, {Type: tea.KeyEnter}}, ActionExit},
		{"number update", []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune{'1'}}}, ActionUpdate},
		{"number skip", []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune{'2'}}}, ActionSkip},
		{"number exit", []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune{'3'}}}, ActionExit},
		{"escape exits", []tea.KeyMsg{{Type: tea.KeyEsc}}, ActionExit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := newPromptModel("0.1.1", testRelease())
			for _, key := range tt.keys {
				model.Update(key)
			}
			if !model.chosen || model.action != tt.action {
				t.Fatalf("chosen=%v action=%v, want %v", model.chosen, model.action, tt.action)
			}
		})
	}
}

func TestPromptIgnoresOtherKeys(t *testing.T) {
	model := newPromptModel("0.1.1", testRelease())
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if model.chosen || model.selected != 0 {
		t.Fatalf("unexpected state: chosen=%v selected=%d", model.chosen, model.selected)
	}
}
