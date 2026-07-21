package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// skipDirs are directories never worth searching; skipping them keeps glob
// and grep fast and their output relevant.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	".idea": true, ".vscode": true, "dist": true, "build": true,
}

// Glob finds files whose path matches a shell-style pattern.
type Glob struct{ WorkDir string }

func (t *Glob) Name() string { return "glob" }

func (t *Glob) Description() string {
	return "Find files matching a glob pattern like \"**/*.go\" or \"cmd/*.md\", relative to the project root."
}

func (t *Glob) Schema() json.RawMessage {
	return mustSchema(`{
		"type": "object",
		"properties": {
			"pattern": {"type": "string", "description": "Glob pattern; ** matches any number of directories"}
		},
		"required": ["pattern"]
	}`)
}

func (t *Glob) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Pattern == "" {
		return "", fmt.Errorf("pattern must not be empty")
	}

	re, err := globToRegexp(args.Pattern)
	if err != nil {
		return "", err
	}

	var matches []string
	err = filepath.WalkDir(t.WorkDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(t.WorkDir, path)
		if err != nil {
			return nil
		}
		if re.MatchString(filepath.ToSlash(rel)) {
			matches = append(matches, rel)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "No files matched " + args.Pattern, nil
	}
	sort.Strings(matches)
	if len(matches) > 500 {
		matches = matches[:500]
		matches = append(matches, "... (truncated at 500 results)")
	}
	return strings.Join(matches, "\n"), nil
}

// globToRegexp converts a glob with ** support into an anchored regexp.
// Order matters: ** placeholders are substituted before single-star handling.
func globToRegexp(pattern string) (*regexp.Regexp, error) {
	p := regexp.QuoteMeta(filepath.ToSlash(pattern))
	p = strings.ReplaceAll(p, `\*\*/`, `(?:.*/)?`)
	p = strings.ReplaceAll(p, `\*\*`, `.*`)
	p = strings.ReplaceAll(p, `\*`, `[^/]*`)
	p = strings.ReplaceAll(p, `\?`, `[^/]`)
	return regexp.Compile("^" + p + "$")
}

// Grep searches file contents with a regular expression.
type Grep struct{ WorkDir string }

func (t *Grep) Name() string { return "grep" }

func (t *Grep) Description() string {
	return "Search file contents with a Go regular expression. Returns matching lines as path:line:text. " +
		"Optionally restrict to files matching a glob pattern."
}

func (t *Grep) Schema() json.RawMessage {
	return mustSchema(`{
		"type": "object",
		"properties": {
			"pattern": {"type": "string", "description": "Regular expression to search for"},
			"glob": {"type": "string", "description": "Optional glob to restrict which files are searched, e.g. \"**/*.go\""}
		},
		"required": ["pattern"]
	}`)
}

func (t *Grep) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Glob    string `json:"glob"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	re, err := regexp.Compile(args.Pattern)
	if err != nil {
		return "", fmt.Errorf("invalid regexp: %w", err)
	}
	var fileFilter *regexp.Regexp
	if args.Glob != "" {
		if fileFilter, err = globToRegexp(args.Glob); err != nil {
			return "", err
		}
	}

	var out []string
	const maxResults = 200
	err = filepath.WalkDir(t.WorkDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || len(out) >= maxResults {
			if len(out) >= maxResults {
				return filepath.SkipAll
			}
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(t.WorkDir, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if fileFilter != nil && !fileFilter.MatchString(rel) {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil || isBinary(data) {
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				out = append(out, fmt.Sprintf("%s:%d:%s", rel, i+1, strings.TrimSpace(line)))
				if len(out) >= maxResults {
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(out) == 0 {
		return "No matches for " + args.Pattern, nil
	}
	result := strings.Join(out, "\n")
	if len(out) >= maxResults {
		result += "\n... (truncated at 200 results)"
	}
	return result, nil
}

// isBinary uses a NUL-byte heuristic over the first 8KB, the same approach
// git uses to classify files.
func isBinary(data []byte) bool {
	n := len(data)
	if n > 8192 {
		n = 8192
	}
	for _, b := range data[:n] {
		if b == 0 {
			return true
		}
	}
	return false
}

// ListDir lists a directory, marking subdirectories with a trailing slash.
type ListDir struct{ WorkDir string }

func (t *ListDir) Name() string { return "list_dir" }

func (t *ListDir) Description() string {
	return "List the entries of a directory. Directories are suffixed with '/'."
}

func (t *ListDir) Schema() json.RawMessage {
	return mustSchema(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Directory path (default: project root)"}
		}
	}`)
}

func (t *ListDir) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
	}
	if args.Path == "" {
		args.Path = "."
	}
	path, err := resolvePath(t.WorkDir, args.Path)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", err
	}
	var lines []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		lines = append(lines, name)
	}
	if len(lines) == 0 {
		return "(empty directory)", nil
	}
	return strings.Join(lines, "\n"), nil
}
