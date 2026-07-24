# Configuration

Everything the agent does is driven by a layered configuration. This guide is the
complete reference: precedence, file locations, every key, the `agent config` CLI,
and the in-session `/config` panel.

## Precedence

Settings are resolved highest-wins, modeled on Claude Code / codex / opencode:

```
flags  >  env vars  >  project config  >  global config  >  built-in defaults
```

- **Project config** — `<project>/.agent/config.json`
- **Global config** — `<agent-home>/config.json`

Merging is **per field**: a project file overrides only the keys it sets, and map
values (`providers`, `mcpServers`, `hooks`, `permissions`, …) merge by name/entry
rather than replacing the whole map. So a project can pin its own model or add one
MCP server without repeating the global file.

## The agent home directory

The global directory is resolved as:

1. `$AGENT_HOME` if set, else
2. whichever of `~/.agent` or `~/.agents` already exists (singular wins if both do), else
3. `~/.agent` (created on demand).

It holds `config.json`, `skills/`, `commands/`, `AGENT.md`, cached pricing, and the
per-project data (`projects/<encoded-path>/…` for sessions, audit log, usage).
Run `agent config show` to print the exact paths in use.

## Core keys

| Setting | Flag | Env var | Config key | Default |
|---|---|---|---|---|
| Provider | `-provider` | `AGENT_PROVIDER` | `provider` | `deepseek` |
| Model | `-model` | `AGENT_MODEL` | `model` | per provider |
| API key | — | `AGENT_API_KEY`, `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `DEEPSEEK_API_KEY` | `api_key` | — |
| Base URL | — | `AGENT_BASE_URL` | `base_url` | per provider |
| Max turns | — | — | `max_turns` | `40` |
| Permission mode | `-bypass` | — | `permission_mode` | `hitl` |
| Bash risk posture | — | — | `bash_policy` | `standard` |
| Sandbox | — | — | `sandbox` | `off` |
| Sandbox: block network | — | — | `sandbox_deny_network` | `false` |
| Goal round cap | — | — | `goal_max_rounds` | `8` |
| Extended thinking | — | — | `thinking` | `adaptive` (Anthropic; `off` disables) |
| Prompt caching | — | — | `prompt_cache` | `on` (Anthropic; `off` disables) |
| Auto-compaction | — | — | `auto_compact` | `on` |
| Context window | — | — | `context_limit` | `128000` |
| Deferred tool loading | — | — | `lazy_tools` | `on` |
| Color theme | — | — | `theme` | `dark` |
| Vision provider | — | — | `vision_provider` | — |
| Vision model | — | — | `vision_model` | — |

## Structured keys

These keys hold maps or lists, each documented in its own guide:

| Key | Purpose | Guide |
|---|---|---|
| `providers` | Named provider profiles | [Providers](providers.md) |
| `prices` | Per-model price overrides (USD / 1M tokens) | [Usage & cost](usage-and-cost.md) |
| `web_search` | Web-search backend + credential | [Web tools](web-tools.md) |
| `permissions` | Per-tool / path / command approval rules | [Permissions](permissions.md) |
| `mcpServers` | Model Context Protocol servers | [MCP](mcp.md) |
| `lspServers` | Language-server overrides | [LSP](lsp.md) |
| `subagents` | Custom subagent definitions | [Tools](tools.md) |
| `hooks` | Lifecycle hook commands | [Hooks](hooks.md) |

## A full example

```json
{
  "provider": "deepseek",
  "model": "deepseek-chat",
  "max_turns": 40,
  "permission_mode": "hitl",
  "goal_max_rounds": 8,
  "auto_compact": "on",
  "context_limit": 128000,
  "theme": "dracula",
  "providers": {
    "ollama":   {"base_url": "http://localhost:11434/v1", "model": "qwen2.5-coder:32b", "api_key": "ollama"},
    "moonshot": {"base_url": "https://api.moonshot.cn/v1", "model": "kimi-k2", "env_key": "MOONSHOT_API_KEY"}
  }
}
```

## The `agent config` CLI

```bash
agent config show                                  # resolved config + both file paths (secrets masked)
agent config init                                  # write a starter global config.json (0600 perms)
agent config set model deepseek-chat               # persist to the global file
agent config set permission_mode bypass project    # persist to <project>/.agent/config.json
```

`agent config set <key> <value> [project]` writes to the global file by default, or
to the project file when `project` is appended. Values are validated before being
written — an invalid theme, mode, or number is rejected.

## The in-session `/config` panel

Inside the interactive session, `/config` opens a searchable settings panel
(Claude Code style) that combines viewing and editing:

- **↑/↓** move, **type** to filter the list.
- **Space** toggles a choice value (permission mode, thinking, sandbox, theme…).
- **Enter** edits a free-text value inline (secrets are masked).
- **Esc** exits — the panel stays open between edits so you can change several
  settings in a row.

Every change **applies to the running session immediately** and is saved to the
**global** config:

- changing `api_key` / `base_url` rebuilds the provider client on the spot,
- `max_turns` / `goal_max_rounds` / `permission_mode` take effect on the next turn,
- `theme` re-colors the session immediately.

One-liner form: `/config set <key> <value> [global|project|session]`. The `session`
scope applies the value without persisting it.

On piped stdin (scripts), `/config` falls back to a numbered select-then-value flow
with a scope choice.
