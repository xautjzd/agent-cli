// Package uninstall removes an agent-cli binary and, when explicitly
// requested, its disposable user data.
package uninstall

import (
	"fmt"
	"os"
	"path/filepath"
)

// Uninstaller contains the exact paths an uninstall operation may remove.
// Fields stay private so callers cannot widen the deletion scope after path
// validation.
type Uninstaller struct {
	executable string
	home       string
	config     string
	projects   string
}

// New resolves and validates the current executable and agent home. The home
// may be overridden through AGENT_HOME by the caller, but a filesystem root is
// never accepted as a data-cleanup target.
func New(executable, agentHome string) (Uninstaller, error) {
	executable, err := filepath.Abs(executable)
	if err != nil {
		return Uninstaller{}, fmt.Errorf("resolve executable: %w", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return Uninstaller{}, fmt.Errorf("resolve executable: %w", err)
	}
	info, err := os.Stat(executable)
	if err != nil {
		return Uninstaller{}, fmt.Errorf("inspect executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Uninstaller{}, fmt.Errorf("refusing to remove non-regular executable %q", executable)
	}

	agentHome, err = filepath.Abs(agentHome)
	if err != nil {
		return Uninstaller{}, fmt.Errorf("resolve agent home: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(agentHome); resolveErr == nil {
		agentHome = resolved
	} else if !os.IsNotExist(resolveErr) {
		return Uninstaller{}, fmt.Errorf("resolve agent home: %w", resolveErr)
	}
	agentHome = filepath.Clean(agentHome)
	if agentHome == filepath.Dir(agentHome) {
		return Uninstaller{}, fmt.Errorf("refusing to use filesystem root %q as agent home", agentHome)
	}

	return Uninstaller{
		executable: executable,
		home:       agentHome,
		config:     filepath.Join(agentHome, "config.json"),
		projects:   filepath.Join(agentHome, "projects"),
	}, nil
}

// Executable returns the binary that will be removed.
func (u Uninstaller) Executable() string { return u.executable }

// Home returns the agent home whose selected data may be removed.
func (u Uninstaller) Home() string { return u.home }

// Config returns the only configuration file removed by purge.
func (u Uninstaller) Config() string { return u.config }

// Projects returns the only cache directory removed by purge.
func (u Uninstaller) Projects() string { return u.projects }

// Remove uninstalls the executable. Purge additionally removes config.json and
// projects, while deliberately preserving the agent home and every other entry.
// Data is removed before the executable so a permission failure leaves the
// command available for a retry.
func (u Uninstaller) Remove(purge bool) error {
	if err := u.validate(); err != nil {
		return err
	}
	if purge {
		if err := os.RemoveAll(u.projects); err != nil {
			return fmt.Errorf("remove project cache %q: %w", u.projects, err)
		}
		if err := os.Remove(u.config); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove config %q: %w", u.config, err)
		}
	}
	if err := os.Remove(u.executable); err != nil {
		return fmt.Errorf("remove executable %q: %w", u.executable, err)
	}
	return nil
}

func (u Uninstaller) validate() error {
	if u.executable == "" || u.home == "" {
		return fmt.Errorf("invalid empty uninstall target")
	}
	if u.home == filepath.Dir(u.home) {
		return fmt.Errorf("refusing to use filesystem root %q as agent home", u.home)
	}
	if u.config != filepath.Join(u.home, "config.json") {
		return fmt.Errorf("invalid config uninstall target %q", u.config)
	}
	if u.projects != filepath.Join(u.home, "projects") {
		return fmt.Errorf("invalid project-cache uninstall target %q", u.projects)
	}
	return nil
}
