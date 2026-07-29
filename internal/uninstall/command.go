package uninstall

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xautjzd/agent-cli/internal/theme"
)

// Choice is the action selected from the interactive uninstall prompt.
type Choice int

const (
	ChoiceKeep Choice = iota
	ChoicePurge
	ChoiceCancel
)

type promptFunc func(io.Reader, io.Writer, Uninstaller, bool, string) (Choice, error)

// Run parses the uninstall command, prompts when needed, and removes the
// validated targets. Environment-specific paths and terminal state are passed
// in by the CLI entry point so the command remains testable.
func Run(
	args []string,
	in io.Reader,
	out io.Writer,
	executable string,
	agentHome string,
	interactive bool,
	currentVersion string,
) error {
	return run(args, in, out, executable, agentHome, interactive, currentVersion, Prompt)
}

func run(
	args []string,
	in io.Reader,
	out io.Writer,
	executable string,
	agentHome string,
	interactive bool,
	currentVersion string,
	prompt promptFunc,
) error {
	fs := flag.NewFlagSet("agent uninstall", flag.ContinueOnError)
	fs.SetOutput(out)
	purge := fs.Bool("purge", false, "also remove config.json and the projects cache")
	yes := fs.Bool("yes", false, "confirm without prompting")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: agent uninstall [--purge] [--yes]")
	}

	remover, err := New(executable, agentHome)
	if err != nil {
		return err
	}
	choice := ChoiceKeep
	if *yes {
		if *purge {
			choice = ChoicePurge
		}
		printUninstallTargets(out, remover, choice == ChoicePurge, currentVersion)
	} else {
		if !interactive {
			return fmt.Errorf("uninstall requires an interactive terminal; pass --yes to confirm")
		}
		choice, err = prompt(in, out, remover, *purge, currentVersion)
		if err != nil {
			return err
		}
	}
	if choice == ChoiceCancel {
		fmt.Fprintln(out, "Uninstall cancelled.")
		return nil
	}

	purgeData := choice == ChoicePurge
	if err := remover.Remove(purgeData); err != nil {
		return err
	}
	fmt.Fprintf(out, "\nRemoved agent-cli %s from %s.\n", currentVersion, remover.Executable())
	if purgeData {
		fmt.Fprintf(out, "Removed %s and %s.\n", remover.Config(), remover.Projects())
		fmt.Fprintf(out, "Preserved all other data in %s.\n", remover.Home())
	} else {
		fmt.Fprintf(out, "Preserved all user data in %s.\n", remover.Home())
	}
	return nil
}

type promptModel struct {
	remover        Uninstaller
	currentVersion string
	purgeOnly      bool
	selected       int
	choice         Choice
	chosen         bool
}

func newPromptModel(remover Uninstaller, purgeOnly bool, currentVersion string) *promptModel {
	return &promptModel{
		remover:        remover,
		currentVersion: currentVersion,
		purgeOnly:      purgeOnly,
		choice:         ChoiceCancel,
	}
}

func (m *promptModel) Init() tea.Cmd { return nil }

func (m *promptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	count := 3
	if m.purgeOnly {
		count = 2
	}
	switch key.Type {
	case tea.KeyUp, tea.KeyCtrlP:
		m.selected = (m.selected + count - 1) % count
	case tea.KeyDown, tea.KeyCtrlN, tea.KeyTab:
		m.selected = (m.selected + 1) % count
	case tea.KeyEnter:
		if m.purgeOnly {
			if m.selected == 0 {
				m.choice = ChoicePurge
			} else {
				m.choice = ChoiceCancel
			}
		} else {
			m.choice = Choice(m.selected)
		}
		m.chosen = true
		return m, tea.Quit
	case tea.KeyEsc, tea.KeyCtrlC:
		m.choice = ChoiceCancel
		m.chosen = true
		return m, tea.Quit
	}
	return m, nil
}

func (m *promptModel) View() string {
	if m.chosen {
		return ""
	}
	th := theme.Current()
	var b strings.Builder
	fmt.Fprintf(&b, "Uninstall agent-cli %s\n\n", m.currentVersion)
	fmt.Fprintf(&b, "%s\n  %s\n\n", th.Paint(th.Muted, "Executable:"), m.remover.Executable())

	var options []string
	if m.purgeOnly {
		options = []string{"Uninstall and remove user data", "Cancel"}
	} else {
		options = []string{
			"Uninstall and keep user data",
			"Uninstall and remove user data",
			"Cancel",
		}
	}
	for i, option := range options {
		line := "  " + option
		if i == m.selected {
			line = th.Paint(th.Accent, "› "+option)
		}
		fmt.Fprintln(&b, line)
	}
	fmt.Fprint(&b, "\n"+th.Paint(th.Muted, "↑↓ select · enter confirm · esc cancel"))
	return b.String()
}

// Prompt renders the keyboard-driven uninstall selector.
func Prompt(in io.Reader, out io.Writer, remover Uninstaller, purgeOnly bool, currentVersion string) (Choice, error) {
	model := newPromptModel(remover, purgeOnly, currentVersion)
	result, err := tea.NewProgram(
		model,
		tea.WithInput(in),
		tea.WithOutput(out),
	).Run()
	if err != nil {
		return ChoiceCancel, err
	}
	final := result.(*promptModel)
	if !final.chosen {
		return ChoiceCancel, nil
	}
	return final.choice, nil
}

func printUninstallTargets(out io.Writer, remover Uninstaller, purge bool, currentVersion string) {
	fmt.Fprintf(out, "Uninstall agent-cli %s\n\n", currentVersion)
	fmt.Fprintf(out, "Executable:\n  %s\n", remover.Executable())
	if purge {
		fmt.Fprintf(out, "Config:\n  %s\n", remover.Config())
		fmt.Fprintf(out, "Project cache:\n  %s\n", remover.Projects())
		fmt.Fprintf(out, "Other files in %s will be preserved.\n", remover.Home())
	} else {
		fmt.Fprintf(out, "All user data in %s will be preserved.\n", remover.Home())
	}
}
