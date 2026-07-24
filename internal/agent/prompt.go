package agent

import (
	"fmt"
	"strings"

	"github.com/xautjzd/agent-cli/internal/memory"
	"github.com/xautjzd/agent-cli/internal/skill"
	"github.com/xautjzd/agent-cli/internal/tool"
)

// PromptBuilder assembles the system prompt from static instructions,
// AGENT.md files, saved memories and skill metadata. Isolating this in one
// type keeps prompt policy independent of the conversation loop (SRP).
//
// The system prompt holds NO volatile data (no date). Keeping it byte-stable
// lets it stay cached indefinitely — even across a day boundary, which matters
// for providers whose prefix cache is long-lived (e.g. DeepSeek's disk cache).
// The current date is supplied separately, as a small context note inserted
// right after the system prompt at request time (see Agent.Now), so only that
// tiny note — not the whole static prefix — changes when the day rolls over.
type PromptBuilder struct {
	WorkDir string
	Skills  skill.Repository
	Memory  memory.Store
	// Tools, when set, contributes the catalog of deferred (on-demand) tools —
	// listed here by name+description only. Their full schemas are loaded at
	// call time via search_tools, keeping this prompt small and cacheable even
	// with many MCP tools connected.
	Tools *tool.Registry
}

const basePrompt = `You are an autonomous coding agent operating in a terminal.

Rules:
- Use the provided tools to inspect and modify the project; never fabricate file contents or command output.
- Prefer edit_file for small changes and write_file only for new or fully rewritten files.
- After changing code, verify it (build, tests) with the bash tool before declaring success.
- Use the language-server tools for code navigation and understanding when available: lsp_diagnostics after editing a file to catch errors/warnings early, lsp_references before changing or removing a symbol to find every use, and lsp_definition/lsp_hover to resolve a symbol's definition, type, or documentation. They are more accurate than text search; fall back to grep when no server handles the language.
- For any multi-step or non-trivial task, use the todo_write tool to plan and track it: write the todo list up front, mark exactly one item in_progress before starting it, and mark it completed the moment it is done. Skip it for trivial single-step tasks.
- When a task matches an available skill's description, call use_skill first and follow its instructions.
- Save durable, non-obvious project knowledge with the remember tool; keep memories short and factual.
- For anything time-sensitive — "latest", "recent", "current", news, versions, prices — use the current date provided in context (not your training data), and the correct current year in web_search queries.
- Be concise. Report what you did and what you verified.`

// firstLine trims a tool description to its first line, keeping the deferred
// catalog to one line per tool however verbose an MCP server's description is.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// Build produces the full system prompt.
//
// Key flow: static behavior rules come first, then user instructions
// (AGENT.md), then recalled memories, then the skill catalog. Ordering is
// deliberate — later sections carry the most session-specific context and
// models weight the extremes of the prompt most heavily.
func (b *PromptBuilder) Build() string {
	var sb strings.Builder
	sb.WriteString(basePrompt)
	fmt.Fprintf(&sb, "\n\nWorking directory: %s\n", b.WorkDir)

	if instr := memory.LoadInstructions(b.WorkDir); instr != "" {
		sb.WriteString("\n## User instructions\n\n" + instr + "\n")
	}

	if b.Memory != nil {
		if entries, err := b.Memory.List(); err == nil && len(entries) > 0 {
			sb.WriteString("\n## Project memory (saved in earlier sessions)\n\n")
			for _, e := range entries {
				fmt.Fprintf(&sb, "### %s\n%s\n\n", e.Name, e.Content)
			}
		}
	}

	if b.Skills != nil {
		if skills, err := b.Skills.List(); err == nil && len(skills) > 0 {
			sb.WriteString("\n## Available skills (load with use_skill when relevant)\n\n")
			for _, s := range skills {
				fmt.Fprintf(&sb, "- %s: %s\n", s.Name, s.Description)
			}
		}
	}

	if b.Tools != nil {
		if deferred := b.Tools.Deferred(); len(deferred) > 0 {
			sb.WriteString("\n## Deferred tools (load with search_tools before calling)\n\n")
			sb.WriteString("These tools are available but not in your active tool list. " +
				"To call one, first load its schema with search_tools (by exact name or query); " +
				"it becomes callable on your next turn.\n\n")
			for _, t := range deferred {
				fmt.Fprintf(&sb, "- %s: %s\n", t.Name(), firstLine(t.Description()))
			}
		}
	}
	return sb.String()
}
