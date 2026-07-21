package skill

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Installer copies skills into a target skills directory.
type Installer struct {
	// TargetDir is where skills are installed, normally ~/.agent/skills.
	TargetDir string
}

// Install adds a skill from a local directory or a git URL.
//
// Key flow: git sources are cloned to a temp directory first; then the
// source is validated (it must contain SKILL.md, or be a collection of
// skill subdirectories) and each discovered skill is copied into TargetDir
// under its own name.
func (in *Installer) Install(source string) ([]string, error) {
	dir := source
	if isGitSource(source) {
		tmp, err := os.MkdirTemp("", "agent-skill-*")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(tmp)
		cmd := exec.Command("git", "clone", "--depth", "1", source, tmp)
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git clone failed: %v\n%s", err, out)
		}
		dir = tmp
	}

	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("source not found: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("source must be a directory or git URL")
	}

	skillDirs, err := findSkillDirs(dir)
	if err != nil {
		return nil, err
	}
	if len(skillDirs) == 0 {
		return nil, fmt.Errorf("no SKILL.md found under %s", dir)
	}

	var installed []string
	for _, sd := range skillDirs {
		s, err := ParseFile(filepath.Join(sd, "SKILL.md"))
		if err != nil {
			return nil, err
		}
		dest := filepath.Join(in.TargetDir, s.Name)
		if err := copyDir(sd, dest); err != nil {
			return nil, fmt.Errorf("install %s: %w", s.Name, err)
		}
		installed = append(installed, s.Name)
	}
	return installed, nil
}

// Remove deletes an installed skill by name.
func (in *Installer) Remove(name string) error {
	dest := filepath.Join(in.TargetDir, name)
	if _, err := os.Stat(filepath.Join(dest, "SKILL.md")); err != nil {
		return fmt.Errorf("skill %q is not installed in %s", name, in.TargetDir)
	}
	return os.RemoveAll(dest)
}

func isGitSource(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "git@") || strings.HasSuffix(s, ".git")
}

// findSkillDirs returns dir itself if it is a skill, otherwise its immediate
// subdirectories that contain SKILL.md (a skill collection repo).
func findSkillDirs(dir string) ([]string, error) {
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err == nil {
		return []string{dir}, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		if _, err := os.Stat(filepath.Join(sub, "SKILL.md")); err == nil {
			out = append(out, sub)
		}
	}
	return out, nil
}

// copyDir recursively copies src into dest, replacing dest if it exists so
// installs are idempotent upgrades.
func copyDir(src, dest string) error {
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == ".git" && d.IsDir() {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
