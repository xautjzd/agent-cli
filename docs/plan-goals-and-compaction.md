# Plan mode, goals & compaction

Three features for driving and sustaining longer pieces of work: **plan mode**
(look before you leap), **goals** (keep working until a condition holds), and
**context compaction** (survive long sessions). The `todo_write` tool for
in-progress task tracking is covered here too.

---

## Plan mode (`/plan`)

`/plan [task]` enters plan mode, modeled on Claude Code:

- **Mutation tools are hard-disabled** — `write_file`, `edit_file`, `remember`,
  `forget`, and `task` are removed from the registry (not just discouraged).
  `bash` and the read tools stay available for exploration, with instructions not
  to run mutating commands.
- The prompt changes to `plan>` and every input becomes a planning turn tuned for
  **token efficiency**: the agent explores only what it must and returns a short,
  **high-level** plan — **Goal · Approach · Changes · Steps · Verify** — with **no
  code, no pasted file contents, no line-by-line detail**. The detail is filled in
  during implementation, not the plan.
- After each plan, an approval gate:
  - **`y`** — restore full tools, exit plan mode, and implement (goal checks apply);
  - **Enter** — keep refining the plan;
  - **`q`** (or `/plan off`) — exit without implementing.

```
> /plan add request caching to the client
plan> ● Glob(**/*.go)
      Goal: cache GET responses in the HTTP client.
      Approach: add a small in-memory Cache keyed by URL …
      Changes:
        - internal/client/cache.go — new Cache type
        - internal/client/client.go — consult the cache on GET
      Steps: 1. add Cache  2. wire into Do  3. tests
      Verify: go test ./internal/client
Approve plan? [y = implement · Enter = keep planning · q = exit plan mode]
```

---

## Goals (`/goal`)

`/goal <condition>` sets a session-scoped goal, modeled on Claude Code's `/goal`:

- The agent starts working toward it **immediately**, using tools.
- After every turn (including later user messages), a **goal check** runs: the
  agent must either verify the goal holds — emitting a completion marker, which
  **auto-clears** the goal — or keep working. It is told not to stop and ask for
  permission.
- A **round cap** (`goal_max_rounds`, default 8) prevents unattended runaway loops;
  if hit, the goal stays active and is re-checked on your next message.
- Goals persist in the session file, so `/resume` restores a pending goal. `/new`
  and `/clear` drop it. The active goal shows in `/config`.

```
> /goal make `go test ./...` pass and gofmt report no diffs
Goal set: make `go test ./...` pass and gofmt report no diffs
● Bash(go test ./...)
...
✓ Goal achieved — cleared.
```

`/goal` with no argument shows the current goal; `/goal clear` drops it.

> **An active goal always runs in bypass permission mode** (goal pursuit must not
> stall on confirmations); the effective mode reverts when the goal clears. See
> [Permissions](permissions.md).

### Configuration

```jsonc
{ "goal_max_rounds": 8 }
```

---

## Task tracking (`todo_write` / `/todos`)

For any multi-step or non-trivial task, the agent maintains a structured **todo
list** via the `todo_write` tool. Writing the plan up front and checking items off
gives you a live view of where the agent is.

Each todo has a `content` (imperative, e.g. *"Add the parser"*), a `status`
(`pending` → `in_progress` → `completed`), and an optional `activeForm` (the
present-continuous label shown while running). Rules the model follows:

- Write the whole plan up front; **each call replaces the entire list**.
- Keep **exactly one** item `in_progress` (the tool rejects more).
- Mark an item `completed` the moment it's done — no batching.
- Skip the list for trivial single-step tasks.

The list renders as a live checklist under the tool call; run **`/todos`** to
reprint it:

```
Todos:
  ✓ Design the todo tool
  ✓ Wire it into the registry
  ▶ Adding tests
  ☐ Update the README
(2 done · 1 in progress · 1 pending)
```

---

## Context compaction

Long sessions eventually approach the model's context window. Rather than failing
or silently dropping messages, the agent **compacts**:

- **Automatic** — after a turn, if context occupancy (from the last request's token
  usage) crosses **85 %** of `context_limit`, the older turns are replaced by a
  model-written summary while the most recent turns (~6 messages, snapped to a turn
  boundary) are kept **verbatim**. The system prompt is never touched. A dim
  `⊙ Compacted context …` line reports it.
- **Manual** — run **`/compact`** any time to summarize now (e.g. before a large
  task).
- **Faithful surgery** — the cut always lands on a user-message boundary, so a tool
  result is never separated from the assistant tool call it answers (which the API
  would reject). Compaction is best-effort — if summarization fails, history is
  left untouched.
- The summary is produced by the current model by default (no tools attached).

### Configuration

```jsonc
{ "auto_compact": "on", "context_limit": 200000 }
```

- `auto_compact` — `on` (default) or `off`.
- `context_limit` — usable window in tokens (default `128000`). Raise it for
  large-window models, lower it for small ones.

Both are editable live in `/config`.
