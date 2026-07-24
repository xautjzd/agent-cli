package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/xautjzd/agent-cli/internal/home"
)

// ProjectsRoot is the directory holding every project's per-project data
// (sessions, usage, audit log), keyed by encoded project path.
func ProjectsRoot() string { return home.Path("projects") }

// ScanAllMeta reads session metadata across *all* projects, newest update
// first. It powers the cross-project stats view. A missing root, unreadable
// project, or corrupt session file is skipped rather than failing the scan —
// partial history is more useful than none.
func ScanAllMeta() ([]Meta, error) {
	root := ProjectsRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Meta
	for _, proj := range entries {
		if !proj.IsDir() {
			continue
		}
		out = append(out, scanSessionsDir(filepath.Join(root, proj.Name(), "sessions"))...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

// scanSessionsDir reads just the Meta of each session file in dir. The full
// message array is ignored: Meta fields sit at the top level of the session
// JSON, so decoding into Meta alone is both correct and cheap.
func scanSessionsDir(dir string) []Meta {
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var metas []Meta
	for _, f := range files {
		if f.IsDir() || filepath.Ext(f.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			continue
		}
		var m Meta
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		metas = append(metas, m)
	}
	return metas
}

// AllUsagePaths returns the usage.json path for every project that has one, so
// callers can total token consumption across projects.
func AllUsagePaths() []string {
	root := ProjectsRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var paths []string
	for _, proj := range entries {
		if !proj.IsDir() {
			continue
		}
		p := filepath.Join(root, proj.Name(), "usage.json")
		if _, err := os.Stat(p); err == nil {
			paths = append(paths, p)
		}
	}
	return paths
}
