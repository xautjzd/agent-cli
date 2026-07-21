// Package skill implements discovery, parsing, installation and loading of
// agent skills.
//
// A skill is a directory containing a SKILL.md file with YAML frontmatter
// (name, description) followed by markdown instructions — the same layout
// used by Claude Code under ~/.claude/skills. This CLI reads the shared
// ~/.agent/skills directory so skills can be reused across agent tools.
package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xautjzd/agent-cli/internal/home"
)

// Skill is one parsed skill.
type Skill struct {
	// Name is the invocation identifier, from frontmatter or directory name.
	Name string
	// Description tells the model when the skill applies.
	Description string
	// Body is the markdown instruction content after the frontmatter.
	Body string
	// Dir is the skill's directory, so instructions can reference
	// supporting files relative to it.
	Dir string
}

// Repository provides read access to installed skills. The agent depends on
// this interface, not on the filesystem layout (DIP).
type Repository interface {
	// List returns metadata for all installed skills, sorted by name.
	List() ([]Skill, error)
	// Load returns the full skill, including its instruction body.
	Load(name string) (*Skill, error)
}

// FSRepository reads skills from one or more root directories. Later roots
// override earlier ones on name conflicts, letting a project-local skills
// directory shadow the global one.
type FSRepository struct {
	Roots []string
}

// DefaultRoots returns the standard skill locations: the shared user-level
// ~/.agent/skills plus the project-local .agent/skills.
func DefaultRoots(projectDir string) []string {
	var roots []string
	if dir := home.Path("skills"); dir != "" {
		roots = append(roots, dir)
	}
	if projectDir != "" {
		roots = append(roots, filepath.Join(projectDir, ".agent", "skills"))
	}
	return roots
}

// List scans every root for directories containing SKILL.md.
func (r *FSRepository) List() ([]Skill, error) {
	byName := map[string]Skill{}
	for _, root := range r.Roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue // a missing root is not an error, just empty
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			s, err := ParseFile(filepath.Join(root, e.Name(), "SKILL.md"))
			if err != nil {
				continue // malformed skills are skipped, not fatal
			}
			byName[s.Name] = *s
		}
	}
	out := make([]Skill, 0, len(byName))
	for _, s := range byName {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Load finds a skill by name across all roots.
func (r *FSRepository) Load(name string) (*Skill, error) {
	skills, err := r.List()
	if err != nil {
		return nil, err
	}
	for i := range skills {
		if skills[i].Name == name {
			return &skills[i], nil
		}
	}
	return nil, fmt.Errorf("skill %q not found (roots: %v)", name, r.Roots)
}

// ParseFile reads and parses one SKILL.md.
func ParseFile(path string) (*Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s, err := Parse(string(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s.Dir = filepath.Dir(path)
	if s.Name == "" {
		// Fall back to the directory name when frontmatter omits `name`.
		s.Name = filepath.Base(s.Dir)
	}
	return s, nil
}

// Parse splits SKILL.md content into frontmatter fields and body.
//
// Key flow: the file must start with a `---` line; everything until the
// closing `---` is scanned for top-level `key: value` pairs. Only `name` and
// `description` are meaningful; unknown keys are ignored for forward
// compatibility with richer skill formats.
func Parse(content string) (*Skill, error) {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(normalized, "---\n") {
		return nil, fmt.Errorf("SKILL.md must start with '---' frontmatter")
	}
	rest := normalized[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, fmt.Errorf("unterminated frontmatter")
	}
	front := rest[:end]
	body := rest[end+len("\n---"):]
	body = strings.TrimPrefix(body, "\n")

	s := &Skill{Body: strings.TrimSpace(body)}
	for _, line := range strings.Split(front, "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found || strings.HasPrefix(line, " ") {
			continue // skip nested or non key:value lines
		}
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		switch strings.TrimSpace(key) {
		case "name":
			s.Name = value
		case "description":
			s.Description = value
		}
	}
	if s.Description == "" {
		return nil, fmt.Errorf("frontmatter must include a description")
	}
	return s, nil
}
