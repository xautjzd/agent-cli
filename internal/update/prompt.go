package update

import (
	"fmt"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xautjzd/agent-cli/internal/theme"
)

// Action is the user's decision at the update prompt.
type Action int

const (
	ActionUpdate Action = iota
	ActionSkip
	ActionExit
)

type promptModel struct {
	current  string
	release  Release
	selected int
	action   Action
	chosen   bool
}

func newPromptModel(current string, release Release) *promptModel {
	return &promptModel{current: current, release: release, action: ActionSkip}
}

func (m *promptModel) Init() tea.Cmd { return nil }

func (m *promptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.Type {
	case tea.KeyUp, tea.KeyCtrlP:
		m.selected = (m.selected + 2) % 3
	case tea.KeyDown, tea.KeyCtrlN, tea.KeyTab:
		m.selected = (m.selected + 1) % 3
	case tea.KeyEnter:
		m.choose(m.selected)
		return m, tea.Quit
	case tea.KeyEsc, tea.KeyCtrlC:
		m.choose(int(ActionExit))
		return m, tea.Quit
	case tea.KeyRunes:
		if len(key.Runes) == 1 && key.Runes[0] >= '1' && key.Runes[0] <= '3' {
			m.choose(int(key.Runes[0] - '1'))
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *promptModel) choose(index int) {
	m.selected = index
	m.action = Action(index)
	m.chosen = true
}

func (m *promptModel) View() string {
	if m.chosen {
		return ""
	}
	th := theme.Current()
	title := lipgloss.NewStyle().Bold(true).Render("✨ Update available!")
	versions := th.Paint(th.Muted, m.current+"  →  "+m.release.Version)
	link := lipgloss.NewStyle().Underline(true).Render(m.release.NotesURL)
	options := []string{
		"Update now " + th.Paint(th.Muted, "(verified GitHub Release)"),
		"Skip",
		"Exit",
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s\n\n", title, versions)
	fmt.Fprintf(&b, "%s %s\n\n", th.Paint(th.Muted, "Release notes:"), link)
	for i, option := range options {
		prefix := "  "
		line := fmt.Sprintf("%d. %s", i+1, option)
		if i == m.selected {
			prefix = "› "
			line = th.Paint(th.Accent, line)
		}
		fmt.Fprintln(&b, prefix+line)
	}
	fmt.Fprint(&b, "\n"+th.Paint(th.Muted, "↑↓ select · enter continue · 1/2/3 choose"))
	return b.String()
}

// Prompt displays the update choice and returns the selected action.
func Prompt(in io.Reader, out io.Writer, current string, release Release) (Action, error) {
	model := newPromptModel(current, release)
	result, err := tea.NewProgram(
		model,
		tea.WithInput(in),
		tea.WithOutput(out),
	).Run()
	if err != nil {
		return ActionSkip, err
	}
	final := result.(*promptModel)
	if !final.chosen {
		return ActionSkip, nil
	}
	return final.action, nil
}
