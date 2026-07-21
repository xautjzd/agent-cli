package repl

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/xautjzd/agent-cli/internal/permission"
)

// BeforeToolCall implements agent.Gate: the permission layer between the
// model's tool requests and their execution.
//
// Key flow: safe calls pass untouched. Dangerous calls either pause for
// human confirmation (HITL, the default) or — in bypass mode — proceed
// immediately with an audit note carrying every key fact (tool, full
// arguments, risk reason, time, cwd). The note is prepended to the tool
// result, so it lands in the conversation context and the persisted
// session: unattended actions stay traceable afterwards.
func (r *Repl) BeforeToolCall(name, args string) (bool, string) {
	dangerous, reason := permission.Classify(name, json.RawMessage(args), r.WorkDir)
	if !dangerous {
		return true, ""
	}

	// Subagents run concurrently, so several may reach the gate at once.
	// Serialize it: a HITL confirmation reads from stdin and its prompt
	// output must not interleave with another's.
	r.gateMu.Lock()
	defer r.gateMu.Unlock()

	if r.permMode() == permission.ModeBypass {
		note := fmt.Sprintf(
			"[AUDIT] Dangerous operation auto-approved (bypass mode)\ntool: %s\nreason: %s\nargs: %s\ncwd: %s\ntime: %s",
			name, reason, args, r.WorkDir, time.Now().Format(time.RFC3339))
		fmt.Fprintf(r.Out, "\n\033[33m⚠ bypass: auto-approved %s — %s\033[0m\n", name, reason)
		return true, note
	}

	fmt.Fprintf(r.Out, "\n\033[33m⚠ Dangerous operation requested\033[0m\n  tool: %s\n  reason: %s\n  args: %s\n",
		name, reason, truncateArgs(args, 200))
	answer, ok := r.readInput("Allow this operation? [y/N] ")
	if !ok {
		return false, ""
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true, ""
	default:
		return false, ""
	}
}

// permMode resolves the effective mode: an active goal forces bypass so
// goal pursuit never stalls waiting for confirmation.
func (r *Repl) permMode() permission.Mode {
	if r.goal != "" {
		return permission.ModeBypass
	}
	if r.Mode == "" {
		return permission.ModeHITL
	}
	return r.Mode
}

// cmdMode implements /mode: show or switch the permission mode.
func (r *Repl) cmdMode(_ context.Context, args string) error {
	switch strings.TrimSpace(args) {
	case "":
		note := ""
		if r.goal != "" && r.Mode != permission.ModeBypass {
			note = " (forced to bypass while the goal is active)"
		}
		fmt.Fprintf(r.Out, "permission mode: %s%s\n", r.permMode(), note)
		fmt.Fprintln(r.Out, "  hitl   — dangerous operations require confirmation (default)")
		fmt.Fprintln(r.Out, "  bypass — no confirmations; dangerous operations are audit-logged into context")
		return nil
	case string(permission.ModeHITL), "default":
		r.Mode = permission.ModeHITL
		fmt.Fprintln(r.Out, "Permission mode: hitl — dangerous operations will ask for confirmation.")
		return nil
	case string(permission.ModeBypass):
		r.Mode = permission.ModeBypass
		fmt.Fprintln(r.Out, "Permission mode: bypass — no confirmations; dangerous operations are audit-logged.")
		return nil
	}
	return fmt.Errorf("unknown mode %q (use hitl or bypass)", args)
}

func truncateArgs(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}
