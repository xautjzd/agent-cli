# Project memory

Two mechanisms give the agent durable, project-scoped knowledge that reloads into
every future session.

## 1. `AGENT.md` — instructions you write

- `<agent-home>/AGENT.md` applies **globally**.
- `<project>/AGENT.md` applies to **one project** (and wins on conflict).

Both are injected into the system prompt verbatim. Use them for coding conventions,
architecture notes, or "always do X" instructions.

## 2. `.agent/memory/*.md` — facts the agent saves itself

The agent writes durable facts via the `remember` tool (conventions, decisions,
preferences) and removes them with `forget`. Each is a small markdown file under
`<project>/.agent/memory/`, reloaded into the system prompt every session.

## Managing memory

```bash
agent memory list           # list saved memories
agent memory show api-style  # print one memory
agent memory delete api-style
```

Run `/memory` in-session to list saved memories.

## Where it lives

Unlike [sessions](sessions.md) (personal, stored at user level), project memory
**stays in the repo** — it's shared knowledge about the codebase.

> **Tip:** add `.agent/` to `.gitignore` if you don't want memory committed, or
> commit it to share the agent's knowledge with your team.
