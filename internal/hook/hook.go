// Package hook implements an event-driven hook mechanism for integrating the
// agent with third-party systems, modeled on Claude Code's hooks (and the
// notify/plugin hooks of codex, opencode, and pi agent).
//
// A hook is an external command the agent runs at a defined extension point
// (a lifecycle event). The command receives a JSON payload describing the
// event on stdin and may influence the agent by printing a JSON result on
// stdout and/or by its exit code. This lets users wire the agent into linters,
// notifiers, policy engines, loggers, or any external tool without changing
// the agent's code.
//
// Design (SOLID): the agent core and REPL depend only on small interfaces and
// on this package's Runner; the Runner owns discovering matching hooks,
// executing them, and aggregating their results. Adding a new event is a pure
// extension — declare the constant and dispatch it from the relevant point.
package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Event is a lifecycle extension point at which hooks may run.
type Event string

const (
	// SessionStart fires when a session begins or is resumed.
	SessionStart Event = "SessionStart"
	// UserPromptSubmit fires before a user message is sent to the model. A
	// hook may block it or inject additional context.
	UserPromptSubmit Event = "UserPromptSubmit"
	// PreToolUse fires before a tool executes. A hook may block the call or
	// inject context.
	PreToolUse Event = "PreToolUse"
	// PostToolUse fires after a tool executes. A hook may inject context
	// (e.g. a linter's findings) appended to the tool result.
	PostToolUse Event = "PostToolUse"
	// Stop fires when the agent finishes responding to a turn.
	Stop Event = "Stop"
	// Notification fires for user-facing notifications (e.g. awaiting input).
	Notification Event = "Notification"
	// SessionEnd fires when the session ends.
	SessionEnd Event = "SessionEnd"
)

// Hook is one configured hook: a command to run at an event, optionally
// filtered by a matcher (a regular expression on the tool name for tool
// events; ignored for others).
type Hook struct {
	Event   Event
	Matcher string
	Command string
	// Timeout bounds the hook command; zero uses defaultTimeout.
	Timeout time.Duration

	re *regexp.Regexp
}

// defaultTimeout bounds a hook command so a hung hook cannot stall the agent.
const defaultTimeout = 30 * time.Second

// Payload is the JSON document delivered to a hook command on stdin.
type Payload struct {
	Event     Event           `json:"event"`
	Timestamp string          `json:"timestamp"`
	Session   string          `json:"session,omitempty"`
	Cwd       string          `json:"cwd"`
	Tool      string          `json:"tool,omitempty"`
	ToolInput json.RawMessage `json:"tool_input,omitempty"`
	Result    string          `json:"tool_result,omitempty"`
	OK        *bool           `json:"tool_ok,omitempty"`
	Prompt    string          `json:"prompt,omitempty"`
	Message   string          `json:"message,omitempty"`
}

// Result is the JSON a hook may print on stdout to influence the agent. All
// fields are optional; a hook that only observes can print nothing.
type Result struct {
	// Decision is "block" to stop the action, "allow" to explicitly permit
	// it, or "" to defer to normal handling.
	Decision string `json:"decision,omitempty"`
	// Reason explains a block, shown to the model and user.
	Reason string `json:"reason,omitempty"`
	// AdditionalContext is injected into the conversation (appended to a tool
	// result, or added to the user prompt).
	AdditionalContext string `json:"additionalContext,omitempty"`
	// SystemMessage is shown to the user only, not the model.
	SystemMessage string `json:"systemMessage,omitempty"`
}

// Outcome aggregates the results of all hooks that ran for one event.
type Outcome struct {
	// Blocked is true if any hook blocked the action.
	Blocked bool
	// Reason is the block reason (from the first blocking hook).
	Reason string
	// Context is the concatenation of every hook's additional context.
	Context string
	// Messages are user-facing system messages from hooks.
	Messages []string
}

// matches reports whether the hook applies to an event and (for tool events)
// tool name.
func (h *Hook) matches(event Event, tool string) bool {
	if h.Event != event {
		return false
	}
	if h.re == nil || tool == "" {
		return true
	}
	return h.re.MatchString(tool)
}

// Runner holds the configured hooks and dispatches events to them.
type Runner struct {
	hooks []Hook
	// Env is extra environment passed to every hook command (in addition to
	// the process environment).
	Env []string
}

// New compiles the hooks. A hook with an invalid matcher regex is dropped and
// its error returned, but the others still load.
func New(hooks []Hook) (*Runner, []error) {
	r := &Runner{}
	var errs []error
	for _, h := range hooks {
		if h.Matcher != "" {
			re, err := regexp.Compile(h.Matcher)
			if err != nil {
				errs = append(errs, fmt.Errorf("hook %s matcher %q: %w", h.Event, h.Matcher, err))
				continue
			}
			h.re = re
		}
		r.hooks = append(r.hooks, h)
	}
	return r, errs
}

// Has reports whether any hook is configured for an event — lets callers skip
// building a payload when nothing would run.
func (r *Runner) Has(event Event) bool {
	if r == nil {
		return false
	}
	for i := range r.hooks {
		if r.hooks[i].Event == event {
			return true
		}
	}
	return false
}

// List returns all configured hooks, for display by /hooks.
func (r *Runner) List() []Hook {
	if r == nil {
		return nil
	}
	out := make([]Hook, len(r.hooks))
	copy(out, r.hooks)
	return out
}

// Run dispatches an event to every matching hook and aggregates the results.
// A nil Runner is a no-op, so callers need not check for configuration.
func (r *Runner) Run(ctx context.Context, event Event, p Payload) Outcome {
	var agg Outcome
	if r == nil {
		return agg
	}
	p.Event = event
	if p.Timestamp == "" {
		p.Timestamp = time.Now().Format(time.RFC3339)
	}
	for i := range r.hooks {
		h := &r.hooks[i]
		if !h.matches(event, p.Tool) {
			continue
		}
		res := r.execute(ctx, h, p)
		if res.Decision == "block" && !agg.Blocked {
			agg.Blocked = true
			agg.Reason = res.Reason
		}
		if res.AdditionalContext != "" {
			if agg.Context != "" {
				agg.Context += "\n"
			}
			agg.Context += res.AdditionalContext
		}
		if res.SystemMessage != "" {
			agg.Messages = append(agg.Messages, res.SystemMessage)
		}
	}
	return agg
}

// execute runs one hook command, feeding the payload on stdin and interpreting
// its stdout/exit code. The contract, aligned with Claude Code:
//
//   - exit 0: success. stdout, if a JSON object, is parsed as a Result;
//     otherwise non-empty stdout is treated as AdditionalContext.
//   - non-zero exit: the action is blocked; the reason is the JSON reason if
//     present, else stderr (or stdout) text.
func (r *Runner) execute(ctx context.Context, h *Hook, p Payload) Result {
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body, _ := json.Marshal(p)
	cmd := exec.CommandContext(cctx, "sh", "-c", h.Command)
	if p.Cwd != "" {
		if info, err := os.Stat(p.Cwd); err == nil && info.IsDir() {
			cmd.Dir = p.Cwd // only chdir into a real directory
		}
	}
	cmd.Stdin = bytes.NewReader(body)
	cmd.Env = append(os.Environ(), r.Env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	out := strings.TrimSpace(stdout.String())

	res := parseResult(out)
	if err != nil {
		// Non-zero exit (or timeout) blocks the action.
		res.Decision = "block"
		if res.Reason == "" {
			res.Reason = firstNonEmpty(strings.TrimSpace(stderr.String()), out, err.Error())
		}
	}
	return res
}

// parseResult decodes a hook's stdout. A JSON object is parsed structurally;
// any other non-empty text becomes AdditionalContext (the common "just print
// some context" case).
func parseResult(out string) Result {
	if out == "" {
		return Result{}
	}
	if strings.HasPrefix(out, "{") {
		var res Result
		if err := json.Unmarshal([]byte(out), &res); err == nil {
			return res
		}
	}
	return Result{AdditionalContext: out}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Describe renders a hook for listing.
func (h Hook) Describe() string {
	m := h.Matcher
	if m == "" {
		m = "*"
	}
	return fmt.Sprintf("%-16s [%s] %s", h.Event, m, h.Command)
}
