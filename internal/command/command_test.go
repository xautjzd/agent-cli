package command

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseFrontmatterAndBody(t *testing.T) {
	c := Parse("---\ndescription: Review a PR\nargument-hint: <pr> [reviewer]\n---\nReview PR $1 assigned to $2.")
	if c.Description != "Review a PR" {
		t.Errorf("description = %q", c.Description)
	}
	if c.ArgumentHint != "<pr> [reviewer]" {
		t.Errorf("argument-hint = %q", c.ArgumentHint)
	}
	if c.Body != "Review PR $1 assigned to $2." {
		t.Errorf("body = %q", c.Body)
	}
}

func TestParseNoFrontmatterFallsBackToFirstLine(t *testing.T) {
	c := Parse("Summarize the current diff.\nBe concise.")
	if c.Body != "Summarize the current diff.\nBe concise." {
		t.Errorf("body = %q", c.Body)
	}
	// With no description, the first non-empty body line is used.
	if c.Description != "Summarize the current diff." {
		t.Errorf("fallback description = %q", c.Description)
	}
}

func TestExpandArgumentsPlaceholder(t *testing.T) {
	if got := Expand("Fix: $ARGUMENTS", "the login bug"); got != "Fix: the login bug" {
		t.Errorf("$ARGUMENTS = %q", got)
	}
}

func TestExpandPositional(t *testing.T) {
	got := Expand("PR $1 → reviewer $2 (extra $3)", "42 alice")
	if got != "PR 42 → reviewer alice (extra )" {
		t.Errorf("positional = %q", got)
	}
	// $10 must not be mangled into $1 + "0".
	if got := Expand("$10", "a b c d e f g h i j"); got != "j" {
		t.Errorf("$10 = %q, want j", got)
	}
}

func TestExpandAppendsWhenNoPlaceholder(t *testing.T) {
	// A plain template with no placeholder still receives the arguments.
	got := Expand("Explain this code.", "internal/agent")
	if got != "Explain this code.\n\ninternal/agent" {
		t.Errorf("append = %q", got)
	}
	// No arguments → body unchanged, no trailing blank lines.
	if got := Expand("Just do it.", ""); got != "Just do it." {
		t.Errorf("no-args = %q", got)
	}
}

// TestListNamespacingAndShadowing checks nested directories become "dir:name"
// and that a project command overrides a same-named user command.
func TestListNamespacingAndShadowing(t *testing.T) {
	dir := t.TempDir()
	userRoot := filepath.Join(dir, "user")
	projRoot := filepath.Join(dir, "proj")
	mustWrite(t, filepath.Join(userRoot, "review.md"), "user review")
	mustWrite(t, filepath.Join(userRoot, "git", "commit.md"), "make a commit")
	mustWrite(t, filepath.Join(projRoot, "review.md"), "project review")

	r := &FSRepository{roots: []root{{dir: userRoot}, {dir: projRoot, project: true}}}
	cmds, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 2 {
		t.Fatalf("commands = %d, want 2 (%+v)", len(cmds), cmds)
	}
	byName := map[string]Command{}
	for _, c := range cmds {
		byName[c.Name] = c
	}
	if _, ok := byName["git:commit"]; !ok {
		t.Errorf("nested command not namespaced: %v", byName)
	}
	rev, ok := byName["review"]
	if !ok || rev.Body != "project review" || !rev.Project {
		t.Errorf("project command should shadow user one: %+v", rev)
	}
}

func TestLoadNotFound(t *testing.T) {
	r := &FSRepository{roots: []root{{dir: t.TempDir()}}}
	if _, err := r.Load("nope"); err == nil {
		t.Error("expected not-found error")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
