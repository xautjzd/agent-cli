# agent-cli documentation

Detailed usage and configuration guides for every feature. New here? Start with
**[Getting started](getting-started.md)**, then skim **[Configuration](configuration.md)**.

Each guide is split into a **Usage** part (how to drive the feature) and a
**Configuration** part (the keys, flags, and files that control it).

## Guides

### Basics
- [Getting started](getting-started.md) — install, first run, quick start.
- [Automatic updates](automatic-updates.md) — startup checks, update choices, verification, and opt-out.
- [Configuration](configuration.md) — precedence, files, every config key, `agent config`, and the `/config` panel.
- [Providers & models](providers.md) — built-in provider presets, named profiles, managed subscription login, the Anthropic Messages API, third-party gateways, and prompt caching.

### Interactive use
- [Interactive session](interactive-session.md) — the full-screen TUI, keybindings, slash commands, activity views, and output rendering.
- [File references & vision](file-references-and-vision.md) — `@path` inlining, `Ctrl+V` image paste, and vision-capability routing.
- [Sessions & resume](sessions.md) — auto-saved history, `/resume`, `/rename`, and where transcripts live.
- [Themes](themes.md) — built-in color themes and live `/theme` switching.

### Working with the agent
- [Plan mode, goals & compaction](plan-goals-and-compaction.md) — `/plan`, `/goal`, `todo_write`, and automatic context compaction.
- [Built-in tools](tools.md) — the tool catalog, whitespace-tolerant editing, and subagent delegation.
- [Web tools](web-tools.md) — `web_search` and `web_fetch`.
- [Code navigation (LSP)](lsp.md) — language-server-backed diagnostics and navigation tools.

### Security & control
- [Permissions & sandbox](permissions.md) — permission modes, the risk classifier, approval rules, the command sandbox, and the audit log.

### Extending
- [Skills](skills.md) — `SKILL.md` skills and on-demand loading.
- [Custom slash commands](custom-commands.md) — reusable prompt templates.
- [Project memory](memory.md) — `AGENT.md` and the `remember`/`forget` store.
- [MCP servers](mcp.md) — Model Context Protocol tools and deferred loading.
- [Hooks](hooks.md) — run external commands at lifecycle events.

### Automation
- [Non-interactive mode](non-interactive.md) — `agent -p`, CI, and GitHub Actions PR review.
- [Usage & cost tracking](usage-and-cost.md) — cross-project `/usage`, live subscription limits, models.dev pricing, and price overrides.
