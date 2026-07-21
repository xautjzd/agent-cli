package repl

import (
	"context"
	"strings"
	"testing"

	"github.com/xautjzd/agent-cli/internal/agent"
	"github.com/xautjzd/agent-cli/internal/provider"
	"github.com/xautjzd/agent-cli/internal/session"
)

// TestSaveResumeAlignmentAfterCompaction verifies that a compacted
// conversation (a synthetic summary turn plus a dropped front) still pairs the
// surviving user turns with the right raw display text on save, and rebuilds
// rawInputs correctly on resume — the subtle invariant compaction could break.
func TestSaveResumeAlignmentAfterCompaction(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	r.Sessions = session.NewProjectStore(r.WorkDir)

	// Simulate a post-compaction history: summary(user) + ack(assistant) then
	// two recent real turns kept verbatim.
	summary := provider.Message{Role: provider.RoleUser, Content: agent.SummaryMarker + "prior work summary"}
	ack := provider.Message{Role: provider.RoleAssistant, Content: "ok"}
	r.Agent.Restore([]provider.Message{
		summary, ack,
		{Role: provider.RoleUser, Content: "expanded keep1 with @file"},
		{Role: provider.RoleAssistant, Content: "r1"},
	})
	// rawInputs reflects the real turns known so far: two were summarized away
	// ("old1","old2"), and keep1's raw typed text is the most recent.
	r.rawInputs = []string{"old1", "old2", "keep1 typed"}

	// A fresh turn arrives: the user's next message and its answer.
	r.Agent.Restore([]provider.Message{
		summary, ack,
		{Role: provider.RoleUser, Content: "expanded keep1 with @file"},
		{Role: provider.RoleAssistant, Content: "r1"},
		{Role: provider.RoleUser, Content: "expanded keep2"},
		{Role: provider.RoleAssistant, Content: "r2"},
	})
	r.saveSession("keep2 typed")

	// Inspect the saved records: the summary turn carries no Display, and the
	// two real user turns carry their raw typed text (not the expanded wire
	// content).
	var userDisplays []string
	var summaryHadDisplay bool
	for _, rec := range r.current.Messages {
		if rec.Role != provider.RoleUser {
			continue
		}
		if agent.IsSummaryMessage(rec.Message) {
			summaryHadDisplay = rec.Display != ""
			continue
		}
		userDisplays = append(userDisplays, rec.Display)
	}
	if summaryHadDisplay {
		t.Error("summary turn must not carry a Display")
	}
	if len(userDisplays) != 2 || userDisplays[0] != "keep1 typed" || userDisplays[1] != "keep2 typed" {
		t.Fatalf("real user turns misaligned after compaction: %v", userDisplays)
	}

	// Resuming reconstructs rawInputs from real turns only (no summary entry).
	id := r.current.ID
	r2, _, _ := newTestRepl(t, "")
	r2.Sessions = session.NewProjectStore(r2.WorkDir)
	// Load into the second repl's store path by saving there first is not
	// needed: reuse the same WorkDir store.
	r2.Sessions = r.Sessions
	if err := r2.resume(id); err != nil {
		t.Fatal(err)
	}
	if len(r2.rawInputs) != 2 || r2.rawInputs[0] != "keep1 typed" || r2.rawInputs[1] != "keep2 typed" {
		t.Errorf("resume rebuilt rawInputs wrong (summary should be skipped): %v", r2.rawInputs)
	}
}

// TestCompactCommandFreesContext checks the /compact command path end to end
// through the repl: it compacts and reports, using the stub provider as the
// summarizer.
func TestCompactCommandFreesContext(t *testing.T) {
	r, _, out := newTestRepl(t, "")
	r.Agent.Restore([]provider.Message{
		{Role: provider.RoleUser, Content: "q1"}, {Role: provider.RoleAssistant, Content: "a1"},
		{Role: provider.RoleUser, Content: "q2"}, {Role: provider.RoleAssistant, Content: "a2"},
		{Role: provider.RoleUser, Content: "q3"}, {Role: provider.RoleAssistant, Content: "a3"},
		{Role: provider.RoleUser, Content: "q4"}, {Role: provider.RoleAssistant, Content: "a4"},
		{Role: provider.RoleUser, Content: "q5"}, {Role: provider.RoleAssistant, Content: "a5"},
		{Role: provider.RoleUser, Content: "q6"}, {Role: provider.RoleAssistant, Content: "a6"},
		{Role: provider.RoleUser, Content: "q7"}, {Role: provider.RoleAssistant, Content: "a7"},
	})
	before := len(r.Agent.History())
	if err := r.dispatch(context.Background(), "/compact"); err != nil {
		t.Fatal(err)
	}
	if len(r.Agent.History()) >= before {
		t.Errorf("/compact did not shrink history: %d -> %d", before, len(r.Agent.History()))
	}
	if !strings.Contains(out.String(), "Compacted") {
		t.Errorf("/compact printed no confirmation: %s", out.String())
	}
	// The first non-system message is now the summary.
	if !agent.IsSummaryMessage(r.Agent.History()[1]) {
		t.Errorf("expected a summary at index 1, got %+v", r.Agent.History()[1])
	}
}
