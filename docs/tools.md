# Built-in tools

The agent acts through tools. Built-in tools are always advertised; MCP tools are
loaded on demand (see [MCP](mcp.md)). This guide is the tool catalog plus two
features worth their own section: **whitespace-tolerant editing** and **subagent
delegation**.

## Catalog

| Tool | Purpose |
|---|---|
| `bash` | Run shell commands (builds, tests, git) with timeout and output truncation |
| `read_file` | Read files with line numbers, offset/limit windowing |
| `write_file` | Create/overwrite files, auto-creating parent directories |
| `edit_file` | Context-anchored, whitespace-tolerant replacement; rejects ambiguous matches |
| `glob` | Find files by pattern (`**/*.go`), skipping `.git`, `node_modules`, etc. |
| `grep` | Regex content search returning `path:line:text` |
| `list_dir` | List directory entries |
| `use_skill` | Load an installed skill's instructions on demand |
| `remember` / `forget` | Save/delete project-scoped memories |
| `todo_write` | Maintain a structured todo list ([details](plan-goals-and-compaction.md#task-tracking-todo_write--todos)) |
| `task` | Delegate an independent sub-task to a subagent (see below) |
| `web_search` | Search the web for current docs, APIs, versions, errors ([details](web-tools.md)) |
| `web_fetch` | Fetch a URL as Markdown; optional `prompt` returns only the relevant part ([details](web-tools.md)) |
| `lsp_diagnostics` | Compiler/linter errors & warnings for a file ([details](lsp.md)) |
| `lsp_references` | Find every reference to a symbol (scope-aware) |
| `lsp_definition` | Jump to where a symbol is defined |
| `lsp_hover` | A symbol's type signature and documentation |
| `search_tools` | Load deferred MCP tool schemas on demand ([details](mcp.md)) |

Run **`/tools`** in-session to list what's currently available (including MCP and
LSP tools).

## Whitespace-tolerant editing

Exact string replacement is brittle — one differing space, tab, or trailing
character makes an edit fail even when the target is obvious. `edit_file` uses
**context-anchored, fuzzy matching** (the approach behind Aider's SEARCH/REPLACE
blocks and Claude Code's edit tool), tried in three tiers:

1. **exact** — byte-for-byte substring (fastest, zero ambiguity).
2. **line-trim** — whole-line match after trimming each line's leading/trailing
   whitespace; the replacement is **re-indented** to the target's actual indent.
3. **ws-collapse** — whole-line match after collapsing internal whitespace runs;
   also re-indented. The most forgiving tier.

So the model can send a block at the wrong indent (or with a stray trailing space)
and the edit still lands, reflowed to fit. Safety is preserved:

- An **ambiguous** match (more than one location) is rejected unless `replace_all`
  is set, so an edit never silently hits the wrong place.
- When nothing matches, the error **points at the closest region** ("closest region
  starts near line N, X% of lines similar") so the model can self-correct.
- A report notes when a fuzzy tier (not exact) was used, so the applied change can
  be double-checked against the returned diff.

## Task delegation & parallel subagents

The `task` tool hands an independent sub-task to a **subagent** — a fresh, isolated
agent with its own context window and tool set — that runs autonomously and returns
a concise report (modeled on Claude Code's Task tool):

- **Isolation** — the subagent's intermediate work (reading many files, searches,
  trial runs) stays in *its* context; only the final report returns, keeping the
  parent's context lean.
- **Parallelism** — each delegation is one tool call, so the model can issue
  several in a turn. The agent core **executes independent tool calls
  concurrently** (gating and result ordering stay deterministic).
- **Bounded depth** — subagents are built with a Task-free tool set, so a subagent
  cannot spawn further subagents. Delegation depth is exactly one.
- **Safety** — subagent tool calls pass through the same permission gate as the
  parent (serialized so concurrent subagents can't interleave prompts); in plan
  mode `task` is withheld.

Subagents inherit the parent's current provider/model, so a mid-session `/provider`
or `/model` switch applies to delegated work too.

### Usage

Run **`/agents`** to see the available subagent types. The built-in
`general-purpose` type is always present.

### Configuration

Define your own subagents under `subagents`:

```jsonc
{
  "subagents": {
    "reviewer": {
      "description": "Reviews a diff or file for bugs and style issues",
      "prompt": "You are a meticulous code reviewer. Report concrete issues with file:line refs.",
      "tools": ["read_file", "grep", "glob"]   // optional allow-list; omit for all tools
    }
  }
}
```

| Field | Meaning |
|---|---|
| `description` | Shown in `/agents`; helps the model pick the right type |
| `prompt` | The subagent's system prompt |
| `tools` | Optional allow-list of tool names (omit for all base tools) |
