package repl

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func planRepl(t *testing.T, stdin string) (*Repl, *stubProvider) {
	t.Helper()
	r, stub, _ := newTestRepl(t, stdin)
	withSessions(t, r)
	// The test repl starts with an empty registry; give it a fake mutating
	// and a fake read-only tool so restriction is observable.
	r.Tools.Register(&fakeTool{name: "write_file"})
	r.Tools.Register(&fakeTool{name: "grep"})
	return r, stub
}

// fakeTool is a named no-op tool that counts executions.
type fakeTool struct {
	name  string
	calls int
}

func (f *fakeTool) Name() string            { return f.name }
func (f *fakeTool) Description() string     { return "fake" }
func (f *fakeTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f *fakeTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	f.calls++
	return "ran", nil
}

// The plan prompt enforces concise, high-level, token-efficient planning:
// it carries the task, forbids code/detail, asks for minimal exploration, and
// prescribes the skimmable Goal/Approach/Changes/Steps/Verify shape.
func TestPlanPromptIsHighLevelAndConcise(t *testing.T) {
	p := planPrompt("add a --version flag")
	if !strings.Contains(p, "add a --version flag") {
		t.Error("plan prompt must carry the task")
	}
	for _, want := range []string{
		"high-level", "Explore only what you must", "do NOT write actual code",
		"Goal:", "Approach:", "Changes:", "Steps:", "Verify:",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plan prompt missing directive %q:\n%s", want, p)
		}
	}
}

func TestPlanModeRestrictsTools(t *testing.T) {
	r, stub := planRepl(t, "\n") // approval prompt answered with Enter (keep planning)

	if err := r.dispatch(context.Background(), "/plan add a login feature"); err != nil {
		t.Fatal(err)
	}
	if !r.planMode {
		t.Fatal("plan mode should be active")
	}
	// Mutating tool gone, read-only tool still there.
	if _, ok := r.Agent.Tools.Get("write_file"); ok {
		t.Error("write_file must be disabled in plan mode")
	}
	if _, ok := r.Agent.Tools.Get("grep"); !ok {
		t.Error("grep should remain available in plan mode")
	}
	// The planning turn wraps the task in plan instructions.
	last := stub.last.Messages[len(stub.last.Messages)-1]
	if !strings.Contains(last.Content, "[Plan mode]") || !strings.Contains(last.Content, "add a login feature") {
		t.Errorf("plan prompt wrong:\n%s", last.Content)
	}
	// Advertised tool defs exclude the mutating tool.
	for _, d := range stub.last.Tools {
		if d.Name == "write_file" {
			t.Error("write_file advertised to the model in plan mode")
		}
	}
}

func TestPlanApprovalImplementsWithFullTools(t *testing.T) {
	r, stub := planRepl(t, "y\n")

	if err := r.dispatch(context.Background(), "/plan refactor config"); err != nil {
		t.Fatal(err)
	}
	if r.planMode {
		t.Error("plan mode should end after approval")
	}
	if _, ok := r.Agent.Tools.Get("write_file"); !ok {
		t.Error("full tools not restored after approval")
	}
	// The implement turn was sent with full tools advertised.
	last := stub.last.Messages[len(stub.last.Messages)-1]
	if !strings.Contains(last.Content, "approved") || !strings.Contains(last.Content, "Implement") {
		t.Errorf("implement prompt wrong: %q", last.Content)
	}
	found := false
	for _, d := range stub.last.Tools {
		if d.Name == "write_file" {
			found = true
		}
	}
	if !found {
		t.Error("write_file not advertised on implement turn")
	}
}

func TestPlanQuitAndOff(t *testing.T) {
	// "q" at the approval prompt exits plan mode without implementing.
	r, stub := planRepl(t, "q\n")
	if err := r.dispatch(context.Background(), "/plan something"); err != nil {
		t.Fatal(err)
	}
	if r.planMode {
		t.Error("q should exit plan mode")
	}
	turns := len(stub.requests)
	if turns != 1 {
		t.Errorf("no implement turn expected after q, got %d turns", turns)
	}

	// /plan toggles on; /plan off toggles off.
	r2, _ := planRepl(t, "")
	r2.dispatch(context.Background(), "/plan")
	if !r2.planMode {
		t.Error("/plan should enable plan mode")
	}
	r2.dispatch(context.Background(), "/plan off")
	if r2.planMode {
		t.Error("/plan off should disable plan mode")
	}
	if _, ok := r2.Agent.Tools.Get("write_file"); !ok {
		t.Error("tools not restored by /plan off")
	}
}

func TestPlanModeRoutesOrdinaryInput(t *testing.T) {
	// Once in plan mode, plain prompts are planning turns too (Enter keeps
	// planning), until approved.
	r, stub := planRepl(t, "\ny\n")
	r.dispatch(context.Background(), "/plan")
	if err := r.runPrompt(context.Background(), "how should we add caching?"); err != nil {
		t.Fatal(err)
	}
	if !r.planMode {
		t.Fatal("Enter should keep planning")
	}
	last := stub.last.Messages[len(stub.last.Messages)-1]
	if !strings.Contains(last.Content, "[Plan mode]") {
		t.Errorf("ordinary input not wrapped in plan mode:\n%s", last.Content)
	}
	// Second turn approved with "y".
	if err := r.runPrompt(context.Background(), "refine step 2"); err != nil {
		t.Fatal(err)
	}
	if r.planMode {
		t.Error("approval should exit plan mode")
	}
}
