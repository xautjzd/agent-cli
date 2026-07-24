# MCP servers

Model Context Protocol servers extend the agent with external tools. Declare them
under `mcpServers` in `config.json` (global or project) using the same shape as
Claude Code. Two transports are supported.

## Configuration

```jsonc
{
  "mcpServers": {
    // Remote HTTP (Streamable HTTP) — "type": "http" or infer it from "url"
    "notion": {
      "type": "http",
      "url": "https://mcp.notion.com/mcp",
      "headers": { "Authorization": "Bearer <token>" }   // optional extra headers
    },
    // Local stdio server launched as a child process — infer it from "command"
    "fs": {
      "command": "npx",
      "args": ["-y", "@mcp/server-filesystem", "/path"],
      "env": { "TOKEN": "xxx" }                            // optional extra env vars
    },
    "legacy": { "type": "http", "url": "…", "disabled": true }  // keep the entry, skip it
  }
}
```

| Field | Meaning |
|---|---|
| `type` | `http` or `stdio`; inferred from `url` / `command` if omitted |
| `command`, `args`, `env` | Stdio: the child process to launch and its environment |
| `url`, `headers` | HTTP: the endpoint and any extra request headers |
| `disabled` | Keep the entry but skip connecting |

`mcpServers` maps **merge across config layers**, so a project can add or override
individual servers without repeating the global map.

## Behavior

At startup the agent connects to each enabled server (`initialize` → `tools/list`)
and merges its tools into the tool set, namespaced as **`mcp__<server>__<tool>`** so
names never collide across servers.

- Connections have a **20 s handshake timeout**.
- A server that fails to connect is **reported** (a warning on stderr and in
  `/mcp`) but **never blocks startup** or the other servers.
- Stdio child processes are terminated on exit.

Run **`/mcp`** to see each server's transport, status, and contributed tools; the
namespaced tools also appear in `/tools`.

## Deferred (on-demand) tool loading

MCP schemas can be large and numerous, so sending every one on every request scales
badly. Instead:

- MCP tools are advertised in the system prompt by **name + one-line description
  only** (a compact, cacheable catalog).
- Their full JSON Schema is pulled into context on demand through the built-in
  **`search_tools`** meta-tool, which activates a tool so the model can call it on
  the next turn.

This keeps per-request tool overhead roughly flat as you add MCP servers, rather
than growing linearly with every tool's schema — the same deferred-loading approach
Claude Code uses. Built-in tools stay eagerly advertised.

Disable it (advertise every MCP tool on every request) with:

```jsonc
{ "lazy_tools": "off" }
```

## Example configurations

Real servers you can drop in. Verify each package name / URL against its own docs —
the MCP ecosystem moves quickly.

### Local stdio servers (launched as child processes)

```jsonc
{
  "mcpServers": {
    // Filesystem access scoped to a directory
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/Users/me/projects"]
    },
    // GitHub (issues, PRs, code search) — token via env
    "github": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": { "GITHUB_PERSONAL_ACCESS_TOKEN": "ghp_xxx" }
    },
    // A local SQLite database
    "sqlite": {
      "command": "uvx",
      "args": ["mcp-server-sqlite", "--db-path", "./app.db"]
    }
  }
}
```

> **Keep tokens out of the file** where you can — a stdio server inherits your shell
> environment, so `"env": { "GITHUB_PERSONAL_ACCESS_TOKEN": "$GITHUB_TOKEN" }` is
> **not** expanded (values are literal). Prefer exporting the variable and letting the
> server read it, or use a project file that is git-ignored.

### Remote HTTP servers

```jsonc
{
  "mcpServers": {
    "notion": {
      "type": "http",
      "url": "https://mcp.notion.com/mcp",
      "headers": { "Authorization": "Bearer secret_xxx" }
    },
    "context7": {
      "type": "http",
      "url": "https://mcp.context7.com/mcp"
    }
  }
}
```

### Project-scoped override

A `<project>/.agent/config.json` merges with the global map **by server name**, so a
project can add a server, or disable a global one it doesn't want, without repeating
everything:

```jsonc
// <project>/.agent/config.json
{
  "mcpServers": {
    "playwright": { "command": "npx", "args": ["-y", "@playwright/mcp@latest"] },
    "github":     { "disabled": true }   // turn off the global GitHub server here
  }
}
```

After editing, run `/mcp` to confirm each server's transport, status, and the tools
it contributed (they also appear in `/tools`, namespaced `mcp__<server>__<tool>`).
