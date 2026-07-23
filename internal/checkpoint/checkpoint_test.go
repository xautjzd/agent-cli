package checkpoint

import (
	"os"
	"path/filepath"
	"testing"
)

// write is a tiny helper that creates or overwrites a file.
func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestRewindRestoresModifyCreateDelete covers the three file transitions a
// turn can make: an existing file overwritten, a new file created, and the
// undo of each.
func TestRewindRestoresModifyCreateDelete(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing.txt")
	created := filepath.Join(dir, "created.txt")
	write(t, existing, "original")

	m := NewManager()

	// Turn 1 modifies the existing file and creates a new one.
	m.Begin("turn one", 1 /*msgCount*/, 0 /*inputCount*/)
	m.SnapshotFile(existing)
	write(t, existing, "modified")
	m.SnapshotFile(created)
	write(t, created, "brand new")

	// Turn 2 changes them again — later snapshots must not override the
	// earlier originals when rewinding past both turns.
	m.Begin("turn two", 3, 1)
	m.SnapshotFile(existing)
	write(t, existing, "modified again")

	// Rewind to before turn 1.
	target, restored, err := m.Rewind(0)
	if err != nil {
		t.Fatal(err)
	}
	if target.Label != "turn one" || target.MsgCount != 1 || target.InputCount != 0 {
		t.Errorf("target = %+v", target)
	}
	if restored != 2 {
		t.Errorf("restored = %d, want 2", restored)
	}
	if got := read(t, existing); got != "original" {
		t.Errorf("existing restored to %q, want original", got)
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Errorf("created file should have been deleted, stat err = %v", err)
	}
	if m.Len() != 0 {
		t.Errorf("all checkpoints should be dropped, Len = %d", m.Len())
	}
}

// TestRewindKeepsEarlierCheckpoints verifies that rewinding to a later
// checkpoint leaves earlier ones intact and only undoes the later turns.
func TestRewindKeepsEarlierCheckpoints(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f.txt")
	write(t, f, "v0")

	m := NewManager()
	m.Begin("t1", 1, 0)
	m.SnapshotFile(f)
	write(t, f, "v1")

	m.Begin("t2", 3, 1)
	m.SnapshotFile(f)
	write(t, f, "v2")

	// Rewind to before t2 only: file returns to v1, t1 survives.
	target, restored, err := m.Rewind(1)
	if err != nil {
		t.Fatal(err)
	}
	if restored != 1 || target.Label != "t2" {
		t.Errorf("restored=%d target=%q", restored, target.Label)
	}
	if got := read(t, f); got != "v1" {
		t.Errorf("file = %q, want v1", got)
	}
	if m.Len() != 1 {
		t.Errorf("Len = %d, want 1 (t1 kept)", m.Len())
	}
}

// TestSnapshotFileOncePerCheckpoint ensures only the first snapshot of a path
// within a turn is kept, so the pre-turn original is what gets restored even
// after several edits in the same turn.
func TestSnapshotFileOncePerCheckpoint(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f.txt")
	write(t, f, "start")

	m := NewManager()
	m.Begin("t1", 1, 0)
	m.SnapshotFile(f)
	write(t, f, "mid")
	m.SnapshotFile(f) // second snapshot in same turn must be ignored
	write(t, f, "end")

	if _, _, err := m.Rewind(0); err != nil {
		t.Fatal(err)
	}
	if got := read(t, f); got != "start" {
		t.Errorf("file = %q, want start", got)
	}
}

// TestSnapshotWithoutCheckpointIsNoOp guards the case where an edit happens
// with no active turn: nothing is tracked and Rewind has nothing to do.
func TestSnapshotWithoutCheckpointIsNoOp(t *testing.T) {
	m := NewManager()
	m.SnapshotFile("/nonexistent/path") // must not panic
	if m.Len() != 0 {
		t.Errorf("Len = %d, want 0", m.Len())
	}
	if _, _, err := m.Rewind(0); err == nil {
		t.Error("Rewind on empty manager should error")
	}
}

// TestRewindPlanContentByState mirrors the reported 3-turn scenario
// (create=version1 → version2 → version3) and asserts the plan describes each
// restore point by the CONTENT it returns to — so a user wanting version2
// selects the checkpoint whose plan says "version2", not one labelled by a
// message. The earlier off-by-one (picking the "version2" message yielded
// version1) is exactly what this prevents.
func TestRewindPlanContentByState(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "version.txt")

	m := NewManager()
	m.Begin("create version1", 1, 0)
	m.SnapshotFile(f) // absent
	write(t, f, "version1")
	m.Begin("update to version2", 3, 1)
	m.SnapshotFile(f) // version1
	write(t, f, "version2")
	m.Begin("update to version3", 5, 2)
	m.SnapshotFile(f) // version2
	write(t, f, "version3")

	// Checkpoints (oldest first): idx0 restores→deleted, idx1→version1,
	// idx2→version2. So the state a user wants is named by the plan content.
	want := []struct {
		idx     int
		delete  bool
		content string
		turns   int
	}{
		{0, true, "", 3},
		{1, false, "version1", 2},
		{2, false, "version2", 1},
	}
	for _, w := range want {
		effects, turns, err := m.RewindPlan(w.idx)
		if err != nil {
			t.Fatal(err)
		}
		if len(effects) != 1 || turns != w.turns {
			t.Fatalf("plan(%d): effects=%d turns=%d", w.idx, len(effects), turns)
		}
		e := effects[0]
		if e.Delete != w.delete || e.Content != w.content {
			t.Errorf("plan(%d): delete=%v content=%q, want delete=%v content=%q",
				w.idx, e.Delete, e.Content, w.delete, w.content)
		}
	}

	// And actually rewinding to idx2 yields version2 on disk (not version1).
	if _, _, err := m.Rewind(2); err != nil {
		t.Fatal(err)
	}
	if got := read(t, f); got != "version2" {
		t.Errorf("after Rewind(2) file = %q, want version2", got)
	}
}

func TestListNewestBookkeeping(t *testing.T) {
	m := NewManager()
	m.Begin("a", 1, 0)
	m.Begin("b", 2, 1)
	cps := m.List()
	if len(cps) != 2 || cps[0].Label != "a" || cps[1].Label != "b" {
		t.Fatalf("List order wrong: %+v", cps)
	}
	if cps[0].ID != 1 || cps[1].ID != 2 {
		t.Errorf("IDs = %d,%d want 1,2", cps[0].ID, cps[1].ID)
	}
}
