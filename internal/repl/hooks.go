package repl

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/xautjzd/agent-cli/internal/agent"
	"github.com/xautjzd/agent-cli/internal/hook"
	"github.com/xautjzd/agent-cli/internal/theme"
)

// Hook integration. The REPL owns the hook.Runner and adapts it to the two
// agent-facing extension points (PreToolUse/PostToolUse via agent.ToolHooks)
// and drives the session/prompt lifecycle events itself. Hook-supplied context
// flows back into the conversation; hook system messages are shown to the user.

// basePayload builds a payload pre-filled with session-wide fields.
func (r *Repl) basePayload() hook.Payload {
	return hook.Payload{Cwd: r.WorkDir, Session: r.sessionID()}
}

// PreToolUse implements agent.ToolHooks: run PreToolUse hooks before a tool.
func (r *Repl) PreToolUse(ctx context.Context, name, args string) agent.HookOutcome {
	if !r.Hooks.Has(hook.PreToolUse) {
		return agent.HookOutcome{}
	}
	p := r.basePayload()
	p.Tool = name
	p.ToolInput = json.RawMessage(args)
	out := r.Hooks.Run(ctx, hook.PreToolUse, p)
	r.showHookMessages(out)
	return agent.HookOutcome{Block: out.Blocked, Reason: out.Reason, Context: out.Context}
}

// PostToolUse implements agent.ToolHooks: run PostToolUse hooks after a tool.
func (r *Repl) PostToolUse(ctx context.Context, name, args, result string, ok bool) agent.HookOutcome {
	if !r.Hooks.Has(hook.PostToolUse) {
		return agent.HookOutcome{}
	}
	p := r.basePayload()
	p.Tool = name
	p.ToolInput = json.RawMessage(args)
	p.Result = result
	p.OK = &ok
	out := r.Hooks.Run(ctx, hook.PostToolUse, p)
	r.showHookMessages(out)
	return agent.HookOutcome{Context: out.Context}
}

// onUserPromptSubmit runs UserPromptSubmit hooks before a prompt is sent. It
// returns the possibly-augmented input and whether the turn was blocked.
func (r *Repl) onUserPromptSubmit(ctx context.Context, input string) (string, bool) {
	if !r.Hooks.Has(hook.UserPromptSubmit) {
		return input, false
	}
	p := r.basePayload()
	p.Prompt = input
	out := r.Hooks.Run(ctx, hook.UserPromptSubmit, p)
	r.showHookMessages(out)
	if out.Blocked {
		if out.Reason != "" {
			fmt.Fprintf(r.Out, "⛔ prompt blocked by hook: %s\n", out.Reason)
		}
		return input, true
	}
	// Injected context is appended so the model sees it alongside the prompt.
	if out.Context != "" {
		input += "\n\n" + out.Context
	}
	return input, false
}

// fireLifecycle runs a fire-and-forget lifecycle event (SessionStart, Stop,
// SessionEnd, Notification). Any injected context is surfaced to the user; it
// is not fed to the model for these observational events.
func (r *Repl) fireLifecycle(ctx context.Context, event hook.Event, message string) {
	if !r.Hooks.Has(event) {
		return
	}
	p := r.basePayload()
	p.Message = message
	r.showHookMessages(r.Hooks.Run(ctx, event, p))
}

// showHookMessages prints any user-facing messages a hook emitted.
func (r *Repl) showHookMessages(out hook.Outcome) {
	th := theme.Current()
	for _, m := range out.Messages {
		fmt.Fprintf(r.Out, "%s\n", th.Paint(th.Muted, "🪝 "+m))
	}
}

// cmdHooks lists the configured hooks so the user can see what integrations
// are active.
func (r *Repl) cmdHooks(_ context.Context, _ string) error {
	hooks := r.Hooks.List()
	if len(hooks) == 0 {
		fmt.Fprintln(r.Out, "No hooks configured. Declare them under \"hooks\" in config.json.")
		return nil
	}
	fmt.Fprintln(r.Out, "Configured hooks:")
	for _, h := range hooks {
		fmt.Fprintf(r.Out, "  %s\n", h.Describe())
	}
	return nil
}
