package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xautjzd/agent-cli/internal/provider"
)

func TestFileStoreRoundTrip(t *testing.T) {
	store := &FileStore{Dir: t.TempDir()}

	if metas, err := store.List(); err != nil || len(metas) != 0 {
		t.Fatalf("empty store: %v %v", metas, err)
	}

	s1 := &Session{
		Meta: Meta{ID: "20260720-100000-aaaa", Title: "first task", Provider: "deepseek", Model: "deepseek-chat", CreatedAt: time.Now()},
		Messages: []Record{
			{Message: provider.Message{Role: provider.RoleUser, Content: "hello"}, Display: "hello typed"},
			{Message: provider.Message{Role: provider.RoleAssistant, Content: "hi"}},
		},
	}
	if err := store.Save(s1); err != nil {
		t.Fatal(err)
	}
	s2 := &Session{
		Meta:     Meta{ID: "20260720-110000-bbbb", Title: "second task", CreatedAt: time.Now()},
		Messages: []Record{{Message: provider.Message{Role: provider.RoleUser, Content: "again"}}},
	}
	if err := store.Save(s2); err != nil {
		t.Fatal(err)
	}

	// Newest updated first, message counts stamped.
	metas, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 || metas[0].ID != s2.ID || metas[0].MessageCount != 1 || metas[1].MessageCount != 2 {
		t.Fatalf("List = %+v", metas)
	}

	// Full load restores messages.
	loaded, err := store.Load(s1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) != 2 || loaded.Messages[1].Content != "hi" || loaded.Title != "first task" {
		t.Fatalf("Load = %+v", loaded)
	}

	// Unique prefix resolves; ambiguous prefix errors.
	if _, err := store.Load("20260720-11"); err != nil {
		t.Errorf("prefix load failed: %v", err)
	}
	if _, err := store.Load("20260720-"); err == nil {
		t.Error("ambiguous prefix should error")
	}

	if err := store.Delete(s1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(s1.ID); err == nil {
		t.Error("deleted session should not load")
	}
}

func TestNewIDAndTitle(t *testing.T) {
	id := NewID(time.Date(2026, 7, 20, 15, 30, 12, 0, time.UTC))
	if !strings.HasPrefix(id, "20260720-153012-") || len(id) != len("20260720-153012-")+4 {
		t.Errorf("NewID = %q", id)
	}
	if TitleFrom("  multi\nline input  ") != "multi" {
		t.Errorf("TitleFrom multiline wrong")
	}
	long := strings.Repeat("x", 80)
	if got := TitleFrom(long); len(got) <= 60 {
		t.Errorf("expected truncation marker, got %q", got)
	}
}

func TestSessionsLiveOutsideTheProject(t *testing.T) {
	// History is personal; a shared repository must not accumulate one
	// developer's transcripts.
	agentHome := t.TempDir()
	t.Setenv("AGENT_HOME", agentHome)
	project := t.TempDir()

	store := NewProjectStore(project)
	if strings.HasPrefix(store.Dir, project) {
		t.Fatalf("sessions stored inside the project: %s", store.Dir)
	}
	if !strings.HasPrefix(store.Dir, agentHome) {
		t.Errorf("sessions not under the agent home: %s", store.Dir)
	}

	if err := store.Save(&Session{
		Meta:     Meta{ID: "20260720-100000-aaaa", Title: "t", CreatedAt: time.Now()},
		Messages: []Record{{Message: provider.Message{Role: provider.RoleUser, Content: "hi"}}},
	}); err != nil {
		t.Fatal(err)
	}
	// Nothing at all should appear in the working tree.
	if _, err := os.Stat(filepath.Join(project, ".agent", "sessions")); !os.IsNotExist(err) {
		t.Error("a project-level sessions directory was created")
	}
}

func TestProjectsAreIsolated(t *testing.T) {
	t.Setenv("AGENT_HOME", t.TempDir())
	a, b := t.TempDir(), t.TempDir()

	storeA := NewProjectStore(a)
	storeB := NewProjectStore(b)
	if storeA.Dir == storeB.Dir {
		t.Fatal("different projects share a session directory")
	}
	storeA.Save(&Session{Meta: Meta{ID: "20260720-100000-aaaa", Title: "in-a", CreatedAt: time.Now()}})

	// Listing one project must not surface another project's history.
	if metas, _ := storeB.List(); len(metas) != 0 {
		t.Errorf("project B sees project A's sessions: %+v", metas)
	}
	if metas, _ := storeA.List(); len(metas) != 1 {
		t.Errorf("project A lost its own session: %+v", metas)
	}
}

func TestEncodeProjectPath(t *testing.T) {
	// The encoded name keeps the origin legible.
	got := EncodeProjectPath("/Users/jane/code/my-app")
	if got != "-Users-jane-code-my-app" {
		t.Errorf("EncodeProjectPath = %q", got)
	}
	// Trailing separators and redundant elements normalize away, so the
	// same project never splits across two directories.
	if EncodeProjectPath("/a/b/") != EncodeProjectPath("/a/b") ||
		EncodeProjectPath("/a/b") != EncodeProjectPath("/a/c/../b") {
		t.Error("equivalent paths encoded differently")
	}
	if EncodeProjectPath("/") == "" {
		t.Error("root path produced an empty directory name")
	}
}

func TestLegacyProjectStoreIsMigrated(t *testing.T) {
	agentHome := t.TempDir()
	t.Setenv("AGENT_HOME", agentHome)
	project := t.TempDir()

	// Simulate history written by an older version, in the project tree.
	legacy := filepath.Join(project, ".agent", "sessions")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"id":"20260101-120000-old","title":"legacy work","messages":[]}`
	if err := os.WriteFile(filepath.Join(legacy, "20260101-120000-old.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	store := NewProjectStore(project)
	sess, err := store.Load("20260101-120000-old")
	if err != nil {
		t.Fatalf("legacy session was not migrated: %v", err)
	}
	if sess.Title != "legacy work" {
		t.Errorf("migrated session corrupted: %+v", sess.Meta)
	}
	// The old location is cleaned up once emptied.
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy directory still present: %v", err)
	}
}

func TestMigrationNeverOverwrites(t *testing.T) {
	agentHome := t.TempDir()
	t.Setenv("AGENT_HOME", agentHome)
	project := t.TempDir()

	// A session already at the new location must win over a stale legacy
	// file with the same ID.
	newDir := ProjectDir(project)
	os.MkdirAll(newDir, 0o755)
	os.WriteFile(filepath.Join(newDir, "dup.json"),
		[]byte(`{"id":"dup","title":"current","messages":[]}`), 0o644)

	legacy := filepath.Join(project, ".agent", "sessions")
	os.MkdirAll(legacy, 0o755)
	os.WriteFile(filepath.Join(legacy, "dup.json"),
		[]byte(`{"id":"dup","title":"stale","messages":[]}`), 0o644)

	store := NewProjectStore(project)
	sess, err := store.Load("dup")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Title != "current" {
		t.Errorf("migration clobbered existing history: %q", sess.Title)
	}
	// The un-migrated legacy file is left in place rather than deleted.
	if _, err := os.Stat(filepath.Join(legacy, "dup.json")); err != nil {
		t.Error("conflicting legacy file should be preserved, not discarded")
	}
}
