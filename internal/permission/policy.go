package permission

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Policy decides what to do with a tool call, combining explicit user rules
// (tool- and path/command-granular) with the built-in risk classifier. It is
// the single place the gate consults, so the gate stays free of policy detail
// (SRP/DIP). A Policy is safe for concurrent use — subagents share one.
//
// Precedence: explicit rules are checked in order; the first match wins. When
// no rule matches, the built-in classifier decides: dangerous → Ask, safe →
// Allow. This gives users a deny-list, an allow-list, or a mix, layered over
// sensible defaults.

// Action is the outcome of evaluating a tool call.
type Action string

const (
	// ActionAllow lets the call proceed without prompting.
	ActionAllow Action = "allow"
	// ActionAsk requires confirmation (HITL) or an audit note (bypass).
	ActionAsk Action = "ask"
	// ActionDeny blocks the call outright, in every mode.
	ActionDeny Action = "deny"
)

// Decision is the result of Policy.Evaluate.
type Decision struct {
	Action Action
	// Reason explains the decision for prompts and the audit log.
	Reason string
	// Dangerous is true when the built-in classifier flagged the call,
	// regardless of the final action (a rule may allow a dangerous call).
	Dangerous bool
	// RuleMatched names the rule that decided this, if any ("" for the
	// classifier default).
	RuleMatched string
}

// Rule is one user-defined approval rule. It matches on the tool name and,
// optionally, the bash command (regex) or file path (glob). An empty matcher
// field matches anything for that tool.
type Rule struct {
	// Tool is the tool name this rule applies to; "*" or "" matches any tool.
	Tool string `json:"tool"`
	// Command is a regular expression matched against a bash command.
	Command string `json:"command,omitempty"`
	// Path is a glob (doublestar-free; * matches within a path segment, **
	// matches across segments) matched against a file path, resolved relative
	// to the working directory.
	Path string `json:"path,omitempty"`
	// Action is allow, ask, or deny.
	Action Action `json:"action"`

	cmdRe *regexp.Regexp // compiled Command
}

// Policy holds the rule set and the bash posture.
type Policy struct {
	mu      sync.RWMutex
	posture Posture
	rules   []Rule
}

// Posture returns the current bash posture.
func (p *Policy) Posture() Posture {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.posture
}

// SetPosture switches the bash posture at runtime (e.g. from /config).
func (p *Policy) SetPosture(posture Posture) {
	if posture != PostureStrict {
		posture = PostureStandard
	}
	p.mu.Lock()
	p.posture = posture
	p.mu.Unlock()
}

// NewPolicy compiles a policy from rules. An invalid command regex makes that
// rule match nothing (it is reported by CompileErrors).
func NewPolicy(posture Posture, rules []Rule) (*Policy, []error) {
	if posture == "" {
		posture = PostureStandard
	}
	p := &Policy{posture: posture}
	var errs []error
	for _, r := range rules {
		if err := p.Add(r); err != nil {
			errs = append(errs, err)
		}
	}
	return p, errs
}

// Add appends a rule at the end of the list (lowest precedence among existing
// rules of equal specificity). Session "always allow/deny" choices are added
// here so they take effect immediately.
func (p *Policy) Add(r Rule) error {
	if r.Command != "" {
		re, err := regexp.Compile(r.Command)
		if err != nil {
			return err
		}
		r.cmdRe = re
	}
	if r.Action == "" {
		r.Action = ActionAsk
	}
	p.mu.Lock()
	p.rules = append(p.rules, r)
	p.mu.Unlock()
	return nil
}

// Prepend inserts a rule at the front (highest precedence). Used for session
// choices that must override configured rules.
func (p *Policy) Prepend(r Rule) error {
	if r.Command != "" {
		re, err := regexp.Compile(r.Command)
		if err != nil {
			return err
		}
		r.cmdRe = re
	}
	if r.Action == "" {
		r.Action = ActionAsk
	}
	p.mu.Lock()
	p.rules = append([]Rule{r}, p.rules...)
	p.mu.Unlock()
	return nil
}

// Rules returns a human-readable description of each rule, in precedence
// order, for display by /security.
func (p *Policy) Rules() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, 0, len(p.rules))
	for _, r := range p.rules {
		out = append(out, r.describe())
	}
	return out
}

// Evaluate decides what to do with a tool call.
func (p *Policy) Evaluate(toolName string, args json.RawMessage, workDir string) Decision {
	command, path := extractTargets(toolName, args)

	p.mu.RLock()
	posture := p.posture
	var matched *Rule
	for i := range p.rules {
		if p.rules[i].matches(toolName, command, path, workDir) {
			r := p.rules[i] // copy out so we don't alias the shared slice
			matched = &r
			break
		}
	}
	p.mu.RUnlock()

	if matched != nil {
		dangerous, reason := ClassifyWith(posture, toolName, args, workDir)
		if reason == "" {
			reason = "matched rule"
		}
		return Decision{Action: matched.Action, Reason: reason, Dangerous: dangerous, RuleMatched: matched.describe()}
	}

	dangerous, reason := ClassifyWith(posture, toolName, args, workDir)
	if dangerous {
		return Decision{Action: ActionAsk, Reason: reason, Dangerous: true}
	}
	return Decision{Action: ActionAllow, Reason: "no risk detected", Dangerous: false}
}

// matches reports whether a rule applies to a call.
func (r Rule) matches(toolName, command, path, workDir string) bool {
	if r.Tool != "" && r.Tool != "*" && r.Tool != toolName {
		return false
	}
	if r.cmdRe != nil {
		if command == "" || !r.cmdRe.MatchString(command) {
			return false
		}
	}
	if r.Path != "" {
		if path == "" || !matchPathGlob(r.Path, path, workDir) {
			return false
		}
	}
	return true
}

func (r Rule) describe() string {
	parts := []string{"tool=" + orStar(r.Tool)}
	if r.Command != "" {
		parts = append(parts, "cmd~/"+r.Command+"/")
	}
	if r.Path != "" {
		parts = append(parts, "path="+r.Path)
	}
	return strings.Join(parts, " ") + " → " + string(r.Action)
}

func orStar(s string) string {
	if s == "" {
		return "*"
	}
	return s
}

// extractTargets pulls the command (for bash) or path (for file tools) from a
// call's arguments, for rule matching.
func extractTargets(toolName string, args json.RawMessage) (command, path string) {
	switch toolName {
	case "bash":
		var a struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal(args, &a)
		return a.Command, ""
	case "write_file", "edit_file", "read_file":
		var a struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(args, &a)
		return "", a.Path
	}
	return "", ""
}

// matchPathGlob matches a path against a glob. The path is resolved relative
// to workDir first so a rule written as "src/**" matches both relative and
// absolute forms of the same file. "**" matches across separators; a single
// "*" matches within one segment.
func matchPathGlob(glob, path, workDir string) bool {
	rel := path
	if filepath.IsAbs(path) && workDir != "" {
		if r, err := filepath.Rel(workDir, filepath.Clean(path)); err == nil {
			rel = r
		}
	}
	rel = filepath.ToSlash(rel)
	glob = filepath.ToSlash(glob)
	return globMatch(glob, rel)
}

// globMatch implements a small glob with ** (across separators) and * (within
// a segment). It compiles the glob to a regexp.
func globMatch(glob, s string) bool {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		switch glob[i] {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				b.WriteString(".*")
				i++
				// swallow a following slash so "**/x" also matches "x"
				if i+1 < len(glob) && glob[i+1] == '/' {
					i++
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '.', '+', '(', ')', '|', '^', '$', '{', '}', '[', ']', '\\':
			b.WriteByte('\\')
			b.WriteByte(glob[i])
		default:
			b.WriteByte(glob[i])
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return false
	}
	return re.MatchString(s)
}
