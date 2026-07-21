// Package home resolves the agent's user-level directory, which holds the
// global config, skills and instructions.
//
// Both `~/.agent` and `~/.agents` are recognized: the singular form is the
// documented default, but the plural is a common spelling and silently
// ignoring a populated one leaves users staring at defaults with no clue
// why their configuration had no effect.
package home

import (
	"os"
	"path/filepath"
)

// Candidate directory names, in preference order.
var candidates = []string{".agent", ".agents"}

// Dir returns the agent home directory.
//
// Key flow: an explicit AGENT_HOME wins; otherwise the first candidate that
// already exists is used, so an established layout keeps working; when none
// exists the documented default is returned for creation.
func Dir() string {
	if v := os.Getenv("AGENT_HOME"); v != "" {
		return v
	}
	base, err := os.UserHomeDir()
	if err != nil {
		return candidates[0]
	}
	for _, name := range candidates {
		path := filepath.Join(base, name)
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return path
		}
	}
	return filepath.Join(base, candidates[0])
}

// Path joins elements onto the agent home directory.
func Path(elem ...string) string {
	return filepath.Join(append([]string{Dir()}, elem...)...)
}
