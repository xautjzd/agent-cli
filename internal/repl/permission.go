package repl

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
	policy := r.policyOrDefault()
	decision := policy.Evaluate(name, json.RawMessage(args), r.WorkDir)

	// A clean allow with no risk passes untouched and unlogged.
	if decision.Action == permission.ActionAllow && !decision.Dangerous {
		return true, ""
	}

	// Subagents run concurrently, so several may reach the gate at once.
	// Serialize it: a HITL confirmation reads from stdin and its prompt
	// output must not interleave with another's.
	r.gateMu.Lock()
	defer r.gateMu.Unlock()

	mode := r.permMode()
	rec := permission.AuditRecord{
		Session:   r.sessionID(),
		Tool:      name,
		Args:      args,
		Reason:    decision.Reason,
		Mode:      mode,
		Rule:      decision.RuleMatched,
		Dangerous: decision.Dangerous,
		Cwd:       r.WorkDir,
		Sandboxed: r.sandboxActive(),
	}

	// A hard deny rule blocks in every mode.
	if decision.Action == permission.ActionDeny {
		fmt.Fprintf(r.Out, "\n\033[31m⛔ Denied by policy: %s — %s\033[0m\n", name, decision.Reason)
		rec.Decision, rec.Approved = permission.ActionDeny, false
		r.audit(rec)
		return false, ""
	}

	// A rule that explicitly allows a (possibly dangerous) call skips the
	// prompt but is still audited.
	if decision.Action == permission.ActionAllow {
		rec.Decision, rec.Approved = permission.ActionAllow, true
		r.audit(rec)
		return true, ""
	}

	// From here the action is Ask. In bypass mode, auto-approve with a
	// structured audit note carried into the conversation context.
	if mode == permission.ModeBypass {
		fmt.Fprintf(r.Out, "\n\033[33m⚠ bypass: auto-approved %s — %s\033[0m\n", name, decision.Reason)
		rec.Decision, rec.Approved = permission.ActionAllow, true
		r.audit(rec)
		return true, rec.Note()
	}

	// Non-interactive (one-shot/CI): there is no human to ask, so a dangerous
	// operation is denied rather than hanging. The model is told how to
	// proceed, and the decision is audited.
	if r.NonInteractive {
		fmt.Fprintf(r.Out, "\n⛔ denied (non-interactive): %s — %s\n", name, decision.Reason)
		rec.Decision, rec.Approved = permission.ActionDeny, false
		r.audit(rec)
		return false, "Error: denied — dangerous operations require approval, unavailable in " +
			"non-interactive mode. Re-run with -bypass to allow, or use a safer approach."
	}

	// HITL: show the details (and a diff preview for file edits), then prompt.
	fmt.Fprintf(r.Out, "\n\033[33m⚠ Approval required\033[0m\n  tool: %s\n  reason: %s\n",
		name, decision.Reason)
	if preview := r.editPreview(name, args); preview != "" {
		fmt.Fprint(r.Out, preview)
	} else {
		fmt.Fprintf(r.Out, "  args: %s\n", truncateArgs(args, 200))
	}
	allow := r.promptApproval(name, args, decision)
	rec.Approved = allow
	if allow {
		rec.Decision = permission.ActionAllow
	} else {
		rec.Decision = permission.ActionDeny
	}
	r.audit(rec)
	if !allow {
		return false, ""
	}
	return true, ""
}

// promptApproval asks the user to approve a call, offering to remember the
// choice for the rest of the session (per-tool, or per-command/path scope).
func (r *Repl) promptApproval(name, args string, decision permission.Decision) bool {
	answer, ok := r.readInput("Allow? [y]es / [n]o / [a]lways (this session) / [d]eny always ")
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	case "a", "always":
		r.rememberChoice(name, args, permission.ActionAllow)
		fmt.Fprintln(r.Out, "  ✓ will allow similar operations for this session")
		return true
	case "d", "deny":
		r.rememberChoice(name, args, permission.ActionDeny)
		fmt.Fprintln(r.Out, "  ⛔ will deny similar operations for this session")
		return false
	default:
		return false
	}
}

// rememberChoice adds a session-scoped rule so the same kind of operation is
// not re-prompted. For bash it scopes to the exact command's base program; for
// file tools it scopes to the tool and the file's directory.
func (r *Repl) rememberChoice(name, args string, action permission.Action) {
	rule := permission.Rule{Tool: name, Action: action}
	switch name {
	case "bash":
		var a struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal([]byte(args), &a)
		if prog := permission.BaseProgram(a.Command); prog != "" {
			// Anchor on the leading program name so "npm ..." repeats match.
			rule.Command = `^\s*` + regexpEscape(prog) + `\b`
		}
	case "write_file", "edit_file":
		var a struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal([]byte(args), &a)
		if a.Path != "" {
			rule.Path = pathGlobForDir(a.Path, r.WorkDir)
		}
	}
	_ = r.policyOrDefault().Prepend(rule)
}

// audit records a decision to the structured audit log.
func (r *Repl) audit(rec permission.AuditRecord) {
	if r.Audit != nil {
		r.Audit.Log(rec)
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
