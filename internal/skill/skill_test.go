package skill

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleSkill = `---
name: commit-helper
description: Generates conventional commit messages.
---

# Commit Helper

Follow these steps.
`

func TestParse(t *testing.T) {
	s, err := Parse(sampleSkill)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.Name != "commit-helper" {
		t.Errorf("Name = %q, want commit-helper", s.Name)
	}
	if s.Description != "Generates conventional commit messages." {
		t.Errorf("Description = %q", s.Description)
	}
	if s.Body == "" || s.Body[0] != '#' {
		t.Errorf("Body should start with markdown heading, got %q", s.Body)
	}
}

func TestParseRejectsMissingFrontmatter(t *testing.T) {
	if _, err := Parse("# just markdown"); err == nil {
		t.Fatal("expected error for missing frontmatter")
	}
	if _, err := Parse("---\nname: x\n"); err == nil {
		t.Fatal("expected error for unterminated frontmatter")
	}
	if _, err := Parse("---\nname: x\n---\nbody"); err == nil {
		t.Fatal("expected error for missing description")
	}
}

func writeSkill(t *testing.T, root, dir, content string) {
	t.Helper()
	d := filepath.Join(root, dir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFSRepositoryListAndLoad(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()
	writeSkill(t, global, "alpha", "---\ndescription: global alpha\n---\nglobal body")
	writeSkill(t, project, "alpha", "---\ndescription: project alpha\n---\nproject body")
	writeSkill(t, global, "beta", "---\nname: beta\ndescription: beta skill\n---\nbeta body")

	repo := &FSRepository{Roots: []string{global, project}}
	skills, err := repo.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 2 {
		t.Fatalf("got %d skills, want 2", len(skills))
	}

	// Project root is listed later, so it must shadow the global skill.
	s, err := repo.Load("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if s.Description != "project alpha" {
		t.Errorf("project skill should shadow global, got %q", s.Description)
	}

	if _, err := repo.Load("missing"); err == nil {
		t.Error("expected error for unknown skill")
	}
}

func TestInstallerLocalDirAndCollection(t *testing.T) {
	target := t.TempDir()
	in := &Installer{TargetDir: target}

	// Single-skill directory.
	src := t.TempDir()
	writeSkill(t, src, ".", "---\nname: solo\ndescription: solo skill\n---\nbody")
	names, err := in.Install(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "solo" {
		t.Fatalf("installed = %v, want [solo]", names)
	}

	// Collection directory with two skills.
	coll := t.TempDir()
	writeSkill(t, coll, "one", "---\ndescription: first\n---\nb")
	writeSkill(t, coll, "two", "---\ndescription: second\n---\nb")
	names, err = in.Install(coll)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 {
		t.Fatalf("installed = %v, want 2 skills", names)
	}

	repo := &FSRepository{Roots: []string{target}}
	skills, err := repo.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 3 {
		t.Fatalf("got %d installed skills, want 3", len(skills))
	}

	// Remove one and verify.
	if err := in.Remove("solo"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Load("solo"); err == nil {
		t.Error("solo should be gone after Remove")
	}
	if err := in.Remove("solo"); err == nil {
		t.Error("removing a missing skill should error")
	}
}
