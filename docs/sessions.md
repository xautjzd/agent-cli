# Sessions & resume

Every conversation is auto-saved per turn as plain, human-inspectable JSON, stored
at **user level** and keyed by project:

```
<agent-home>/projects/<encoded-project-path>/sessions/<id>.json
```

Sessions are deliberately **not** kept inside the repository: a working tree is
shared with your team, while conversation history is personal, so storing it there
would leak transcripts and clutter the repo. Each project still sees only its own
sessions. History left in an older project-local `.agent/sessions/` is migrated
automatically on first run.

> Project **memory** and project **skills** stay in the repo on purpose — those are
> shared knowledge about the codebase. See [Memory](memory.md) and [Skills](skills.md).

## Usage

### Resume

`/resume` opens an interactive picker — `↑`/`↓` to move, **type to search**
(matches title, model, and ID), `Enter` to select, `Esc` to cancel:

```
> /resume
Resume a session:
  search: read
❯ 2m ago     fix the failing tests            deepseek-chat · 12 msgs
  1h ago     explain repository structure     deepseek-chat · 4 msgs
  ↑↓ navigate · type to search · enter select · esc cancel
```

- Piped stdin falls back to a numbered list.
- `/resume <id-or-prefix>` skips the picker.

Resuming restores the full conversation into context (the system prompt is rebuilt
fresh so new memories/skills apply) and **replays the transcript through the exact
same rendering pipeline as live chat** — `> input` lines show what you actually
typed (not expanded `@file` payloads), and tool calls reappear with their
green/red status dots and `⎿` result previews.

### New / clear

- `/new` — start a fresh session (the current one stays resumable).
- `/clear` — clear conversation history but keep the system prompt; the old session
  stays resumable.

### Rename

Titles are derived from your first message and can be changed any time with
`/rename` — including **before** the first message, in which case the name is
applied when the session starts.

```
> /rename fix the login bug     # rename now
> /rename                       # show the current title and prompt for a new one
```

## From the shell

```bash
agent session list                 # all recorded sessions
agent session show <id>            # full transcript incl. tool calls and results
agent session rename <id> <title>  # retitle a session
agent session delete <id>
```

IDs accept a unique prefix. Titles are normalized identically whether set via
`/rename` or the CLI.
