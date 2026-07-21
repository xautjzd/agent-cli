package repl

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xautjzd/agent-cli/internal/catalog"
)

// candidate is one completion suggestion shown in the popup.
type candidate struct {
	// text replaces the current token when the candidate is accepted,
	// e.g. "/model" or "@internal/repl/repl.go".
	text string
	// desc is the dimmed explanation rendered next to the text.
	desc string
}

// maxCandidates bounds how many candidates are collected. The popup shows a
// scrolling window of popupRows at a time, so every command and skill stays
// reachable even when the list is long.
const maxCandidates = 50

// popupRows is the visible height of the completion popup.
const popupRows = 8

// completionSkipDirs mirrors the search tools' skip list: directories that
// would only pollute @-path suggestions.
var completionSkipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	".idea": true, ".vscode": true, "dist": true, "build": true,
}

// tokenBounds returns the [start, end) byte range of the whitespace-delimited
// token containing the cursor. With the cursor on a space, the token to its
// left is chosen so completion keeps working right after typing a token.
func tokenBounds(value string, pos int) (int, int) {
	if pos > len(value) {
		pos = len(value)
	}
	start := pos
	for start > 0 && value[start-1] != ' ' && value[start-1] != '\t' {
		start--
	}
	end := pos
	for end < len(value) && value[end] != ' ' && value[end] != '\t' {
		end++
	}
	return start, end
}

// completionsFor computes popup candidates for the text before the cursor.
//
// Key flow: only the token under the cursor matters. A leading-"/" token at
// the start of the line completes commands and skills; a token starting with
// "@" completes project file paths. Everything else yields no popup.
func (r *Repl) completionsFor(value string, pos int) []candidate {
	start, _ := tokenBounds(value, pos)
	tok := value[start:pos]

	switch {
	case start == 0 && strings.HasPrefix(tok, "/"):
		return r.commandCandidates(tok[1:])
	case strings.HasPrefix(tok, "@"):
		return r.fileCandidates(tok[1:])
	}
	// Argument position: some commands know their own valid arguments, so
	// "/provider gl<tab>" and "/model cla<tab>" complete too.
	if cands := r.argumentCandidates(value, start, pos); cands != nil {
		return cands
	}
	return nil
}

// argumentCandidates completes the argument of a slash command whose
// options are known — provider and model names. It returns nil when the
// cursor is not in such a position.
func (r *Repl) argumentCandidates(value string, start, pos int) []candidate {
	if !strings.HasPrefix(value, "/") {
		return nil
	}
	cmd, rest, found := strings.Cut(value[1:], " ")
	if !found {
		return nil
	}
	// Only complete the first argument; later words are free text.
	argStart := 1 + len(cmd) + 1
	if start != argStart {
		return nil
	}
	query := strings.ToLower(value[argStart:pos])
	_ = rest

	var options [][2]string // name, description
	switch cmd {
	case "provider":
		// User profiles first; a profile shadows a preset of the same name,
		// so the preset is skipped to avoid a duplicate suggestion.
		seen := map[string]bool{}
		for name, p := range r.Cfg.Providers {
			options = append(options, [2]string{name, "your config · " + p.BaseURL})
			seen[name] = true
		}
		for _, p := range catalog.All() {
			if seen[p.Name] {
				continue
			}
			options = append(options, [2]string{p.Name, p.Label + " · " + p.DefaultModel})
		}
	case "model":
		// Offer the configured profile's own model first — it may be newer
		// than the catalog knows — then the catalog's list.
		seen := map[string]bool{}
		if p, ok := r.Cfg.Providers[r.Cfg.Provider]; ok && p.Model != "" {
			options = append(options, [2]string{p.Model, "current profile"})
			seen[p.Model] = true
		}
		for _, m := range catalog.ModelsFor(r.Cfg.Provider) {
			if seen[m] {
				continue
			}
			options = append(options, [2]string{m, r.Cfg.Provider})
		}
	default:
		return nil
	}

	var prefix, substr []candidate
	for _, o := range options {
		c := candidate{text: o[0], desc: o[1]}
		switch {
		case strings.HasPrefix(strings.ToLower(o[0]), query):
			prefix = append(prefix, c)
		case strings.Contains(strings.ToLower(o[0]), query):
			substr = append(substr, c)
		}
	}
	sort.Slice(prefix, func(i, j int) bool { return prefix[i].text < prefix[j].text })
	sort.Slice(substr, func(i, j int) bool { return substr[i].text < substr[j].text })
	out := append(prefix, substr...)
	if len(out) > maxCandidates {
		out = out[:maxCandidates]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// commandCandidates matches built-in commands and installed skills.
// Prefix matches rank before substring matches so "mo" puts /model first.
func (r *Repl) commandCandidates(query string) []candidate {
	var prefix, substr []candidate
	add := func(name, desc string) {
		c := candidate{text: "/" + name, desc: desc}
		switch {
		case strings.HasPrefix(name, query):
			prefix = append(prefix, c)
		case strings.Contains(name, query):
			substr = append(substr, c)
		}
	}
	for _, c := range commands {
		add(c.name, c.desc)
	}
	if skills, err := r.Skills.List(); err == nil {
		for _, s := range skills {
			add(s.Name, "skill: "+s.Description)
		}
	}
	out := append(prefix, substr...)
	if len(out) > maxCandidates {
		out = out[:maxCandidates]
	}
	return out
}

// fileCandidates matches project-relative paths against the query. The file
// list is walked once per editor invocation and cached on the Repl until the
// next prompt, keeping keystroke latency flat.
func (r *Repl) fileCandidates(query string) []candidate {
	if r.fileCache == nil {
		r.fileCache = listProjectFiles(r.WorkDir)
	}
	var prefix, substr []candidate
	for _, p := range r.fileCache {
		c := candidate{text: "@" + p}
		base := filepath.Base(p)
		switch {
		case strings.HasPrefix(p, query) || strings.HasPrefix(base, query):
			prefix = append(prefix, c)
		case strings.Contains(p, query):
			substr = append(substr, c)
		}
		if len(prefix) >= popupRows {
			// Enough high-quality matches to fill the visible window.
			break
		}
	}
	out := append(prefix, substr...)
	if len(out) > maxCandidates {
		out = out[:maxCandidates]
	}
	return out
}

// listProjectFiles returns up to a few thousand project-relative file paths,
// shortest first so top-level files surface before deeply nested ones.
func listProjectFiles(workDir string) []string {
	const cap = 5000
	var files []string
	filepath.WalkDir(workDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if completionSkipDirs[d.Name()] || (d.Name() != "." && strings.HasPrefix(d.Name(), ".") && path != workDir) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(workDir, path)
		if err != nil {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		if len(files) >= cap {
			return filepath.SkipAll
		}
		return nil
	})
	sort.Slice(files, func(i, j int) bool {
		if len(files[i]) != len(files[j]) {
			return len(files[i]) < len(files[j])
		}
		return files[i] < files[j]
	})
	return files
}

// acceptCandidate replaces the token containing the cursor with the
// candidate text plus a trailing space, returning the new value and cursor
// position.
func acceptCandidate(value string, pos int, c candidate) (string, int) {
	start, end := tokenBounds(value, pos)
	sep := " "
	if end < len(value) && (value[end] == ' ' || value[end] == '\t') {
		sep = "" // the following text already provides the separator
	}
	newValue := value[:start] + c.text + sep + value[end:]
	return newValue, start + len(c.text) + len(sep)
}
