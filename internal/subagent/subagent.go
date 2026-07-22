// Package subagent implements task delegation: the parent agent can hand an
// independent sub-task to a fresh, isolated agent (a "subagent") that runs to
// completion on its own conversation and tool set, then returns a concise
// report. This mirrors Claude Code's Task tool, opencode's and codex's
// sub-agents.
//
// Two properties make delegation useful:
//
//   - Isolation: a subagent has its own context window and message history, so
//     a large exploratory sub-task (reading many files, running searches) does
//     not bloat the parent's context — only the final report returns.
//   - Parallelism: because each delegation is one tool call, the model can
//     issue several in a single turn; the agent core executes independent tool
//     calls concurrently, so independent sub-tasks run in parallel.
//
// Design (SOLID): the Task tool depends only on a Spawner it is given (DIP);
// the Spawner owns building and running a child agent. Subagents are given a
// tool set that excludes the Task tool itself, so delegation cannot recurse
// infinitely — a subagent cannot spawn further subagents.
package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/xautjzd/agent-cli/internal/agent"
	"github.com/xautjzd/agent-cli/internal/provider"
	"github.com/xautjzd/agent-cli/internal/tool"
	"github.com/xautjzd/agent-cli/internal/usage"
)

// Definition describes one subagent type the model may delegate to. A type
// bundles a specialized system prompt with an optional tool allow-list, so a
// deployment can offer, e.g., a read-only "explorer" or a "reviewer".
type Definition struct {
	// Name is the identifier passed as subagent_type.
	Name string
	// Description tells the model when to use this subagent.
	Description string
	// Prompt is the subagent's system prompt (its role and instructions).
	Prompt string
	// Tools, when non-empty, restricts the subagent to these tool names;
	// empty means every tool the Spawner builds (minus Task).
	Tools []string
}

// DefaultDefinition is the built-in general-purpose subagent, offered when no
// custom types are configured and used as the fallback type.
var DefaultDefinition = Definition{
	Name:        "general-purpose",
	Description: "General-purpose agent for researching questions, searching code, and executing multi-step tasks autonomously. Use it to offload an independent sub-task so its intermediate work stays out of the main conversation.",
	Prompt:      "You are a subagent handling a delegated sub-task. Work autonomously to completion — you cannot ask the user questions, so make reasonable assumptions and state them. Use your tools to investigate and act. When done, reply with a concise report of what you found or changed, including the concrete details (file paths, function names, results) the calling agent needs. Do not include conversational filler.",
}

// Spawner builds and runs child agents. It is configured once at the
// composition root and shared by the Task tool.
type Spawner struct {
	// Parent, when set, is the agent whose current Provider and Model the
	// subagents inherit — so a mid-session /provider or /model switch applies
	// to delegated work too. When nil, the static Provider/Model below are
	// used.
	Parent *agent.Agent
	// Provider and Model back the child agent's completions when Parent is
	// nil; by default the same as the parent's at startup.
	Provider provider.Provider
	Model    string
	// BuildTools returns a fresh set of tools for a subagent. It MUST NOT
	// include the Task tool, or delegation could recurse without bound.
	BuildTools func() []tool.Tool
	// Definitions are the available subagent types, keyed by name. The
	// general-purpose type is always available.
	Definitions map[string]Definition
	// MaxTurns bounds each subagent's tool-use loop (0 uses the agent
	// default).
	MaxTurns int
	// NewEvents optionally supplies an events sink per subagent run (e.g. for
	// nested rendering). Nil runs the subagent silently, which keeps parallel
	// output from interleaving; only the final report is surfaced.
	NewEvents func(name string) agent.Events
	// Gate optionally screens the subagent's own tool calls. Nil lets the
	// subagent act without confirmation (it runs unattended); audit notes, if
	// any, land in its returned report.
	Gate agent.Gate
	// Usage, when set, records subagent token consumption into the shared
	// recorder so delegated work counts toward the totals.
	Usage *usage.Recorder
}

// definition resolves a requested type name, falling back to the
// general-purpose subagent for an empty or unknown name.
func (s *Spawner) definition(name string) Definition {
	if name != "" {
		if d, ok := s.Definitions[name]; ok {
			return d
		}
	}
	if d, ok := s.Definitions[DefaultDefinition.Name]; ok {
		return d
	}
	return DefaultDefinition
}

// Types returns the available subagent definitions in name order, for listing.
func (s *Spawner) Types() []Definition {
	seen := map[string]bool{}
	var out []Definition
	for _, d := range s.Definitions {
		out = append(out, d)
		seen[d.Name] = true
	}
	if !seen[DefaultDefinition.Name] {
		out = append(out, DefaultDefinition)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Run executes a delegated task on a fresh child agent and returns its final
// report. The child starts from an empty conversation with the definition's
// system prompt and a Task-free tool set.
func (s *Spawner) Run(ctx context.Context, typeName, taskPrompt string) (string, error) {
	def := s.definition(typeName)

	reg := tool.NewRegistry()
	for _, t := range s.buildToolsFor(def) {
		reg.Register(t)
	}

	var events agent.Events
	if s.NewEvents != nil {
		events = s.NewEvents(def.Name)
	}
	prov, model := s.Provider, s.Model
	if s.Parent != nil {
		prov, model = s.Parent.Provider, s.Parent.Model
	}
	child := agent.New(prov, model, reg, def.Prompt, events, s.MaxTurns)
	child.Gate = s.Gate
	child.Usage = s.Usage
	// A subagent never compacts: it is short-lived and its whole point is to
	// keep its context out of the parent, so it just runs to completion.

	out, err := child.Run(ctx, taskPrompt)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// buildToolsFor returns the tool set for a definition: all tools the Spawner
// builds, filtered to the definition's allow-list when it has one.
func (s *Spawner) buildToolsFor(def Definition) []tool.Tool {
	all := s.BuildTools()
	if len(def.Tools) == 0 {
		return all
	}
	allow := map[string]bool{}
	for _, n := range def.Tools {
		allow[n] = true
	}
	var out []tool.Tool
	for _, t := range all {
		if allow[t.Name()] {
			out = append(out, t)
		}
	}
	return out
}

// Task is the tool the parent agent calls to delegate a sub-task. One call
// spawns one subagent; the agent core runs multiple calls in one turn
// concurrently, giving parallel delegation.
type Task struct {
	Spawner *Spawner
}

func (t *Task) Name() string { return "task" }

func (t *Task) Description() string {
	var b strings.Builder
	b.WriteString("Delegate an independent sub-task to a subagent that runs autonomously on its own context and returns a report. ")
	b.WriteString("Use it to offload self-contained work (research, multi-file search, a scoped change) so its intermediate steps stay out of this conversation. ")
	b.WriteString("Issue several task calls in one turn to run independent sub-tasks in parallel. ")
	b.WriteString("The subagent cannot ask questions, so give it a complete, self-contained prompt. Available subagent types:")
	for _, d := range t.Spawner.Types() {
		fmt.Fprintf(&b, "\n- %s: %s", d.Name, d.Description)
	}
	return b.String()
}

func (t *Task) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"description": {"type": "string", "description": "A short (3-5 word) description of the sub-task"},
			"prompt": {"type": "string", "description": "The complete, self-contained instructions for the subagent"},
			"subagent_type": {"type": "string", "description": "Which subagent type to use (default general-purpose)"}
		},
		"required": ["prompt"]
	}`)
}

// Execute runs the delegated task and returns the subagent's report, wrapped
// with a header so the model can tell which delegation produced it.
func (t *Task) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Description  string `json:"description"`
		Prompt       string `json:"prompt"`
		SubagentType string `json:"subagent_type"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(args.Prompt) == "" {
		return "", fmt.Errorf("prompt must not be empty")
	}
	report, err := t.Spawner.Run(ctx, args.SubagentType, args.Prompt)
	if err != nil {
		return "", fmt.Errorf("subagent failed: %w", err)
	}
	if report == "" {
		report = "(the subagent returned no report)"
	}
	return report, nil
}
