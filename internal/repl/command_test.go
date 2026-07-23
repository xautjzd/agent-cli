package repl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	usercmd "github.com/xautjzd/agent-cli/internal/command"
)

// TestDispatchCustomCommand verifies "/greet <args>" loads a project command,
// expands its arguments, and sends the filled prompt to the agent.
func TestDispatchCustomCommand(t *testing.T) {
	r, stub, _ := newTestRepl(t, "")

	cmdDir := filepath.Join(r.WorkDir, ".agent", "commands")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\ndescription: Greet someone\n---\nWrite a short greeting to $1 in $2."
	if err := os.WriteFile(filepath.Join(cmdDir, "greet.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	r.Commands = usercmd.NewRepository(r.WorkDir)

	if err := r.dispatch(context.Background(), "/greet Alice French"); err != nil {
		t.Fatal(err)
	}

	// The expanded prompt (not the raw slash line) is what the model receives.
	sent := stub.last.Messages
	if len(sent) == 0 {
		t.Fatal("no message sent to the provider")
	}
	last := sent[len(sent)-1].Content
	if !strings.Contains(last, "Write a short greeting to Alice in French.") {
		t.Errorf("prompt not expanded: %q", last)
	}
}

// TestDispatchCustomCommandBeatsSkill checks a custom command shadows a
// same-named skill (the user authored it explicitly as a command).
func TestDispatchCustomCommandBeatsSkill(t *testing.T) {
	// newTestRepl installs a "demo" skill; add a "demo" command too.
	r, stub, _ := newTestRepl(t, "")
	cmdDir := filepath.Join(r.WorkDir, ".agent", "commands")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cmdDir, "demo.md"), []byte("COMMAND BODY $ARGUMENTS"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.Commands = usercmd.NewRepository(r.WorkDir)

	if err := r.dispatch(context.Background(), "/demo hello"); err != nil {
		t.Fatal(err)
	}
	last := stub.last.Messages[len(stub.last.Messages)-1].Content
	if !strings.Contains(last, "COMMAND BODY hello") {
		t.Errorf("custom command should win over skill: %q", last)
	}
}
