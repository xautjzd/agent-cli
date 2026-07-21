package repl

import (
	"context"
	"strings"
	"testing"

	"github.com/xautjzd/agent-cli/internal/provider"
)

// scriptedProvider replays a fixed sequence of text replies.
type scriptedProvider struct {
	replies  []string
	requests []provider.Request
}

func (s *scriptedProvider) Name() string { return "scripted" }
func (s *scriptedProvider) Chat(_ context.Context, req provider.Request) (*provider.Response, error) {
	s.requests = append(s.requests, req)
	reply := "working on it"
	if len(s.replies) > 0 {
		reply = s.replies[0]
		s.replies = s.replies[1:]
	}
	return &provider.Response{Message: provider.Message{Role: provider.RoleAssistant, Content: reply}}, nil
}

func TestGoalShowAndClear(t *testing.T) {
	r, _, out := newTestRepl(t, "")

	r.dispatch(context.Background(), "/goal")
	if !strings.Contains(out.String(), "No active goal") {
		t.Errorf("show without goal: %s", out.String())
	}
	r.dispatch(context.Background(), "/goal clear")
	if !strings.Contains(out.String(), "No active goal to clear") {
		t.Errorf("clear without goal: %s", out.String())
	}
}

func TestGoalSetWorksUntilAchieved(t *testing.T) {
	r, _, out := newTestRepl(t, "")
	withSessions(t, r)
	sp := &scriptedProvider{replies: []string{
		"starting work",                 // directive round
		"still going",                   // check round 1
		"GOAL_ACHIEVED tests are green", // check round 2
		"should never be sent",
	}}
	r.Agent.Provider = sp

	if err := r.dispatch(context.Background(), "/goal make the tests pass"); err != nil {
		t.Fatal(err)
	}
	// Three rounds ran, then the loop stopped.
	if len(sp.requests) != 3 {
		t.Fatalf("expected 3 provider calls, got %d", len(sp.requests))
	}
	// The first round carries the directive, later ones the goal check.
	first := sp.requests[0].Messages[len(sp.requests[0].Messages)-1].Content
	if !strings.Contains(first, "A session goal has been set") || !strings.Contains(first, "make the tests pass") {
		t.Errorf("directive prompt wrong: %q", first)
	}
	second := sp.requests[1].Messages[len(sp.requests[1].Messages)-1].Content
	if !strings.Contains(second, "Goal check") {
		t.Errorf("check prompt wrong: %q", second)
	}
	// Goal auto-cleared on achievement.
	if r.goal != "" {
		t.Errorf("goal should be cleared, still %q", r.goal)
	}
	if !strings.Contains(out.String(), "Goal achieved") {
		t.Errorf("missing achievement message: %s", out.String())
	}
}

func TestGoalRoundCap(t *testing.T) {
	r, _, out := newTestRepl(t, "")
	withSessions(t, r)
	sp := &scriptedProvider{} // never achieves
	r.Agent.Provider = sp
	r.GoalMaxRounds = 3

	if err := r.dispatch(context.Background(), "/goal impossible task"); err != nil {
		t.Fatal(err)
	}
	if len(sp.requests) != 3 {
		t.Errorf("expected cap at 3 rounds, got %d", len(sp.requests))
	}
	if r.goal != "impossible task" {
		t.Errorf("goal should stay active after cap, got %q", r.goal)
	}
	if !strings.Contains(out.String(), "still active after 3 rounds") {
		t.Errorf("missing cap message: %s", out.String())
	}

	// A later ordinary turn re-triggers goal checking.
	sp.requests = nil
	sp.replies = []string{"turn answer", "GOAL_ACHIEVED finally"}
	if err := r.runPrompt(context.Background(), "any user message"); err != nil {
		t.Fatal(err)
	}
	if len(sp.requests) != 2 {
		t.Fatalf("expected turn + 1 check, got %d", len(sp.requests))
	}
	if r.goal != "" {
		t.Error("goal should clear after re-check achievement")
	}
}

func TestGoalPersistsAndRestoresOnResume(t *testing.T) {
	r, _, out := newTestRepl(t, "")
	withSessions(t, r)
	sp := &scriptedProvider{}
	r.Agent.Provider = sp
	r.GoalMaxRounds = 1

	r.dispatch(context.Background(), "/goal finish the report")
	id := r.current.ID

	// /new drops the goal; resume restores it.
	r.dispatch(context.Background(), "/new")
	if r.goal != "" {
		t.Fatal("goal should be session-scoped")
	}
	if err := r.dispatch(context.Background(), "/resume "+id); err != nil {
		t.Fatal(err)
	}
	if r.goal != "finish the report" {
		t.Errorf("goal not restored, got %q", r.goal)
	}
	if !strings.Contains(out.String(), "Active goal restored") {
		t.Errorf("missing restore notice: %s", out.String())
	}
}
