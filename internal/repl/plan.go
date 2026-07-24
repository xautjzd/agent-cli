package repl

import (
	"context"
	"fmt"
	"strings"

	"github.com/xautjzd/agent-cli/internal/tool"
)

// Plan mode, modeled on Claude Code: the agent explores the project and
// proposes an implementation plan without changing anything. Mutating tools
// are removed from the registry while the mode is active (a hard guarantee,
// not just prompting), and each plan turn ends with an approval prompt —
// approving restores full tools and tells the agent to implement.

// mutatingToolNames are the tools withheld from the agent in plan mode.
// Bash stays available for read-only exploration (git log, ls, …) but the
// plan instructions forbid mutating commands.
var mutatingToolNames = map[string]bool{
	"write_file": true,
	"edit_file":  true,
	"remember":   true,
	"forget":     true,
	// task is withheld too: a subagent could otherwise mutate the workspace,
	// escaping plan mode's read-only guarantee.
	"task": true,
}

// cmdPlan implements /plan: no args toggles the mode on (or reports it),
// "off" leaves it, anything else enters plan mode and starts planning that
// task immediately.
func (r *Repl) cmdPlan(ctx context.Context, args string) error {
	switch {
	case args == "off":
		if !r.planMode {
			fmt.Fprintln(r.Out, "Plan mode is not active.")
			return nil
		}
		r.exitPlanMode()
		return nil
	case args == "":
		if r.planMode {
			fmt.Fprintln(r.Out, "Plan mode is active. Type a task to plan, or /plan off to exit.")
			return nil
		}
		r.enterPlanMode()
		return nil
	default:
		if !r.planMode {
			r.enterPlanMode()
		}
		return r.runPlanTurn(ctx, args)
	}
}

// enterPlanMode swaps the agent's registry for a read-only subset.
func (r *Repl) enterPlanMode() {
	r.fullTools = r.Agent.Tools
	restricted := tool.NewRegistry()
	for _, t := range r.fullTools.All() {
		if mutatingToolNames[t.Name()] {
			continue
		}
		// Preserve deferral so MCP tools stay loaded-on-demand in plan mode too,
		// rather than re-inflating the request with every schema.
		if r.fullTools.IsDeferred(t.Name()) {
			restricted.RegisterDeferred(t)
		} else {
			restricted.Register(t)
		}
	}
	r.Agent.Tools = restricted
	r.Tools = restricted
	r.planMode = true
	fmt.Fprintln(r.Out, "Plan mode on — the agent explores read-only and proposes a concise, high-level plan (no code, no edits). Type your task; /plan off to exit.")
}

// exitPlanMode restores the full tool registry.
func (r *Repl) exitPlanMode() {
	if r.fullTools != nil {
		r.Agent.Tools = r.fullTools
		r.Tools = r.fullTools
		r.fullTools = nil
	}
	r.planMode = false
	fmt.Fprintln(r.Out, "Plan mode off.")
}

// runPlanTurn executes one planning turn and then offers the approval
// gate.
//
// Key flow: the task is wrapped in plan instructions (explore, don't
// mutate, end with a numbered plan); after the agent answers, the user
// chooses — approve (restore tools and implement), keep planning (default),
// or leave plan mode without implementing.
func (r *Repl) runPlanTurn(ctx context.Context, input string) error {
	msg, err := buildUserMessage(input, r.WorkDir, r.imagePastes, planPrompt)
	if err != nil {
		return err
	}
	if msg, err = r.prepareImageMessage(ctx, msg); err != nil {
		return err
	}
	_, err = r.Agent.RunMessage(ctx, msg)
	r.saveSession(input)
	if err != nil {
		return err
	}

	answer, ok := r.readInput("Approve plan? [y = implement · Enter = keep planning · q = exit plan mode] ")
	if !ok {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		r.exitPlanMode()
		fmt.Fprintln(r.Out, "Plan approved — implementing.")
		_, err := r.Agent.Run(ctx, "The plan above is approved. Implement it now with the available tools, following the plan and filling in the implementation detail; verify your work (build, tests) as you go.")
		r.saveSession("(plan approved — implement)")
		if err != nil {
			return err
		}
		return r.checkGoal(ctx)
	case "q":
		r.exitPlanMode()
		return nil
	default:
		return nil // stay in plan mode; the next input refines the plan
	}
}

func planPrompt(task string) string {
	return fmt.Sprintf(`[Plan mode] You are planning, not implementing. Produce a concise, high-level plan — nothing more.

Efficiency (important — do not waste tokens):
- Explore only what you must. Prefer targeted searches and reads over reading whole files; do not exhaustively survey the codebase.
- Plan, don't detail: do NOT write actual code, paste file contents, or give line-by-line detail. Describe the approach, not the implementation — the detail belongs to the implementation phase.
- Do NOT modify anything. File-mutation tools are disabled; do not run mutating shell commands (no writes, installs, deletions, git commits).

Deliver a short, skimmable plan the user can approve, in this shape (a few lines each):
- Goal: one line.
- Approach: 2–4 sentences on the overall strategy and key decisions.
- Changes: one bullet per file/component to touch — what, not how.
- Steps: a brief numbered list of the implementation order.
- Verify: how correctness will be checked (build, tests).

Task: %s`, task)
}
