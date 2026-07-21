package repl

import (
	"context"
	"strings"
	"testing"

	"github.com/xautjzd/agent-cli/internal/permission"
	"github.com/xautjzd/agent-cli/internal/provider"
)

// respProvider replays full scripted responses (including tool calls).
type respProvider struct {
	responses []provider.Response
	requests  []provider.Request
}

func (p *respProvider) Name() string { return "resp" }
func (p *respProvider) Chat(_ context.Context, req provider.Request) (*provider.Response, error) {
	p.requests = append(p.requests, req)
	if len(p.responses) == 0 {
		return &provider.Response{Message: provider.Message{Role: provider.RoleAssistant, Content: "done"}}, nil
	}
	resp := p.responses[0]
	p.responses = p.responses[1:]
	return &resp, nil
}

// dangerousCall is an assistant response requesting `rm -rf build`.
func dangerousCall(id string) provider.Response {
	return provider.Response{Message: provider.Message{
		Role: provider.RoleAssistant,
		ToolCalls: []provider.ToolCall{{
			ID: id, Type: "function",
			Function: provider.FunctionCall{Name: "bash", Arguments: `{"command":"rm -rf build"}`},
		}},
	}}
}

// gateRepl builds a repl whose agent uses the permission gate and a
// counting bash stand-in tool.
func gateRepl(t *testing.T, stdin string) (*Repl, *respProvider, *fakeTool) {
	t.Helper()
	r, _, _ := newTestRepl(t, stdin)
	withSessions(t, r)
	bash := &fakeTool{name: "bash"}
	r.Tools.Register(bash)
	rp := &respProvider{}
	r.Agent.Provider = rp
	r.Agent.Gate = r
	return r, rp, bash
}

func TestHITLDeniesDangerousCall(t *testing.T) {
	r, rp, bash := gateRepl(t, "n\n")
	rp.responses = []provider.Response{dangerousCall("c1")}

	if err := r.runPrompt(context.Background(), "clean the build dir"); err != nil {
		t.Fatal(err)
	}
	if bash.calls != 0 {
		t.Error("denied tool must not execute")
	}
	// The model received the denial as the tool result.
	last := rp.requests[len(rp.requests)-1].Messages
	var toolMsg *provider.Message
	for i := range last {
		if last[i].Role == provider.RoleTool && last[i].ToolCallID == "c1" {
			toolMsg = &last[i]
		}
	}
	if toolMsg == nil || !strings.Contains(toolMsg.Content, "denied") {
		t.Errorf("denial not fed back to model: %+v", toolMsg)
	}
}

func TestHITLApprovesDangerousCall(t *testing.T) {
	r, _, bash := gateRepl(t, "y\n")
	r.Agent.Provider.(*respProvider).responses = []provider.Response{dangerousCall("c1")}
	_ = r // provider already wired

	if err := r.runPrompt(context.Background(), "clean the build dir"); err != nil {
		t.Fatal(err)
	}
	if bash.calls != 1 {
		t.Errorf("approved tool should execute once, got %d", bash.calls)
	}
}

func TestBypassExecutesWithAuditInContext(t *testing.T) {
	r, rp, bash := gateRepl(t, "") // no stdin: any prompt attempt would fail
	r.dispatch(context.Background(), "/mode bypass")
	rp.responses = []provider.Response{dangerousCall("c1")}

	if err := r.runPrompt(context.Background(), "clean the build dir"); err != nil {
		t.Fatal(err)
	}
	if bash.calls != 1 {
		t.Fatal("bypass mode must execute without confirmation")
	}
	// The audit note is in the tool result fed back to the model — i.e. in
	// the conversation context and the persisted session.
	last := rp.requests[len(rp.requests)-1].Messages
	found := false
	for _, m := range last {
		if m.Role == provider.RoleTool && strings.Contains(m.Content, "[AUDIT]") &&
			strings.Contains(m.Content, "rm -rf build") && strings.Contains(m.Content, "file deletion") {
			found = true
		}
	}
	if !found {
		t.Error("audit note with key facts missing from context")
	}
	// And it is persisted in the session file.
	sess, err := r.Sessions.Load(r.current.ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted := false
	for _, rec := range sess.Messages {
		if strings.Contains(rec.Content, "[AUDIT]") {
			persisted = true
		}
	}
	if !persisted {
		t.Error("audit note not persisted in session")
	}
}

func TestGoalForcesBypass(t *testing.T) {
	r, rp, bash := gateRepl(t, "") // no stdin: HITL prompt would deny
	r.GoalMaxRounds = 2
	rp.responses = []provider.Response{
		dangerousCall("c1"),
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "GOAL_ACHIEVED build dir removed"}},
	}

	if err := r.dispatch(context.Background(), "/goal remove the build dir"); err != nil {
		t.Fatal(err)
	}
	if bash.calls != 1 {
		t.Error("goal mode must auto-approve dangerous calls (bypass)")
	}
	if r.goal != "" {
		t.Error("goal should have cleared")
	}
	// After the goal ends, the effective mode reverts to HITL.
	if r.permMode() != permission.ModeHITL {
		t.Errorf("mode after goal = %s, want hitl", r.permMode())
	}
}

func TestModeCommand(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	if r.permMode() != permission.ModeHITL {
		t.Errorf("default mode = %s, want hitl", r.permMode())
	}
	if err := r.dispatch(context.Background(), "/mode bypass"); err != nil {
		t.Fatal(err)
	}
	if r.permMode() != permission.ModeBypass {
		t.Error("/mode bypass did not switch")
	}
	if err := r.dispatch(context.Background(), "/mode hitl"); err != nil {
		t.Fatal(err)
	}
	if r.permMode() != permission.ModeHITL {
		t.Error("/mode hitl did not switch back")
	}
	if err := r.dispatch(context.Background(), "/mode nonsense"); err == nil {
		t.Error("invalid mode should error")
	}
}
