// Package memory implements project-scoped persistent memory.
//
// Two complementary mechanisms are provided:
//
//  1. Instruction files (AGENT.md): user-authored context loaded verbatim
//     into the system prompt, read from ~/.agents/AGENT.md (global) and
//     <project>/AGENT.md (project), project last so it wins on conflicts.
//  2. A memory store (.agent/memory/*.md): facts the agent saves during
//     sessions via the remember tool, indexed and re-loaded next session.
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/xautjzd/agent-cli/internal/home"
)

// Entry is one saved memory.
type Entry struct {
	// Name is the kebab-case slug identifying the memory file.
	Name string
	// Content is the memory body.
	Content string
}

// Store persists and recalls memories. The agent depends on this interface
// so alternative backends (e.g. SQLite) can be swapped in later (DIP).
type Store interface {
	Save(name, content string) error
	List() ([]Entry, error)
	Get(name string) (*Entry, error)
	Delete(name string) error
}

// FileStore keeps each memory as one markdown file under Dir.
type FileStore struct {
	Dir string
}

// NewProjectStore returns the store for a project's .agent/memory directory.
func NewProjectStore(projectDir string) *FileStore {
	return &FileStore{Dir: filepath.Join(projectDir, ".agent", "memory")}
}

var slugRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Save writes one memory file. Names are validated as kebab-case slugs so
// they stay filesystem- and human-friendly.
func (s *FileStore) Save(name, content string) error {
	if !slugRe.MatchString(name) {
		return fmt.Errorf("memory name must be a kebab-case slug, got %q", name)
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("memory content must not be empty")
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path(name), []byte(strings.TrimSpace(content)+"\n"), 0o644)
}

// List returns all memories sorted by name.
func (s *FileStore) List() ([]Entry, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no memory yet is a normal state
		}
		return nil, err
	}
	var out []Entry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.Dir, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, Entry{
			Name:    strings.TrimSuffix(e.Name(), ".md"),
			Content: strings.TrimSpace(string(data)),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns one memory by name.
func (s *FileStore) Get(name string) (*Entry, error) {
	data, err := os.ReadFile(s.path(name))
	if err != nil {
		return nil, fmt.Errorf("memory %q not found", name)
	}
	return &Entry{Name: name, Content: strings.TrimSpace(string(data))}, nil
}

// Delete removes one memory by name.
func (s *FileStore) Delete(name string) error {
	if err := os.Remove(s.path(name)); err != nil {
		return fmt.Errorf("memory %q not found", name)
	}
	return nil
}

func (s *FileStore) path(name string) string {
	return filepath.Join(s.Dir, name+".md")
}

// LoadInstructions concatenates the global and project AGENT.md files.
// Missing files are simply skipped; the project file is appended last so
// its guidance takes precedence in the prompt.
func LoadInstructions(projectDir string) string {
	var parts []string
	if data, err := os.ReadFile(home.Path("AGENT.md")); err == nil {
		parts = append(parts, "# Global instructions (AGENT.md)\n\n"+string(data))
	}
	if projectDir != "" {
		if data, err := os.ReadFile(filepath.Join(projectDir, "AGENT.md")); err == nil {
			parts = append(parts, "# Project instructions (AGENT.md)\n\n"+string(data))
		}
	}
	return strings.Join(parts, "\n\n")
}
