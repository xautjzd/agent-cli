package uninstall

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

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
	selectedPurge := *purge
	if !*yes {
		if !interactive {
			return fmt.Errorf("uninstall requires an interactive terminal; pass --yes to confirm")
		}
		if *purge {
			printUninstallTargets(out, remover, true, currentVersion)
			confirmed, err := readConfirmation(in, out)
			if err != nil {
				return err
			}
			if !confirmed {
				fmt.Fprintln(out, "Uninstall cancelled.")
				return nil
			}
		} else {
			action, err := readUninstallChoice(in, out, remover, currentVersion)
			if err != nil {
				return err
			}
			switch action {
			case 1:
				selectedPurge = false
			case 2:
				selectedPurge = true
			default:
				fmt.Fprintln(out, "Uninstall cancelled.")
				return nil
			}
		}
	} else {
		printUninstallTargets(out, remover, selectedPurge, currentVersion)
	}

	if err := remover.Remove(selectedPurge); err != nil {
		return err
	}
	fmt.Fprintf(out, "\nRemoved agent-cli %s from %s.\n", currentVersion, remover.Executable())
	if selectedPurge {
		fmt.Fprintf(out, "Removed %s and %s.\n", remover.Config(), remover.Projects())
		fmt.Fprintf(out, "Preserved all other data in %s.\n", remover.Home())
	} else {
		fmt.Fprintf(out, "Preserved all user data in %s.\n", remover.Home())
	}
	return nil
}

func readUninstallChoice(in io.Reader, out io.Writer, remover Uninstaller, currentVersion string) (int, error) {
	fmt.Fprintf(out, "Uninstall agent-cli %s\n\n", currentVersion)
	fmt.Fprintf(out, "Executable:\n  %s\n\n", remover.Executable())
	fmt.Fprintln(out, "1. Uninstall and keep all user data")
	fmt.Fprintln(out, "2. Uninstall and also remove:")
	fmt.Fprintf(out, "   %s\n", remover.Config())
	fmt.Fprintf(out, "   %s\n", remover.Projects())
	fmt.Fprintln(out, "3. Cancel")
	fmt.Fprint(out, "\nChoose 1, 2, or 3: ")

	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, fmt.Errorf("read uninstall choice: %w", err)
	}
	switch strings.TrimSpace(line) {
	case "1":
		return 1, nil
	case "2":
		return 2, nil
	case "3", "":
		return 3, nil
	default:
		return 0, fmt.Errorf("invalid choice: enter 1, 2, or 3")
	}
}

func readConfirmation(in io.Reader, out io.Writer) (bool, error) {
	fmt.Fprint(out, "\nContinue? [y/N]: ")
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read uninstall confirmation: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
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
