// Package command implements user-defined slash commands: reusable prompt
// templates the user drops into a directory and invokes as "/name args".
//
// A command is a single markdown file with optional YAML frontmatter
// (description, argument-hint) followed by the prompt body — the same layout
// Claude Code uses under ~/.claude/commands. Files live in a shared
// user-level directory and a project-local one, so a team can check
// project commands into the repo while personal commands stay global.
//
// The body may reference the invocation arguments with `$ARGUMENTS` (all
// args) or `$1`, `$2`, … (positional). On invocation the placeholders are
// filled and the result is sent to the agent as an ordinary user message, so
// a command is just a saved prompt — no new tool or capability surface.
package command

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/xautjzd/agent-cli/internal/home"
)

// Command is one parsed slash command.
type Command struct {
	// Name is the invocation identifier: the file path under a root without
	// the .md suffix, with directory separators joined by ':' so
	// commands/git/commit.md becomes "git:commit".
	Name string
	// Description is a one-line summary for listings and completion.
	Description string
	// ArgumentHint documents the expected arguments (shown in help), e.g.
	// "<pr-number> [reviewer]".
	ArgumentHint string
	// Body is the prompt template after the frontmatter.
	Body string
	// Path is the source file, for diagnostics.
	Path string
	// Project is true for a project-local command, false for a user one, so
	// listings can show the origin the way Claude Code does.
	Project bool
}

// Repository provides read access to installed commands. The REPL depends on
// this interface, not on the filesystem layout (DIP).
type Repository interface {
	// List returns all commands, sorted by name (project commands shadow
	// same-named user commands).
	List() ([]Command, error)
	// Load returns one command by name.
	Load(name string) (*Command, error)
}

// root pairs a directory with whether it is the project-local one.
type root struct {
	dir     string
	project bool
}

// FSRepository reads commands from a user root and a project root. The project
// root is scanned last so a project command shadows a same-named user one.
type FSRepository struct {
	roots []root
}

// NewRepository builds a repository over the standard command locations: the
// shared user-level <agent-home>/commands and the project-local
// <project>/.agent/commands.
func NewRepository(projectDir string) *FSRepository {
	r := &FSRepository{}
	if dir := home.Path("commands"); dir != "" {
		r.roots = append(r.roots, root{dir: dir})
	}
	if projectDir != "" {
		r.roots = append(r.roots, root{dir: filepath.Join(projectDir, ".agent", "commands"), project: true})
	}
	return r
}

// List walks every root for *.md files, deriving each command's name from its
// path so nested directories namespace the command (dir/name → "dir:name").
func (r *FSRepository) List() ([]Command, error) {
	byName := map[string]Command{}
	for _, rt := range r.roots {
		_ = filepath.WalkDir(rt.dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
				return nil // missing root or non-markdown: skip, never fatal
			}
			cmd, perr := ParseFile(path)
			if perr != nil {
				return nil // a malformed command is skipped, not fatal
			}
			rel, rerr := filepath.Rel(rt.dir, path)
			if rerr != nil {
				return nil
			}
			cmd.Name = nameFromRel(rel)
			cmd.Project = rt.project
			byName[cmd.Name] = *cmd // later (project) root wins
			return nil
		})
	}
	out := make([]Command, 0, len(byName))
	for _, c := range byName {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Load finds one command by name.
func (r *FSRepository) Load(name string) (*Command, error) {
	cmds, err := r.List()
	if err != nil {
		return nil, err
	}
	for i := range cmds {
		if cmds[i].Name == name {
			return &cmds[i], nil
		}
	}
	return nil, fmt.Errorf("command %q not found", name)
}

// nameFromRel turns a root-relative path into a command name: drop the .md
// suffix and join directories with ':'.
func nameFromRel(rel string) string {
	rel = strings.TrimSuffix(rel, ".md")
	return strings.ReplaceAll(rel, string(filepath.Separator), ":")
}

// ParseFile reads and parses one command file. Name is left empty here; the
// repository fills it from the file's path so nesting can namespace it.
func ParseFile(path string) (*Command, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cmd := Parse(string(data))
	cmd.Path = path
	return cmd, nil
}

// Parse splits an optional `---` frontmatter block from the body. Unlike a
// skill, a command needs no required fields — a bare prompt file is valid — so
// this never errors; a missing description falls back to the first body line.
func Parse(content string) *Command {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	cmd := &Command{}
	body := normalized

	if strings.HasPrefix(normalized, "---\n") {
		rest := normalized[len("---\n"):]
		if end := strings.Index(rest, "\n---"); end >= 0 {
			front := rest[:end]
			body = strings.TrimPrefix(rest[end+len("\n---"):], "\n")
			for _, line := range strings.Split(front, "\n") {
				key, value, found := strings.Cut(line, ":")
				if !found || strings.HasPrefix(line, " ") {
					continue
				}
				value = strings.Trim(strings.TrimSpace(value), `"'`)
				switch strings.TrimSpace(key) {
				case "description":
					cmd.Description = value
				case "argument-hint", "argument_hint":
					cmd.ArgumentHint = value
				}
			}
		}
	}

	cmd.Body = strings.TrimSpace(body)
	if cmd.Description == "" {
		cmd.Description = firstLine(cmd.Body)
	}
	return cmd
}

// firstLine returns the first non-empty line of s, trimmed for use as a
// fallback description.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			if len(t) > 80 {
				t = t[:80]
			}
			return t
		}
	}
	return "custom command"
}

var positionalRe = regexp.MustCompile(`\$(\d+)`)

// Expand fills the argument placeholders in a command body. `$ARGUMENTS` is
// replaced with the whole argument string and `$1`, `$2`, … with the
// whitespace-split positional arguments (missing ones become empty). When the
// body references no placeholder at all and arguments were supplied, they are
// appended so a plain template still receives the user's input.
func Expand(body, args string) string {
	hadArgs := strings.Contains(body, "$ARGUMENTS")
	out := strings.ReplaceAll(body, "$ARGUMENTS", args)

	fields := strings.Fields(args)
	hadPos := false
	out = positionalRe.ReplaceAllStringFunc(out, func(m string) string {
		hadPos = true
		n := 0
		fmt.Sscanf(m, "$%d", &n)
		if n >= 1 && n <= len(fields) {
			return fields[n-1]
		}
		return ""
	})

	if !hadArgs && !hadPos && strings.TrimSpace(args) != "" {
		out = strings.TrimRight(out, "\n") + "\n\n" + args
	}
	return out
}
