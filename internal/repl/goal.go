package repl

import (
	"context"
	"fmt"
	"strings"
)

// Goal support, modeled on Claude Code's /goal: the user states a condition,
// the agent starts working toward it immediately, and after every turn a
// goal check keeps it working until the condition verifiably holds — then
// the goal auto-clears. A round cap prevents unattended runaway loops.

// achievedMarker is the token the model must emit to declare the goal met.
// It is unusual enough not to appear in ordinary output by accident.
const achievedMarker = "GOAL_ACHIEVED"

const defaultGoalRounds = 8

// cmdGoal implements /goal: no args shows the active goal, "clear" drops
// it, anything else sets it and immediately puts the agent to work.
func (r *Repl) cmdGoal(ctx context.Context, args string) error {
	switch {
	case args == "":
		if r.goal == "" {
			fmt.Fprintln(r.Out, "No active goal. Set one with /goal <text>.")
		} else {
			fmt.Fprintf(r.Out, "Active goal: %s\n", r.goal)
		}
		return nil
	case args == "clear":
		if r.goal == "" {
			fmt.Fprintln(r.Out, "No active goal to clear.")
			return nil
		}
		r.goal = ""
		r.saveGoalState()
		fmt.Fprintln(r.Out, "Goal cleared.")
		return nil
	default:
		r.goal = args
		fmt.Fprintf(r.Out, "Goal set: %s\n", args)
		return r.goalLoop(ctx, true)
	}
}

// goalLoop drives the work-check cycle. Each round is one full agentic turn
// (the model may call tools), prompted either with the initial directive or
// with a goal check. The loop ends when the model emits the achieved marker
// or the round cap is reached — in the latter case the goal stays active so
// the next user turn re-triggers checking.
//
// Key flow mirrors Claude Code's Stop hook: the agent is not allowed to
// simply stop; every stop is answered with "is the goal met? if not, keep
// going" until the condition holds.
func (r *Repl) goalLoop(ctx context.Context, fresh bool) error {
	max := r.GoalMaxRounds
	if max <= 0 {
		max = defaultGoalRounds
	}
	for round := 0; r.goal != "" && round < max; round++ {
		prompt := goalCheckPrompt(r.goal)
		if fresh && round == 0 {
			prompt = goalDirectivePrompt(r.goal)
		}
		out, err := r.Agent.Run(ctx, prompt)
		r.saveSession(prompt)
		if err != nil {
			return err
		}
		if strings.Contains(out, achievedMarker) {
			r.goal = ""
			r.saveGoalState()
			fmt.Fprintln(r.Out, "\n✓ Goal achieved — cleared.")
			return nil
		}
	}
	if r.goal != "" {
		fmt.Fprintf(r.Out,
			"\nGoal still active after %d rounds. It will be re-checked on your next message; use /goal clear to drop it.\n", max)
	}
	return nil
}

// checkGoal is called after ordinary user turns so an active goal keeps
// being pursued no matter how the conversation was advanced.
func (r *Repl) checkGoal(ctx context.Context) error {
	if r.goal == "" {
		return nil
	}
	return r.goalLoop(ctx, false)
}

// saveGoalState persists a goal change to the session file immediately (the
// regular per-turn save also carries it, but clearing between turns must
// not be lost).
func (r *Repl) saveGoalState() {
	if r.Sessions == nil || r.current == nil {
		return
	}
	r.current.Goal = r.goal
	if err := r.Sessions.Save(r.current); err != nil {
		fmt.Fprintln(r.Out, "warning: session not saved:", err)
	}
}

func goalDirectivePrompt(goal string) string {
	return fmt.Sprintf(`A session goal has been set:

%s

Treat this goal as your directive and start working toward it now with the available tools.
When — and only when — the goal is fully achieved and verified, reply with the marker %s followed by a one-line summary.
Never output %s before the goal truly holds.`, goal, achievedMarker, achievedMarker)
}

func goalCheckPrompt(goal string) string {
	return fmt.Sprintf(`Goal check. The active session goal is:

%s

If the goal is now fully achieved and verified, reply with the marker %s followed by a one-line summary.
Otherwise, continue working toward the goal now — do not stop to ask for permission.`, goal, achievedMarker)
}
