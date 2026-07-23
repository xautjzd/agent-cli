package repl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xautjzd/agent-cli/internal/checkpoint"
	"github.com/xautjzd/agent-cli/internal/session"
)

// TestRewindRestoresFilesAndConversation drives two turns, emulating a file
// edit inside each, then rewinds to before the second turn and asserts that
// both the working tree and the conversation are rolled back.
func TestRewindRestoresFilesAndConversation(t *testing.T) {
	// Selecting "1" picks the newest checkpoint (the second turn); "y"
	// confirms the file-effect preview.
	r, _, _ := newTestRepl(t, "1\ny\n")
	r.Checkpoints = checkpoint.NewManager()
	r.Sessions = &session.FileStore{Dir: filepath.Join(r.WorkDir, "sessions")}

	f := filepath.Join(r.WorkDir, "code.txt")
	os.WriteFile(f, []byte("v0"), 0o644)

	ctx := context.Background()

	// Turn 1: a checkpoint opens before the turn; the edit attributes to it
	// (SnapshotFile targets the active checkpoint).
	if err := r.runPrompt(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	r.Checkpoints.SnapshotFile(f)
	os.WriteFile(f, []byte("v1"), 0o644)

	// Turn 2.
	if err := r.runPrompt(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	r.Checkpoints.SnapshotFile(f)
	os.WriteFile(f, []byte("v2"), 0o644)

	// Sanity: two turns recorded (sys + 2×(user+assistant) = 5).
	if got := len(r.Agent.History()); got != 5 {
		t.Fatalf("history len = %d, want 5", got)
	}

	// Rewind to before the second message.
	if err := r.dispatch(ctx, "/rewind"); err != nil {
		t.Fatal(err)
	}

	// File is back to the first turn's state.
	if data, _ := os.ReadFile(f); string(data) != "v1" {
		t.Errorf("file = %q, want v1", data)
	}
	// Conversation trimmed to before turn 2: sys + user(first) + assistant.
	if got := len(r.Agent.History()); got != 3 {
		t.Errorf("history len = %d, want 3", got)
	}
	if len(r.rawInputs) != 1 || r.rawInputs[0] != "first" {
		t.Errorf("rawInputs = %v, want [first]", r.rawInputs)
	}
	// The trimmed session was re-persisted.
	if r.current != nil {
		reloaded, err := r.Sessions.Load(r.current.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(reloaded.Messages) != 2 { // user(first) + assistant, system excluded
			t.Errorf("persisted messages = %d, want 2", len(reloaded.Messages))
		}
	}
}

// TestRewindWarnsAndCancelsDeletion reproduces the reported scenario: a file
// created in one turn, overwritten in the next. Rewinding to the CREATION turn
// would delete the file; the preview must warn and, on "no", leave it intact.
func TestRewindDeletionWarningCancels(t *testing.T) {
	// "2" selects the oldest checkpoint (the creation turn); "n" declines.
	r, _, out := newTestRepl(t, "2\nn\n")
	r.Checkpoints = checkpoint.NewManager()
	r.Sessions = &session.FileStore{Dir: filepath.Join(r.WorkDir, "sessions")}

	f := filepath.Join(r.WorkDir, "version.txt")
	ctx := context.Background()

	if err := r.runPrompt(ctx, "create version.txt version1"); err != nil {
		t.Fatal(err)
	}
	r.Checkpoints.SnapshotFile(f) // captured as non-existent
	os.WriteFile(f, []byte("version1"), 0o644)

	if err := r.runPrompt(ctx, "write version2"); err != nil {
		t.Fatal(err)
	}
	r.Checkpoints.SnapshotFile(f)
	os.WriteFile(f, []byte("version2"), 0o644)

	if err := r.dispatch(ctx, "/rewind"); err != nil {
		t.Fatal(err)
	}
	// The preview must have flagged the deletion...
	if !strings.Contains(out.String(), "delete") {
		t.Errorf("expected a deletion warning, got:\n%s", out.String())
	}
	// ...and declining must leave the file untouched at its latest content.
	if data, _ := os.ReadFile(f); string(data) != "version2" {
		t.Errorf("file = %q, want version2 (rewind cancelled)", data)
	}
}

// TestRewindNoCheckpoints reports a friendly error before any turn.
func TestRewindNoCheckpoints(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	r.Checkpoints = checkpoint.NewManager()
	err := r.dispatch(context.Background(), "/rewind")
	if err == nil || !strings.Contains(err.Error(), "no checkpoints") {
		t.Errorf("expected no-checkpoints error, got %v", err)
	}
}
