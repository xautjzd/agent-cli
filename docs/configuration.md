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
| Provider | `-provider` | `AGENT_PROVIDER` | `provider` | none — pick one with `/provider` or `agent provider use` |
| Model | `-model` | `AGENT_MODEL` | `model` | per provider |
| API key | — | `AGENT_API_KEY`, the vendor's own variable, or `<NAME>_API_KEY` for a custom provider | `api_key` | — |
| Base URL | — | `AGENT_BASE_URL` | `base_url` | per provider |
| Wire format | `-format` | `AGENT_FORMAT` | `format` | the vendor's primary wire (`anthropic` picks a Claude-Code endpoint where one exists) |
| Max turns | — | — | `max_turns` | `40` |
| Permission mode | `-bypass` | — | `permission_mode` | `hitl` |
| Bash risk posture | — | — | `bash_policy` | `standard` |
| Sandbox | — | — | `sandbox` | `off` |
| Sandbox: block network | — | — | `sandbox_deny_network` | `false` |
| Goal round cap | — | — | `goal_max_rounds` | `8` |
| Extended thinking | — | — | `thinking` | `adaptive` (`off`/`minimal`/`low`/`medium`/`high`/`xhigh`/`max`; per-model support — see providers.md) |
| Prompt caching | — | — | `prompt_cache` | `on` (Anthropic; `off` disables) |
| Auto-compaction | — | — | `auto_compact` | `on` |
| Context window | — | — | `context_limit` | `128000` |
| Deferred tool loading | — | — | `lazy_tools` | `on` |
| Color theme | — | — | `theme` | `dark` |
| Web search engine | — | — | `web_search_provider` (writes `web_search.provider`) | `duckduckgo` — see [web tools](web-tools.md) |
| Vision provider | — | — | `vision_provider` | — |
| Vision model | — | — | `vision_model` | — |

## Structured keys

These keys hold maps or lists, each documented in its own guide:

| Key | Purpose | Guide |
|---|---|---|
| `providers` | Custom provider definitions | [Providers](providers.md) |
| `api_keys` | A credential per provider name, without defining a provider | [Providers](providers.md) |
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
  "model": "deepseek-v4-flash",
  "max_turns": 40,
  "permission_mode": "hitl",
  "goal_max_rounds": 8,
  "auto_compact": "on",
  "context_limit": 128000,
  "theme": "dracula",
  "api_keys": {
    "deepseek": "sk-…"
  },
  "providers": {
    "my-gw":  {"base_url": "https://llm.internal/v1", "model": "internal-v2"},
    "gw2":    {"base_url": "https://gw2.example/anthropic", "model": "claude-x",
               "format": "anthropic", "auth": "bearer"}
  }
}
```

`api_keys` holds a credential for a **built-in** provider — storing one there
keeps the vendor's endpoint and model list coming from the catalog, whereas an
entry in `providers` *defines* a provider and takes over that name. `providers`
entries without an `api_key` read `<NAME>_API_KEY` from the environment.

## The `agent config` CLI

```bash
agent config show                                  # resolved config + both file paths (secrets masked)
agent config init                                  # write a starter global config.json (0600 perms)
agent config set model deepseek-v4-pro             # persist to the global file
agent config set permission_mode bypass project    # persist to <project>/.agent/config.json
```

`agent config set <key> <value> [project]` writes to the global file by default, or
to the project file when `project` is appended. Values are validated before being
written — an invalid theme, mode, or number is rejected.

## The `agent provider` CLI

Provider selection has its own subcommand, so it needs no key names:

```bash
agent provider list                                # every provider + credential status
agent provider use anthropic claude-sonnet-5       # persist provider (and model)
agent provider use deepseek --anthropic            # pick a vendor's other wire
agent provider add                                 # define a custom one (guided)
agent provider remove my-gw
```

See **[Providers & models](providers.md)**.

## The in-session `/config` panel

Inside the interactive session, `/config` opens a searchable settings panel
(Claude Code style) that combines viewing and editing:

- **↑/↓** move, **type** to filter the list.
- **Space** toggles a choice value (permission mode, thinking, sandbox, theme…).
- **Enter** edits a free-text value inline.
- **Esc** exits — the panel stays open between edits so you can change several
  settings in a row.

**Provider**, **Model** and **Provider base URL** are shown for context but are
**read-only** here — switch them with [`/provider`](providers.md) and `/model`,
which change the endpoint, credential and model together. The API key is not
listed: masked, it says nothing useful (see `agent config set api_key` and the
environment variables above).

Every other change **applies to the running session immediately** and is saved to
the **global** config:

- `max_turns` / `goal_max_rounds` / `permission_mode` take effect on the next turn,
- `theme` re-colors the session immediately,
- `web_search_provider` repoints `web_search` at once, subagents included.

One-liner form: `/config set <key> <value> [global|project|session]`. The `session`
scope applies the value without persisting it.

On piped stdin (scripts), `/config` falls back to a numbered select-then-value flow
with a scope choice.
