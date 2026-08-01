package repl

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xautjzd/agent-cli/internal/agent"
	"github.com/xautjzd/agent-cli/internal/config"
	"github.com/xautjzd/agent-cli/internal/memory"
	"github.com/xautjzd/agent-cli/internal/provider"
	"github.com/xautjzd/agent-cli/internal/session"
	"github.com/xautjzd/agent-cli/internal/skill"
	"github.com/xautjzd/agent-cli/internal/tool"
	"github.com/xautjzd/agent-cli/internal/usage"
)

// stubProvider records requests and returns a fixed answer.
type stubProvider struct {
	last     provider.Request
	requests []provider.Request
}

func (s *stubProvider) Name() string { return "stub" }
func (s *stubProvider) Chat(_ context.Context, req provider.Request) (*provider.Response, error) {
	s.last = req
	s.requests = append(s.requests, req)
	return &provider.Response{
		Message: provider.Message{Role: provider.RoleAssistant, Content: "ok"},
		Usage:   provider.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}, nil
}

func TestUsageCommand(t *testing.T) {
	t.Setenv("AGENT_HOME", t.TempDir())
	r, _, out := newTestRepl(t, "")
	// Before any turn: context occupancy unknown.
	if err := r.dispatch(context.Background(), "/usage"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "unknown until the next reply") {
		t.Errorf("expected unknown context note: %s", out.String())
	}

	if err := r.runPrompt(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := r.dispatch(context.Background(), "/usage"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "This session") || !strings.Contains(got, "15 tok") || !strings.Contains(got, "context 15") {
		t.Errorf("usage output wrong:\n%s", got)
	}
}

func TestUsageCommandAggregatesAllProjects(t *testing.T) {
	t.Setenv("AGENT_HOME", t.TempDir())
	first := usage.NewRecorder(session.UsagePath("/work/project-a"))
	first.Record("openai", "gpt-5.6-sol", 100, 20, time.Second)
	second := usage.NewRecorder(session.UsagePath("/work/project-b"))
	second.Record("openai", "gpt-5.6-sol", 200, 30, 2*time.Second)
	second.Record("anthropic", "claude-opus-4-8", 50, 10, 3*time.Second)

	r, _, out := newTestRepl(t, "")
	r.printLocalUsage()
	got := stripANSI(out.String())
	for _, want := range []string{
		"Usage · this PC · all projects · all time",
		"Tokens        410",
		"Requests      3",
		"gpt-5.6-sol",
		"claude-opus-4-8",
		"openai",
		"anthropic",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("global usage missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "this project") {
		t.Errorf("usage still claims project-only scope:\n%s", got)
	}
}

func TestExpandFileRefs(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha content"), 0o644)
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("beta"), 0o644)

	// Plain text passes through untouched.
	out, err := ExpandFileRefs("no refs here", dir)
	if err != nil || out != "no refs here" {
		t.Fatalf("passthrough failed: %q %v", out, err)
	}

	// File reference appends a delimited block; mention stays in place.
	out, err = ExpandFileRefs("explain @a.txt please", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "explain @a.txt please") ||
		!strings.Contains(out, "--- Referenced file: a.txt ---") ||
		!strings.Contains(out, "alpha content") {
		t.Errorf("expansion wrong:\n%s", out)
	}

	// Trailing punctuation is trimmed; nested paths work.
	out, err = ExpandFileRefs("see @sub/b.txt.", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "beta") {
		t.Errorf("nested ref failed:\n%s", out)
	}

	// Directory reference inlines a listing.
	out, err = ExpandFileRefs("what is in @sub", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Referenced directory: sub") || !strings.Contains(out, "b.txt") {
		t.Errorf("dir ref failed:\n%s", out)
	}

	// Missing file is a hard error, not a silent pass.
	if _, err = ExpandFileRefs("read @missing.txt", dir); err == nil {
		t.Error("expected error for missing reference")
	}

	// Email-like tokens (no leading boundary) are not treated as refs.
	out, err = ExpandFileRefs("mail me at user@example.com thanks", dir)
	if err == nil && strings.Contains(out, "Referenced") {
		t.Errorf("email wrongly expanded:\n%s", out)
	}

	// A ref glued to CJK text with no space still resolves — Chinese prompts
	// rarely put a space before "@".
	out, err = ExpandFileRefs("看一下@a.txt", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "--- Referenced file: a.txt ---") || !strings.Contains(out, "alpha content") {
		t.Errorf("CJK-glued ref failed:\n%s", out)
	}
}

// newTestRepl builds a Repl over a stub provider and temp skill/memory dirs.
func newTestRepl(t *testing.T, stdin string) (*Repl, *stubProvider, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	os.MkdirAll(filepath.Join(skillsDir, "demo"), 0o755)
	os.WriteFile(filepath.Join(skillsDir, "demo", "SKILL.md"),
		[]byte("---\nname: demo\ndescription: demo skill\n---\nDo demo steps."), 0o644)

	stub := &stubProvider{}
	out := &bytes.Buffer{}
	repo := &skill.FSRepository{Roots: []string{skillsDir}}
	reg := tool.NewRegistry()
	ag := agent.New(stub, "m1", reg, "sys", nil, 5)
	r := &Repl{
		Agent:   ag,
		Cfg:     &config.Config{Provider: "stub", Model: "m1", MaxTurns: 5},
		Skills:  repo,
		Memory:  &memory.FileStore{Dir: filepath.Join(dir, "mem")},
		Tools:   reg,
		WorkDir: dir,
		In:      strings.NewReader(stdin),
		Out:     out,
	}
	r.scanner = bufio.NewScanner(r.In)
	return r, stub, out
}

func TestDispatchModelSwitch(t *testing.T) {
	r, _, out := newTestRepl(t, "")
	if err := r.dispatch(context.Background(), "/model gpt-4o"); err != nil {
		t.Fatal(err)
	}
	if r.Cfg.Model != "gpt-4o" || r.Agent.Model != "gpt-4o" {
		t.Errorf("model not switched: cfg=%s agent=%s", r.Cfg.Model, r.Agent.Model)
	}
	if !strings.Contains(out.String(), "Switched model to gpt-4o") {
		t.Errorf("missing confirmation: %s", out.String())
	}
}

func TestDispatchSkillFallback(t *testing.T) {
	// "/demo fix the tests" must invoke the demo skill with the task.
	r, stub, _ := newTestRepl(t, "")
	if err := r.dispatch(context.Background(), "/demo fix the tests"); err != nil {
		t.Fatal(err)
	}
	msgs := stub.last.Messages
	last := msgs[len(msgs)-1]
	if !strings.Contains(last.Content, "Do demo steps.") ||
		!strings.Contains(last.Content, "Task: fix the tests") {
		t.Errorf("skill message wrong:\n%s", last.Content)
	}
}

func TestDispatchUnknown(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	if err := r.dispatch(context.Background(), "/nonsense"); err == nil {
		t.Fatal("expected error for unknown command")
	}
}

func TestDispatchClearAndExit(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	r.Agent.Run(context.Background(), "hello")
	if err := r.dispatch(context.Background(), "/clear"); err != nil {
		t.Fatal(err)
	}
	if got := len(r.Agent.History()); got != 1 {
		t.Errorf("history after clear = %d, want 1 (system prompt)", got)
	}
	if err := r.dispatch(context.Background(), "/exit"); err != errExit {
		t.Errorf("exit returned %v", err)
	}
}

func TestPickerSelectsCommand(t *testing.T) {
	// Selecting entry 1 (/help) with no arguments must print the help text.
	r, _, out := newTestRepl(t, "1\n\n")
	if err := r.dispatch(context.Background(), "/"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "/provider [<name> [model]|custom|remove <name>]") {
		t.Errorf("picker did not run help:\n%s", out.String())
	}
}

func TestRunPromptExpandsRefs(t *testing.T) {
	r, stub, _ := newTestRepl(t, "")
	os.WriteFile(filepath.Join(r.WorkDir, "x.txt"), []byte("secret42"), 0o644)
	if err := r.runPrompt(context.Background(), "summarize @x.txt"); err != nil {
		t.Fatal(err)
	}
	last := stub.last.Messages[len(stub.last.Messages)-1]
	if !strings.Contains(last.Content, "secret42") {
		t.Errorf("@ref not expanded into prompt:\n%s", last.Content)
	}
}
