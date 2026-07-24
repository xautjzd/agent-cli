# agent-cli

A Claude Code–style coding agent CLI written in Go. It runs an agentic tool-use loop
against the **Anthropic Messages API** (via the official Go SDK) or any
**OpenAI-compatible** provider (OpenAI, DeepSeek, GLM, Kimi, Qwen, Ollama, or a
custom endpoint), with built-in file/shell tools, code navigation, skills, and
project-scoped memory.

```bash
go install github.com/xautjzd/agent-cli/cmd/agent@latest
export DEEPSEEK_API_KEY=sk-...     # or ANTHROPIC_API_KEY / OPENAI_API_KEY / …
cd my-project && agent
```

## Features

- **Agentic loop** — the model plans, calls tools, reads results, and iterates until done.
- **Full-screen TUI** — a scrolling conversation viewport with a bottom-pinned input box,
  live `/`-command and `@`-file completion, streaming output, and clean terminal resizing.
- **Built-in tools** — `bash`, `read_file`, `write_file`, `edit_file` (whitespace-tolerant),
  `glob`, `grep`, `list_dir`, `todo_write`, `web_search`, `web_fetch`, `task`, and more.
- **Code navigation (LSP)** — `lsp_diagnostics`, `lsp_references`, `lsp_definition`,
  `lsp_hover` backed by language servers (gopls, tsserver, pyright, rust-analyzer, clangd).
- **Multi-provider** — Anthropic and OpenAI-compatible behind one interface, with
  zero-config presets for common vendors; switch mid-session with `/provider`.
- **Permissions & sandbox** — an evasion-resistant risk classifier, per-tool/path/command
  approval rules, an optional command sandbox, and a structured audit log.
- **Sessions, plan mode & goals** — auto-saved resumable history, read-only planning,
  goal-directed autonomy, undo via `/rewind`, and automatic context compaction.
- **Extensible** — `SKILL.md` skills, custom slash commands, project memory, MCP servers,
  and lifecycle hooks for third-party integration.
- **Automation-ready** — a non-interactive `-p` mode for CI and PR review, plus token &
  cost tracking with live [models.dev](https://models.dev) pricing.

## Installation

```bash
go install github.com/xautjzd/agent-cli/cmd/agent@latest
# or from a checkout:
go build -o agent ./cmd/agent
```

Requires **Go 1.22+**. The result is a single static binary.

## Quick start

```bash
# 1. Configure a provider (pick one)
export DEEPSEEK_API_KEY=sk-...                                   # DeepSeek (default)
export AGENT_PROVIDER=anthropic ANTHROPIC_API_KEY=sk-ant-...     # or Anthropic
export AGENT_PROVIDER=openai   OPENAI_API_KEY=sk-...             # or OpenAI

# 2. Run interactively in your project directory
cd my-project
agent

# 3. Or run a single prompt
agent -p "explain the structure of this repository"
```

Inside the session, type `/` to see every command, `@path` to inline a file, and
`/help` for an overview. Full walkthrough: **[docs/getting-started.md](docs/getting-started.md)**.

## Documentation

Detailed usage and configuration guides live in **[`docs/`](docs/README.md)**.

| Guide | What it covers |
|---|---|
| [Getting started](docs/getting-started.md) | Install, first run, quick start |
| [Configuration](docs/configuration.md) | Precedence, files, every config key, `agent config`, the `/config` panel |
| [Providers & models](docs/providers.md) | Presets, named profiles, the Anthropic API, gateways, prompt caching |
| [Interactive session](docs/interactive-session.md) | The TUI, keybindings, full slash-command reference, output rendering |
| [File references & vision](docs/file-references-and-vision.md) | `@path` inlining, `Ctrl+V` image paste, vision routing |
| [Sessions & resume](docs/sessions.md) | Auto-saved history, `/resume`, `/rename`, storage |
| [Themes](docs/themes.md) | Built-in color themes and live `/theme` switching |
| [Plan, goals & compaction](docs/plan-goals-and-compaction.md) | `/plan`, `/goal`, `todo_write`, context compaction |
| [Built-in tools](docs/tools.md) | Tool catalog, whitespace-tolerant editing, subagents |
| [Web tools](docs/web-tools.md) | `web_search` and `web_fetch` |
| [Code navigation (LSP)](docs/lsp.md) | Language-server-backed tools |
| [Permissions & sandbox](docs/permissions.md) | Modes, classifier, approval rules, sandbox, audit log |
| [Skills](docs/skills.md) | `SKILL.md` skills and on-demand loading |
| [Custom slash commands](docs/custom-commands.md) | Reusable prompt templates |
| [Project memory](docs/memory.md) | `AGENT.md` and the `remember`/`forget` store |
| [MCP servers](docs/mcp.md) | Model Context Protocol tools and deferred loading |
| [Hooks](docs/hooks.md) | Run external commands at lifecycle events |
| [Non-interactive mode](docs/non-interactive.md) | `agent -p`, CI, GitHub Actions PR review |
| [Usage & cost](docs/usage-and-cost.md) | `/usage`, models.dev pricing, price overrides |

## Architecture

```
cmd/agent/            CLI entry, subcommands, composition root
internal/provider/    Provider/Streamer interfaces + factory registry
                      openai.go    — OpenAI-compatible wire format (OpenAI, DeepSeek, custom)
                      anthropic.go — Anthropic Messages API adapter (official Go SDK)
internal/agent/       Agentic loop (agent.go) + system prompt assembly (prompt.go) + compaction
internal/tool/        Tool interface, registry, built-in tools
internal/editmatch/   Context-anchored, whitespace-tolerant edit matching (edit_file)
internal/subagent/    Task delegation: Spawner + task tool (isolated, parallel subagents)
internal/permission/  Risk classifier (tokenizing, evasion-resistant), policy rules, audit log
internal/sandbox/     Command confinement backends (sandbox-exec, bwrap, noop)
internal/hook/        Lifecycle hooks: external-command integration at extension points
internal/mcp/         Model Context Protocol client (stdio + HTTP transports)
internal/lsp/         Language Server Protocol client + navigation tools
internal/webtool/     Web tools: web_search (DuckDuckGo/Brave/Tavily) + web_fetch (HTML→Markdown)
internal/usage/       Token/cost tracking with models.dev pricing (/usage)
internal/skill/       SKILL.md parsing, discovery, installer
internal/command/     User-defined slash commands
internal/memory/      AGENT.md loading + file-backed memory store
internal/session/     Session persistence for /resume (one JSON file per session)
internal/checkpoint/  Per-turn file snapshots for /rewind
internal/theme/       Semantic color roles + built-in themes
internal/diff/        Line-oriented unified diff engine
internal/home/        Resolves the agent home directory (~/.agent or ~/.agents)
internal/repl/        Interactive session: full-screen TUI, live completion, slash commands
```

Design notes (SOLID):

- **SRP** — each tool, the prompt builder, the loop, and the installer are separate units.
- **OCP** — new providers register a factory; new tools implement one interface. No
  existing code changes either way.
- **LSP** — every provider/tool/store implementation is substitutable; tests run the real
  loop against a fake provider.
- **ISP** — the UI observes the loop through small `Events` interfaces; vendors' wire
  formats never leak past `internal/provider`.
- **DIP** — the agent core depends on interfaces (`provider.Provider`, `tool.Tool`,
  `skill.Repository`, `memory.Store`); concrete wiring lives only in `cmd/agent`.

## Development

```bash
go build ./...   # build
go test ./...    # run unit tests
go vet ./...     # static checks
```

## License

See the repository for license details.
