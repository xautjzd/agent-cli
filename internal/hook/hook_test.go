package hook

import (
	"context"
	"strings"
	"testing"
)

func TestNoHooksIsNoop(t *testing.T) {
	r, errs := New(nil)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	out := r.Run(context.Background(), PreToolUse, Payload{Tool: "bash"})
	if out.Blocked || out.Context != "" {
		t.Errorf("no hooks should produce an empty outcome: %+v", out)
	}
	// A nil runner is also safe.
	var nr *Runner
	if nr.Has(PreToolUse) || nr.Run(context.Background(), Stop, Payload{}).Blocked {
		t.Error("nil runner must be a safe no-op")
	}
}

func TestHookReceivesPayloadOnStdin(t *testing.T) {
	// The hook echoes back the tool name from the JSON payload as additional
	// context, proving the payload arrives on stdin.
	r, _ := New([]Hook{{
		Event:   PostToolUse,
		Command: `sed 's/.*"tool":"\([a-z_]*\)".*/tool was \1/'`,
	}})
	out := r.Run(context.Background(), PostToolUse, Payload{Tool: "edit_file"})
	if !strings.Contains(out.Context, "tool was edit_file") {
		t.Errorf("payload not delivered on stdin: %q", out.Context)
	}
}

func TestHookMatcherFiltersByTool(t *testing.T) {
	r, _ := New([]Hook{{
		Event:   PreToolUse,
		Matcher: "^bash$",
		Command: `echo matched`,
	}})
	// Matching tool → hook runs.
	if out := r.Run(context.Background(), PreToolUse, Payload{Tool: "bash"}); !strings.Contains(out.Context, "matched") {
		t.Errorf("matcher should have matched bash: %+v", out)
	}
	// Non-matching tool → hook is skipped.
	if out := r.Run(context.Background(), PreToolUse, Payload{Tool: "read_file"}); out.Context != "" {
		t.Errorf("matcher should not match read_file: %+v", out)
	}
}

func TestHookBlocksViaJSONDecision(t *testing.T) {
	r, _ := New([]Hook{{
		Event:   PreToolUse,
		Command: `echo '{"decision":"block","reason":"not allowed"}'`,
	}})
	out := r.Run(context.Background(), PreToolUse, Payload{Tool: "bash"})
	if !out.Blocked || out.Reason != "not allowed" {
		t.Errorf("JSON block decision not honored: %+v", out)
	}
}

func TestHookBlocksViaExitCode(t *testing.T) {
	r, _ := New([]Hook{{
		Event:   PreToolUse,
		Command: `echo "bad command" >&2; exit 2`,
	}})
	out := r.Run(context.Background(), PreToolUse, Payload{Tool: "bash"})
	if !out.Blocked {
		t.Fatal("non-zero exit should block")
	}
	if !strings.Contains(out.Reason, "bad command") {
		t.Errorf("stderr should become the block reason: %q", out.Reason)
	}
}

func TestHookAdditionalContextAndJSON(t *testing.T) {
	r, _ := New([]Hook{{
		Event:   PostToolUse,
		Command: `echo '{"additionalContext":"lint: 2 issues","systemMessage":"ran linter"}'`,
	}})
	out := r.Run(context.Background(), PostToolUse, Payload{Tool: "edit_file"})
	if out.Context != "lint: 2 issues" {
		t.Errorf("additionalContext wrong: %q", out.Context)
	}
	if len(out.Messages) != 1 || out.Messages[0] != "ran linter" {
		t.Errorf("systemMessage wrong: %v", out.Messages)
	}
}

func TestMultipleHooksAggregate(t *testing.T) {
	r, _ := New([]Hook{
		{Event: PostToolUse, Command: `echo first`},
		{Event: PostToolUse, Command: `echo second`},
	})
	out := r.Run(context.Background(), PostToolUse, Payload{Tool: "bash"})
	if !strings.Contains(out.Context, "first") || !strings.Contains(out.Context, "second") {
		t.Errorf("contexts should concatenate: %q", out.Context)
	}
}

func TestInvalidMatcherReported(t *testing.T) {
	_, errs := New([]Hook{{Event: PreToolUse, Matcher: "(", Command: "echo x"}})
	if len(errs) == 0 {
		t.Error("invalid matcher regex should be reported")
	}
}

func TestHasAndList(t *testing.T) {
	r, _ := New([]Hook{{Event: Stop, Command: "echo done"}})
	if !r.Has(Stop) || r.Has(PreToolUse) {
		t.Error("Has wrong")
	}
	if len(r.List()) != 1 {
		t.Error("List should return the configured hook")
	}
}

func TestPayloadTimestampAutoSet(t *testing.T) {
	// The hook checks the payload has a populated RFC3339 timestamp (starts
	// with a 4-digit year) and the event name, printing "ok" only if so.
	r, _ := New([]Hook{{
		Event:   SessionStart,
		Command: `grep -qE '"event":"SessionStart","timestamp":"[0-9]{4}' && echo ok`,
	}})
	out := r.Run(context.Background(), SessionStart, Payload{Cwd: t.TempDir()})
	if out.Context != "ok" {
		t.Errorf("payload should carry a timestamp and event: got %q", out.Context)
	}
}
