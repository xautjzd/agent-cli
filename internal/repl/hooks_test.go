package repl

import (
	"context"
	"strings"
	"testing"

	"github.com/xautjzd/agent-cli/internal/hook"
)

func TestUserPromptSubmitInjectsContext(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	r.Hooks, _ = hook.New([]hook.Hook{{
		Event:   hook.UserPromptSubmit,
		Command: `echo '{"additionalContext":"[branch: main]"}'`,
	}})

	got, blocked := r.onUserPromptSubmit(context.Background(), "fix the bug")
	if blocked {
		t.Fatal("should not be blocked")
	}
	if !strings.Contains(got, "fix the bug") || !strings.Contains(got, "[branch: main]") {
		t.Errorf("context not appended to prompt: %q", got)
	}
}

func TestUserPromptSubmitCanBlock(t *testing.T) {
	r, _, out := newTestRepl(t, "")
	r.Hooks, _ = hook.New([]hook.Hook{{
		Event:   hook.UserPromptSubmit,
		Command: `echo '{"decision":"block","reason":"contains a secret"}'`,
	}})

	_, blocked := r.onUserPromptSubmit(context.Background(), "here is my password")
	if !blocked {
		t.Fatal("hook should block the prompt")
	}
	if !strings.Contains(out.String(), "contains a secret") {
		t.Errorf("block reason should be shown: %s", out.String())
	}
}

func TestNoHooksPromptUnchanged(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	// r.Hooks is nil — must be a safe no-op.
	got, blocked := r.onUserPromptSubmit(context.Background(), "do the thing")
	if blocked || got != "do the thing" {
		t.Errorf("nil hooks should leave the prompt unchanged: %q blocked=%v", got, blocked)
	}
}

func TestReplPreToolUseAdapterBlocks(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	r.Hooks, _ = hook.New([]hook.Hook{{
		Event:   hook.PreToolUse,
		Matcher: "^bash$",
		Command: `exit 2`,
	}})
	out := r.PreToolUse(context.Background(), "bash", `{"command":"ls"}`)
	if !out.Block {
		t.Error("PreToolUse adapter should surface a hook block")
	}
	// A non-matching tool is unaffected.
	if r.PreToolUse(context.Background(), "read_file", `{"path":"x"}`).Block {
		t.Error("non-matching tool must not be blocked")
	}
}

func TestCmdHooksLists(t *testing.T) {
	r, _, out := newTestRepl(t, "")
	r.Hooks, _ = hook.New([]hook.Hook{{Event: hook.Stop, Command: "notify-send done"}})
	if err := r.dispatch(context.Background(), "/hooks"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Stop") || !strings.Contains(out.String(), "notify-send done") {
		t.Errorf("/hooks should list configured hooks: %s", out.String())
	}
}
