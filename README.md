# agent-cli

A Claude Code–style coding agent CLI written in Go. It runs an agentic tool-use loop
against the **Anthropic Messages API** (via the official Go SDK) or any
**OpenAI-compatible** provider (OpenAI, DeepSeek, or any custom endpoint), with built-in
file/shell tools, reusable skills from `~/.agent/skills`, and project-scoped persistent
memory.

## Features

- **Agentic loop** — the model plans, calls tools, reads results, and iterates until done.
- **Built-in tools** — `bash`, `read_file`, `write_file`, `edit_file`, `glob`, `grep`,
  `list_dir`, `use_skill`, `remember`, `forget`, `web_search`, `web_fetch`, `todo_write`.
- **Skills** — install and invoke SKILL.md-based skills; shares the standard
  `~/.agent/skills` directory so skills are reusable across agent tools; project-local
  `.agent/skills` can shadow global skills.
- **Project memory** — the agent saves durable facts to `.agent/memory/*.md` and reloads
  them into every future session; `AGENT.md` (global and per-project) is injected as
  user instructions.
- **Multi-provider** — two wire formats behind one interface: **Anthropic** (Messages
  API, official SDK — tool use, extended thinking, vision, streaming) and
  **OpenAI-compatible** (OpenAI, DeepSeek, vLLM, Ollama, Moonshot, Qwen… via
  `provider: custom` + `base_url`). Switch mid-session with `/provider`.
- **Interactive session** — Claude Code–style `/` slash commands (switch model/provider
  mid-session, run skills explicitly, inspect config/memory/tools) and `@path` file
  references that inline file contents into your prompt.

## Installation

```bash
go install github.com/xautjzd/agent-cli/cmd/agent@latest
# or from a checkout:
go build -o agent ./cmd/agent
```

Requires Go 1.22+. The result is a single static binary.

## Quick start

```bash
# 1. Configure a provider (pick one)
export DEEPSEEK_API_KEY=sk-...            # DeepSeek (default provider)
export AGENT_PROVIDER=anthropic ANTHROPIC_API_KEY=sk-ant-...   # or Anthropic
export AGENT_PROVIDER=openai OPENAI_API_KEY=sk-...             # or OpenAI

# 2. Run interactively in your project directory
cd my-project
agent

# 3. Or run a single prompt
agent -p "explain the structure of this repository"
```

## Interactive session

Inside `agent` (the REPL), the same conventions as Claude Code / Codex / pi apply.
On a real terminal the session is a **full-screen UI** (a single persistent bubbletea
program in the alternate screen): a scrolling **conversation viewport** on top and a
**bottom-pinned input box**. Typing `/` at the start of the line or `@` anywhere pops up
a **live-filtered candidate menu** as you type.

```
 … conversation scrolls here (PgUp/PgDn to scroll back) …
 ❯ refactor the config loader
 ● EditFile(internal/config/config.go)
   ⎿ Edited internal/config/config.go (+8 -2)

╭──────────────────────────────────────────────────────────────╮
│ >                                                            │
╰──────────────────────────────────────────────────────────────╯
  ↑↓ history/menu · tab accept · pgup/pgdn scroll · /exit
```

Because the program owns the whole viewport, **resizing the terminal repaints cleanly** —
the input stays pinned at the bottom, history stays visible, and there are no stacked
"ghost box" artifacts (the failure mode of an inline editor the terminal reflows). Mid-turn
interactions are handled as overlays in the same program (no nested program is spawned): a
permission confirmation is a modal prompt, and list pickers (`/resume`, `/config`) are
**arrow-navigable** — `↑`/`↓` to move, type to filter, `Enter` to choose, `Esc` to cancel.
`/config` stays open so you can change several settings in a row; enum settings (permission
mode, sandbox, …) present their choices as a sub-list.

Keys: `↑`/`↓` navigate candidates (or input history when no popup is open), `Tab` accepts
the highlighted candidate, `Enter` submits, `PgUp`/`PgDn` scroll the conversation, `Ctrl-C`
interrupts a running turn (or exits when idle). When stdin is piped (scripts, CI), the REPL
falls back to plain line-by-line reading.

### `@path` file references

Mention a file or directory anywhere in a prompt to inline its content:

```
> explain @cmd/agent/main.go and how it relates to @internal/agent
> summarize @README.md, focusing on configuration
```

Files are appended to the message as delimited blocks (100KB cap per file);
directories inline their listing. A missing path is reported before anything is sent.
`@` references also work in one-shot mode: `agent -p "review @main.go"`.

**Images are multimodal**: an `@ref` pointing at `.png/.jpg/.jpeg/.gif/.webp` is attached
as an image part instead of being inlined as text — converted to whichever format the
active provider expects (OpenAI-vision data URLs, or Anthropic base64 image blocks).

### Ctrl+V image paste

Press **Ctrl+V** in the editor to paste an image from the system clipboard. A clean
`[Image #N]` placeholder is inserted at the cursor (never a file path — Claude Code
style), and a `📎 image #N attached` notice appears under the input box. On submit the
placeholder resolves to a multimodal image part alongside your text.

The image itself is written to the **system temporary directory**
(`os.TempDir()` — `$TMPDIR` on macOS, `/tmp` on Linux, `%TEMP%` on Windows), under
`agent-cli-pastes/`. It is one-shot scratch: read once when the message is built, then
left for the OS to reclaim on its normal temp-cleanup schedule — nothing lands in the
repository or your home directory. Older in-tree `.agent/pastes/` images are moved out
to temp automatically on startup.

- macOS: built-in (`osascript`); Linux: needs `wl-clipboard` or `xclip`.
- If the clipboard holds no image, a notice appears and the input is left untouched.

### Vision capability routing

Image turns are routed by what the active model can actually do (Claude Code / codex
models are all vision-capable, so they never face this; here the gap is bridged
explicitly):

1. **Vision-capable model** (`gpt-4o`, `claude-*`, `qwen2.5-vl`, `glm-4v`, …, detected
   from the model name; unrecognized vision models on custom endpoints can be marked in
   their profile with `"vision": true`) → image parts are sent natively.
2. **Text-only model with a vision fallback configured** → the images are first
   described by the fallback model, and the description (with visible text transcribed
   verbatim) is fed to your primary model as text. History stays image-free, so later
   turns keep working:

   ```bash
   agent config set vision_provider openai
   agent config set vision_model gpt-4o-mini
   ```
   ```
   > @shot.png what does this error mean?
   🖼 deepseek-chat has no vision — describing image(s) with gpt-4o-mini…
   ```
3. **Neither** → the turn fails *before any API call* with those exact instructions —
   no cryptic provider 400.

Switching mid-session to a text-only model (`/model`, `/provider`) automatically
replaces image parts already in history with placeholders, so the new model isn't
rejected by leftover images.

### `/` slash commands

Type `/` and the popup lists all commands **and** skills, narrowing as you type
(`/mo` → `/model`, `/memory`). Long lists scroll with the selection — `↑ n more` /
`↓ n more` indicators mark hidden rows, so every entry stays reachable. On piped stdin, submitting a bare `/` opens a numbered
picker instead. Commands:

| Command | Effect |
|---------|--------|
| `/help` | List commands, skills, and usage hints |
| `/model [name]` | Show or switch the model mid-session |
| `/provider <name> [model]` | Switch provider mid-session (prompts for the API key if none is set, and offers to save it) |
| `/<skill-name> [task]` | Run any installed skill as a slash command, optionally with a task |
| `/<command> [args]` | Run a user-defined slash command (see below) |
| `/skills` | List installed skills (aligned; `agent skill show <name>` for full text) |
| `/commands` | List user-defined slash commands |
| `/tools` | List available tools (aligned two-column layout) |
| `/todos` | Show the agent's current task list (from `todo_write`) |
| `/mcp` | List connected MCP servers, their transport, and the tools each contributed |
| `/agents` | List subagent types the `task` tool can delegate to |
| `/hooks` | List configured lifecycle hooks (third-party integration) |
| `/config` | Open the settings panel (view + edit combined) |
| `/memory` | List saved project memories |
| `/goal <text>` | Set a session goal the agent keeps working toward until met (`/goal` shows it, `/goal clear` drops it) |
| `/plan [task]` | Plan mode: explore read-only, propose a plan, implement on approval (`/plan off` exits) |
| `/mode [hitl\|bypass]` | Show or switch the permission mode for dangerous operations |
| `/usage` | Usage & cost: all-time totals + per-model/provider breakdown, plus this session |
| `/compact` | Summarize earlier turns to free up context now (also runs automatically) |
| `/rename [title]` | Rename the current session (no argument prompts for one) |
| `/new` | Start a new session (the current one stays resumable) |
| `/resume [id]` | Resume a previous session — lists sessions to pick from, or jumps straight to an ID/prefix |
| `/clear` | Clear conversation history (keeps system prompt; old session stays resumable) |
| `/exit` | Quit |

Example session:

```
> /provider openai gpt-4o-mini      # hop providers without losing context
> /commit-helper stage and commit my changes
> explain @internal/repl/repl.go
> /clear
```

### Custom slash commands

Drop a markdown file into a `commands` directory and it becomes a `/`-command —
a reusable prompt template, the same idea as Claude Code's `.claude/commands`:

- **Personal** — `~/.agent/commands/<name>.md` (available in every project)
- **Project** — `.agent/commands/<name>.md` (checked into the repo, shared with the team; shadows a personal command of the same name)

Nested directories namespace the command: `commands/git/commit.md` → `/git:commit`.

The file is an optional `---` frontmatter block (`description`, `argument-hint`)
followed by the prompt body. Arguments are substituted into the body:

- `$ARGUMENTS` — the full argument string
- `$1`, `$2`, … — positional arguments (whitespace-split)
- if the body has no placeholder, the arguments are appended to it

`@path` file references in the result are expanded just like a typed prompt.
When filled, the prompt is sent to the agent as an ordinary turn.

```markdown
---
description: Review a pull request
argument-hint: <pr-number>
---
Review PR #$1 for correctness, tests, and style. Summarize risks first.
```

Then run it: `/review 42`. List what's available with `/commands`.

### Sessions & resume

Every conversation is auto-saved per turn as plain, human-inspectable JSON — stored at
**user level**, keyed by project:

```
~/.agent/projects/<encoded-project-path>/sessions/<id>.json
```

Sessions are deliberately *not* kept inside the repository: a working tree is shared with
your team, while conversation history is personal, so storing it there would both leak
transcripts and clutter the repo. Each project still sees only its own sessions. History
left in an older project-local `.agent/sessions/` is migrated automatically on first run.

(Project **memory** and project **skills** stay in the repo on purpose — those are shared
knowledge about the codebase.) Titles are derived from your first message and can be
changed any time with `/rename` — including *before* the first message, in which case the
name is applied when the session starts. `/new` starts a fresh session; `/resume` opens an
interactive picker — `↑`/`↓` to move, **type to search** (matches title, model, and ID),
`Enter` to select, `Esc` to cancel:

```
> /resume
Resume a session:
  search: read
❯ 2m ago     fix the failing tests            deepseek-chat · 12 msgs
  1h ago     explain repository structure     deepseek-chat · 4 msgs
  ↑↓ navigate · type to search · enter select · esc cancel
```

(Piped stdin falls back to a numbered list; `/resume <id-or-prefix>` skips the picker.)

Resuming restores the full conversation into context (the system prompt is rebuilt fresh
so new memories/skills apply) and **replays the transcript through the exact same
rendering pipeline as live chat** — `> input` lines show what you actually typed (not
the expanded @file payloads), assistant text renders normally, and tool calls reappear
as `● ToolName(args)` with their green/red status dots and `⎿` result previews.
History is also traceable from the shell:

```bash
agent session list             # all recorded sessions
agent session show <id>            # full transcript incl. tool calls and results
agent session rename <id> <title>  # retitle a session
agent session delete <id>
```

### Goals

`/goal <condition>` sets a session-scoped goal, modeled on Claude Code's `/goal`:

- The agent starts working toward it **immediately**, using tools.
- After every turn (including later user messages), a goal check runs: the agent must
  either verify the goal holds — emitting a completion marker, which **auto-clears** the
  goal — or keep working. It is told not to stop and ask for permission.
- A round cap (8 per trigger) prevents unattended runaway loops; if hit, the goal stays
  active and is re-checked on your next message.
- Goals persist in the session file, so `/resume` restores a pending goal. `/new` and
  `/clear` drop it (goals are session-scoped). The active goal shows in `/config`.

```
> /goal make `go test ./...` pass and gofmt report no diffs
Goal set: make `go test ./...` pass and gofmt report no diffs
● Bash(go test ./...)
...
✓ Goal achieved — cleared.
```

### Plan mode

`/plan [task]` enters plan mode, modeled on Claude Code:

- **Mutation tools are hard-disabled** (`write_file`, `edit_file`, `remember`, `forget`
  are removed from the registry — not just discouraged). `bash` and the read tools stay
  available for exploration, with instructions not to run mutating commands.
- The prompt changes to `plan>` and every input becomes a planning turn: the agent
  explores and ends with a concise numbered plan.
- After each plan, an approval gate: **`y`** restores full tools, exits plan mode, and
  tells the agent to implement (goal checks apply as usual); **Enter** keeps refining
  the plan; **`q`** (or `/plan off`) exits without implementing.

```
> /plan add request caching to the client
plan> ● Glob(**/*.go)
      ...
      1. Add a Cache interface in internal/cache …
      2. …
Approve plan? [y = implement · Enter = keep planning · q = exit plan mode]
```

### Context compaction

Long sessions eventually approach the model's context window. Rather than failing or
silently dropping messages, the agent **compacts** — the same approach mainstream coding
agents (Claude Code, Codex, opencode) take:

- **Automatic**: after a turn, if context occupancy (from the last request's token usage)
  crosses **85 %** of `context_limit`, the older turns are replaced by a model-written
  summary while the most recent turns (~6 messages, snapped to a turn boundary) are kept
  **verbatim**. The system prompt is never touched. A dim `⊙ Compacted context …` line
  reports it.
- **Manual**: run **`/compact`** any time to summarize now (e.g. before a large task).
- **Faithful surgery**: the cut always lands on a user-message boundary, so a tool result
  is never separated from the assistant tool call it answers (which the API would reject).
  Compaction is best-effort — if summarization fails, history is left untouched.
- The summary is produced by the current model by default (no tools attached); the
  `Summarizer` interface lets a dedicated/cheaper model be injected in code.

Configure via `auto_compact` (`on`/`off`, default on) and `context_limit` (usable window in
tokens, default 128000 — raise it for large-window models, lower it for small ones), both
editable live in `/config`:

```jsonc
{ "auto_compact": "on", "context_limit": 200000 }
```

### Permission modes

Every tool call passes through a permission gate. A classifier flags dangerous
operations: destructive shell commands (`rm`, `sudo`, `git push`, `git reset --hard`,
`chmod`, `kill`, piping downloads into a shell, …) and file writes **outside** the
project directory. Two modes decide what happens:

- **`hitl` (default)** — dangerous operations pause and ask, showing a **diff preview**
  for file edits so you approve the concrete change, not just a path:

  ```
  ⚠ Approval required
    tool: edit_file
    reason: writing outside the project directory (/etc/hosts)
    change:
        1 - 127.0.0.1 localhost
        1 + 127.0.0.1 evil
  Allow? [y]es / [n]o / [a]lways (this session) / [d]eny always
  ```

  **`a`** remembers the choice for the rest of the session (scoped to the command's
  program or the file's directory) so you are not re-prompted; **`d`** denies it for the
  session. Denying feeds the refusal back to the model as the tool result, so it adjusts
  course instead of aborting.

- **`bypass`** — no confirmations; the agent completes the task autonomously. Every
  dangerous operation is auto-approved **with a structured audit note injected into the
  conversation context** (tool, risk reason, full arguments, time, cwd). Because tool
  results are part of the context, the note is sent to the model *and* persisted in the
  session file — `agent session show <id>` reveals exactly what ran unattended.

Switch with `/mode bypass` / `/mode hitl`, or start one-shot runs with `agent -bypass -p "..."`.
**An active `/goal` always runs in bypass mode** (goal pursuit must not stall on
confirmations); the effective mode reverts when the goal clears. `/config` shows the
effective mode.

### Hardened command classification

The dangerous-command classifier does not match the raw command string (a deny-list that
`\rm`, `/bin/rm`, `"rm"`, or `sh -c 'rm …'` would slip past). Instead it **tokenizes** the
command into the individual programs it will actually run — splitting on `; | && ||` and
extracting `$(…)` substitutions, respecting quotes — then **normalizes** each program name
(strips the directory, a leading backslash, surrounding quotes) and classifies the
normalized name. Obfuscating a command name is itself flagged; shell interpreters
(`sh -c`, `eval`, …) are dangerous because they run anything; command wrappers
(`timeout`, `env`, `xargs`, …) are unwrapped and their payload re-checked; and anything
unparseable **fails closed**. Two postures (`bash_policy`):

- **`standard`** (default) — flag known-dangerous commands, allow the rest.
- **`strict`** — additionally require approval for any command **not** on the known-safe
  allow-list (default-deny for the unknown).

### Approval rules (per tool / path / command)

Beyond the built-in classifier, declare granular rules under `permissions` in `config.json`.
Rules are evaluated in order; the first match wins; `action` is `allow`, `ask`, or `deny`
(a `deny` blocks in every mode, even bypass):

```jsonc
{
  "permissions": [
    { "tool": "bash", "command": "\\bgit\\s+push\\b", "action": "deny" },   // never push
    { "tool": "edit_file", "path": "vendor/**", "action": "deny" },         // vendor is off-limits
    { "tool": "write_file", "path": "src/**", "action": "allow" }           // auto-approve src edits
  ]
}
```

`command` is a regex matched against a bash command; `path` is a glob (`**` spans
directories) matched against a file path (relative or absolute).

### Command sandbox (defense in depth)

Optionally confine bash commands so a mistaken or malicious command cannot write outside
the project — a layer *beneath* the gate (the gate decides *whether* a command runs; the
sandbox limits *what* it can touch). Enable with `sandbox` = `on` / `auto` (`auto` is
silent when no backend is present):

- **macOS** — `sandbox-exec` (Seatbelt) with a generated profile: reads allowed broadly,
  **writes confined to the working directory** (plus temp/cache), optionally no network.
- **Linux** — `bubblewrap` (`bwrap`): host bind-mounted read-only, the project writable.

```jsonc
{ "sandbox": "auto", "sandbox_deny_network": false }
```

### Structured audit log

Every gated decision — approved, denied, or auto-approved in bypass — is appended as a
JSON line to a per-project audit log (`~/.agent/projects/<encoded>/audit.log`), with the
time, tool, full arguments, decision, reason, matched rule, mode, cwd, and whether the
command was sandboxed. This is a durable record, separate
from the in-context note the model sees.

### Output rendering

- **Streaming**: assistant text and thinking arrive token by token over SSE (with
  `stream_options.include_usage`, so token stats stay accurate). Tool calls are
  assembled from argument fragments transparently. Providers or display sinks without
  streaming support fall back to blocking completions automatically — same output,
  delivered at the end.
- **Thinking** (reasoning models like `deepseek-reasoner`): shown dim + italic under a
  `✻ Thinking…` header, visually separate from the final answer — streamed live when
  the model streams it.
- **Tool calls**: rendered as `● ToolName(args)` with CamelCase names (`ReadFile`,
  `Bash`, …). The dot is yellow while running, then repainted **green** on success or
  **red** on failure, with a dim `⎿ result preview` line underneath.
- **File edits show a diff**: `edit_file` and `write_file` return a line-numbered
  unified diff, rendered with **red removals, green additions** and dim context under
  the tool call:

  ```
  ● EditFile(demo.go)
    ⎿ Edited demo.go (+1 -1)
          5   func greet(name string) {
          6 - 	fmt.Println("Hello, " + name)
          6 + 	fmt.Printf("Hello, %s\n", name)
  ```

  The diff goes to the model too — it is the most precise confirmation of what changed,
  so a mis-applied edit is caught without re-reading the file. Output is bounded:
  3 lines of context per change, at most 12 change blocks and 40 printed lines, with the
  remainder summarized. Creating a brand-new file reports `Created <path>` instead.
- **Stats**: after every turn a dim summary line reports elapsed time, the turn's token
  split (summed over all tool-loop rounds), the **current context occupancy** (the last
  request's prompt + completion — what the next request starts from), and session totals:

  ```
  ⏱ 2.4s · turn: 3,023 in + 123 out (2 rounds) · context: 1,602 tok · session: 3,146 tok, 2.4s
  ```

  `/clear` and `/new` reset the context estimate (it reads "unknown" until the next model
  reply).

### Usage & cost tracking

`/usage` reports token consumption and **estimated cost** the way Claude Code's Usage panel
does — **all-time for the project** (persisted across sessions), broken down **by model** and
**by provider**, plus the current session:

```
Usage · this project · all time

  Total cost    $401.23
  Tokens        1.1m  (27.1k in · 1.0m out)
  Requests      72
  Model time    4h30m36s

  By model
    claude-opus-4-8   738.9k tok   17.2k → 721.7k   $287.69
    claude-fable-5    307.0k tok    4.9k → 302.1k   $113.54
    glm-4.6            13.0k tok    5.0k →   8.0k         —
  By provider
    anthropic    1.0m tok   22.1k → 1.0m   $401.23
    deepseek    13.0k tok    5.0k → 8.0k         —

  This session   15.2k tok · 12.3s · context 15.2k
```

- Totals accumulate to `~/.agent/projects/<encoded>/usage.json`, so "total consumed" survives
  restarts; **subagent** turns count toward the totals too (shared recorder).
- Prices come from **[models.dev](https://models.dev)** (the primary source, kept current):
  the catalog is cached at `~/.agent/models-dev-prices.json`, loaded instantly on startup and
  refreshed in the background (24h TTL) — so it never blocks startup and works offline from the
  last cache. Because the same model id is listed under many providers (first-party + gateways)
  at different prices, the price is matched by **your provider**; when the provider is unknown,
  the model's **most-common** (first-party) price is used, not an inflated gateway rate.
- **Config `prices` as a backstop** for models models.dev doesn't cover, and a small **built-in
  table** for offline first runs. A model with no price anywhere shows **`—`** — its tokens
  still count. Config prices (USD per 1M tokens) are keyed by model; because cost is derived
  from stored tokens on every read, a newly available price **retroactively costs
  already-accumulated usage**:

  ```jsonc
  {
    "prices": {
      "deepseek-v4-pro": { "input": 0.28, "output": 1.14 },
      "glm-4.6":         { "input": 0.60, "output": 2.20 }
    }
  }
  ```
  When a model is unpriced, `/usage` lists it with a copy-pasteable `"prices"` snippet.

## Non-interactive mode (CI, PR review, GitHub Actions)

`agent -p "<prompt>"` runs a single prompt and exits — no TUI, no prompts. It's built for
automation: **it never waits for a human**, so it can't hang in CI.

```bash
agent -p "review the staged diff; list bugs, security issues, and style problems"
git diff origin/main | agent -p "review this diff" -q     # pipe context on stdin
echo "summarize @CHANGELOG.md" | agent -p -                # "-" reads the prompt from stdin
agent -p "audit @internal/config" -output json | jq -r .result
```

- **Input** — a normal `-p "…"` prompt; `-p -` reads the whole prompt from stdin; and when
  stdin is piped alongside a `-p` prompt, that stdin (a diff, a log) is appended as context.
  `@path` references work as usual.
- **Permissions** — with no human to approve, a dangerous operation is **denied** rather than
  blocking (safe default for read-only review). Pass **`-bypass`** to auto-approve and
  audit-log dangerous operations for autonomous runs.
- **Output** — the final answer goes to **stdout** (cleanly pipeable); tool activity goes to
  **stderr**; `-q` suppresses tool activity. **`-output json`** emits a structured object
  (`result`, `provider`, `model`, `input_tokens`, `output_tokens`, `rounds`,
  `duration_seconds`, `cost_usd`, and `error` on failure) — stdout is JSON only.
- **Exit code** — `0` on success, non-zero on error (a failed API call, denied-required
  operation, etc.), so a workflow step fails loudly.

### GitHub Actions: PR review

```yaml
name: agent-review
on: pull_request
jobs:
  review:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 }
      - name: Install agent-cli
        run: go install github.com/xautjzd/agent-cli/cmd/agent@latest
      - name: Review the diff
        env:
          DEEPSEEK_API_KEY: ${{ secrets.DEEPSEEK_API_KEY }}   # or ANTHROPIC_API_KEY, etc.
        run: |
          git diff origin/${{ github.base_ref }}...HEAD \
            | agent -p "You are a senior reviewer. Review this diff and list concrete
                        correctness, security, and style issues with file:line references.
                        End with APPROVE or REQUEST_CHANGES." -q \
            | tee review.md
      - name: Post as a PR comment
        uses: marocchino/sticky-pull-request-comment@v2
        with: { path: review.md }
```

The review runs read-only (no `-bypass`), so the agent can `git diff`/`grep`/read files but
any accidental mutation is denied. Gate the merge on the output by grepping for
`REQUEST_CHANGES`, or use `-output json` and parse `.result`.

## Configuration

Layered precedence, modeled on Claude Code / codex / opencode:

**flags > env vars > project config (`<project>/.agent/config.json`) > global config (`~/.agent/config.json`) > defaults**

The global directory is resolved as: `$AGENT_HOME` if set, otherwise whichever of
`~/.agent` or `~/.agents` already exists (singular wins if both do), otherwise
`~/.agent`. It holds `config.json`, `skills/`, and `AGENT.md`. Run `agent config show`
to see which paths are actually in use.

| Setting | Flag | Env var | Config key | Default |
|---------|------|---------|------------|---------|
| Provider | `-provider` | `AGENT_PROVIDER` | `provider` | `deepseek` |
| Model | `-model` | `AGENT_MODEL` | `model` | per provider |
| API key | — | `AGENT_API_KEY`, `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `DEEPSEEK_API_KEY` | `api_key` | — |
| Base URL | — | `AGENT_BASE_URL` | `base_url` | per provider |
| Max turns | — | — | `max_turns` | `40` |
| Permission mode | `-bypass` | — | `permission_mode` | `hitl` |
| Goal round cap | — | — | `goal_max_rounds` | `8` |
| Extended thinking | — | — | `thinking` | `adaptive` (Anthropic only; `off` disables) |

### Built-in providers (zero config)

Common vendors ship as presets — naming one is the whole configuration, because the
endpoint, wire format, auth style, default model and credential variable are all known:

```bash
export ZHIPUAI_API_KEY=...        # or DEEPSEEK_API_KEY, MOONSHOT_API_KEY, DASHSCOPE_API_KEY…
agent config set provider glm     # done — model, base_url and auth resolved automatically
```

| Preset | Vendor | Wire format | Credential |
|---|---|---|---|
| `anthropic` | Anthropic | Anthropic | `ANTHROPIC_API_KEY` |
| `openai` | OpenAI | OpenAI | `OPENAI_API_KEY` |
| `deepseek` | DeepSeek | OpenAI | `DEEPSEEK_API_KEY` |
| `glm` (`zhipu`) | Zhipu GLM | OpenAI | `ZHIPUAI_API_KEY` |
| `glm-anthropic` | Zhipu GLM (Claude-Code endpoint) | Anthropic | `ANTHROPIC_AUTH_TOKEN` |
| `deepseek-anthropic` | DeepSeek (Claude-Code endpoint) | Anthropic | `ANTHROPIC_AUTH_TOKEN` |
| `kimi` (`moonshot`) | Moonshot Kimi | OpenAI | `MOONSHOT_API_KEY` |
| `kimi-anthropic` | Moonshot Kimi (Claude-Code endpoint) | Anthropic | `ANTHROPIC_AUTH_TOKEN` |
| `dashscope` (`qwen`) | Alibaba DashScope | OpenAI | `DASHSCOPE_API_KEY` |
| `dashscope-intl` | DashScope (Singapore) | OpenAI | `DASHSCOPE_API_KEY` |
| `openrouter` | OpenRouter | OpenAI | `OPENROUTER_API_KEY` |
| `siliconflow` | SiliconFlow | OpenAI | `SILICONFLOW_API_KEY` |
| `ollama` | Ollama (local) | OpenAI | — |

`/provider` with no argument lists your config profiles and the built-ins **separately**
— a profile named like a preset (e.g. `glm`) shadows it and shows once, tagged
"overrides built-in", so there are no duplicates. Each built-in shows its default model
and whether its credential is exported. **`/provider <TAB>` and `/model <TAB>` complete
from this catalog** (deduplicated against your profiles), and a missing credential is
reported with the exact variable to export.

Model lists are seeded from [models.dev](https://models.dev) and refreshed periodically;
they are advisory (any model the vendor accepts works, listed or not) — see the note at
the end of this section.

Presets are defaults, never constraints:

- Any field you set explicitly (`model`, `base_url`, `api_key`) wins over the preset —
  including a model newer than this catalog knows about.
- A named profile with the same name shadows the preset entirely, so `glm` can point at
  your own gateway.
- Endpoints and model lists change over time; treat the catalog as convenience, and
  override anything a vendor moves.

Full example with **named provider profiles** (any OpenAI-compatible endpoint, addressed
by name — codex/opencode style; `env_key` keeps secrets out of the file):

```json
{
  "provider": "deepseek",
  "model": "deepseek-chat",
  "max_turns": 40,
  "permission_mode": "hitl",
  "goal_max_rounds": 8,
  "providers": {
    "ollama":   {"base_url": "http://localhost:11434/v1", "model": "qwen2.5-coder:32b", "api_key": "ollama"},
    "moonshot": {"base_url": "https://api.moonshot.cn/v1", "model": "kimi-k2", "env_key": "MOONSHOT_API_KEY"}
  }
}
```

With profiles defined, `"provider": "ollama"` in config or `/provider ollama` in-session
just works. A project's `.agent/config.json` overrides the global file per field, and
profiles merge by name — so a project can pin its own model or add one endpoint without
repeating everything.

### Anthropic provider

`provider: anthropic` speaks the **Messages API** through the official
`anthropic-sdk-go`, not the OpenAI-compatible format. The adapter is the only place
Anthropic types appear — the agent loop, tools, sessions, permission gate, plan mode and
goals all work identically across providers. It translates in both directions:

| Concern | OpenAI-compatible | Anthropic |
|---|---|---|
| System prompt | a message with `role: system` | top-level `system` field |
| Tool calls | `tool_calls[].function.arguments` (JSON **string**) | `tool_use` block, `input` (JSON **object**) |
| Tool results | one message per result | `tool_result` blocks, **all in one user message** |
| Images | `image_url` data URL | `image` block with `media_type` + base64 |
| Thinking | `reasoning_content` (display-only) | signed `thinking` blocks, replayed verbatim |
| `max_tokens` | optional | **required** (defaults supplied) |

Notes:

- **Extended thinking is on by default** (adaptive, summarized) and renders through the
  usual `✻ Thinking…` display. Disable with `agent config set thinking off`.
  Thinking blocks carry signatures and are replayed unchanged on later turns — the API
  rejects altered ones — so reasoning survives multi-turn tool loops.
- **Parallel tool calls** are preserved: every result for one assistant turn is sent in a
  single user message, which is what keeps the model issuing parallel calls.
- **Credentials** come from `ANTHROPIC_API_KEY`, the config file, or — since the SDK
  resolves them itself — an `ant auth login` profile on disk.

```bash
export ANTHROPIC_API_KEY=sk-ant-...
agent -provider anthropic                       # claude-opus-4-8 by default
agent -provider anthropic -model claude-sonnet-5
```

#### Anthropic-compatible third-party gateways

Several vendors expose an **Anthropic-compatible** endpoint so Claude Code–style clients
can drive their models. Declare one as a named profile with `"format": "anthropic"` —
the profile keeps its own endpoint, model and credentials, and `/provider <name>`
switches to it mid-session:

```json
{
  "provider": "glm",
  "providers": {
    "glm":  {"format": "anthropic", "auth": "bearer",
             "base_url": "https://open.bigmodel.cn/api/anthropic",
             "model": "glm-4.6", "env_key": "ANTHROPIC_AUTH_TOKEN"},
    "qwen": {"format": "anthropic", "auth": "bearer",
             "base_url": "https://dashscope.aliyuncs.com/api/v2/apps/claude-code-proxy",
             "model": "qwen3-coder-plus", "env_key": "DASHSCOPE_API_KEY"},
    "ollama": {"base_url": "http://localhost:11434/v1", "model": "qwen2.5-coder:32b", "api_key": "ollama"}
  }
}
```

Without `"format"` a profile uses the OpenAI-compatible client, so both wire formats can
coexist. **Confirm the exact base URL and model names against your vendor's console** —
these paths differ per provider and change over time.

Profile fields: `format` (`openai` default, or `anthropic`), `auth` (`api_key` default —
sends `x-api-key` as Anthropic does; `bearer` sends `Authorization: Bearer`, which most
third-party gateways require), `base_url`, `model`, `api_key`/`env_key`, `vision`.

If a gateway rejects the request, three settings usually explain it:

- **`401 InvalidApiKey`** — either the credential is not exported (check
  `agent config show` for `api_key: (not set)`) or the auth style is wrong; try
  flipping `"auth"` between `bearer` and `api_key`.

- **Extended thinking** is requested by default and not every gateway implements it —
  `agent config set thinking off` disables it.
- **Streaming**: gateways that only implement the blocking API report
  `no stream events`; that means the endpoint is not fully compatible.

### Shell commands

```bash
agent config show                            # resolved config + both file paths (key masked)
agent config init                            # write a starter global config (0600 perms)
agent config set model deepseek-chat         # persist to the global file
agent config set permission_mode bypass project   # persist to <project>/.agent/config.json
```

### Interactive configuration (in-session)

- `/config` — open the settings panel (viewing and editing are one screen; there is no
  separate read-only dump) (Claude Code style): a searchable list of
  settings with their current values. **↑/↓** move, **type** to filter, **Space** toggles
  a choice value (e.g. permission mode, extended thinking), **Enter** edits a free-text
  value inline, **Esc** exits. Each change applies to the running session and is saved to
  the global config immediately; the panel stays open so you can change the next setting.
  (Piped stdin falls back to a numbered select-then-value flow with a scope choice.)
- `/config set <key> <value> [global|project|session]` — one-liner version

All edits apply to the running session immediately — changing `api_key`/`base_url`
rebuilds the provider client on the spot, `max_turns`/`goal_max_rounds`/`permission_mode`
take effect on the next turn, and invalid values are rejected before anything is written.

### MCP servers

Model Context Protocol servers extend the agent with external tools. Declare them under
`mcpServers` in `config.json` (global or project) using the same shape as Claude Code —
two transports are supported:

```jsonc
{
  "mcpServers": {
    // Remote HTTP (Streamable HTTP) server — "type": "http" or infer it from "url"
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

At startup the agent connects to each enabled server (`initialize` → `tools/list`), and
merges its tools into the tool set namespaced as **`mcp__<server>__<tool>`** so names never
collide across servers. `mcpServers` maps merge across config layers, so a project can add
or override individual servers without repeating the global map. Connections have a 20 s
handshake timeout; a server that fails to connect is reported (a warning on stderr and in
`/mcp`) but never blocks startup or the other servers. Stdio child processes are terminated
on exit. Run **`/mcp`** to see each server's transport, status, and contributed tools; the
namespaced tools also appear in `/tools`.

## Skills

A skill is a directory containing `SKILL.md` with YAML frontmatter followed by markdown
instructions — the same layout Claude Code uses:

```markdown
---
name: commit-helper
description: Generates conventional commit messages from staged changes.
---

# Commit Helper

1. Run `git diff --cached` to inspect staged changes.
2. ...
```

Skills are discovered from `~/.agent/skills/<name>/SKILL.md` (global, shared with other
agent tools) and `.agent/skills/<name>/SKILL.md` (project-local, shadows global on name
conflicts).

```bash
agent skill list                                  # list installed skills
agent skill install ./my-skill                    # install from a local directory
agent skill install https://github.com/org/skills # install every skill in a git repo
agent skill show commit-helper                    # print a skill's full instructions
agent skill remove commit-helper                  # uninstall
```

At runtime, only skill names + descriptions are placed in the system prompt. When a task
matches a skill, the model calls the `use_skill` tool to load its full instructions —
keeping context cheap until a skill is actually needed.

## Project memory

Two mechanisms, both project-scoped:

1. **`AGENT.md`** — instructions you write. `~/.agent/AGENT.md` applies globally,
   `<project>/AGENT.md` applies to one project (and wins on conflict). Both are injected
   into the system prompt verbatim.
2. **`.agent/memory/*.md`** — facts the agent saves itself via the `remember` tool
   (conventions, decisions, preferences). Reloaded into the system prompt every session.

```bash
agent memory list           # list saved memories
agent memory show api-style # print one memory
agent memory delete api-style
```

Tip: add `.agent/` to `.gitignore` if you don't want memory committed, or commit it to
share the agent's knowledge with your team.

## Built-in tools

| Tool | Purpose |
|------|---------|
| `bash` | Run shell commands (builds, tests, git) with timeout and output truncation |
| `read_file` | Read files with line numbers, offset/limit windowing |
| `write_file` | Create/overwrite files, auto-creating parent directories |
| `edit_file` | Context-anchored, whitespace-tolerant replacement; rejects ambiguous matches (see below) |
| `glob` | Find files by pattern (`**/*.go`), skipping `.git`, `node_modules`, etc. |
| `grep` | Regex content search returning `path:line:text` |
| `list_dir` | List directory entries |
| `use_skill` | Load an installed skill's instructions on demand |
| `remember` / `forget` | Save/delete project-scoped memories |
| `task` | Delegate an independent sub-task to a subagent (see below) |
| `web_search` | Search the web for current docs, APIs, versions, error explanations |
| `web_fetch` | Fetch a URL as Markdown; optional `prompt` returns only the relevant part |
| `todo_write` | Maintain a structured todo list to plan and track a multi-step task (see below) |

### Task planning & progress tracking

For any multi-step or non-trivial task, the agent maintains a structured **todo
list** via the `todo_write` tool — the planning pattern popularized by Claude Code,
pi, and opencode. Writing the plan up front and checking items off as it goes
measurably improves completion of complex work and gives you a live view of where
the agent is.

Each todo has a `content` (imperative, e.g. *"Add the parser"*), a `status`
(`pending` → `in_progress` → `completed`), and an optional `activeForm` (the
present-continuous label shown while running, e.g. *"Adding the parser"*). The
rules the model follows:

- Write the whole plan up front; **each call replaces the entire list**.
- Keep **exactly one** item `in_progress` at a time (the tool rejects more).
- Mark an item `completed` the moment it's done — no batching.
- Skip the list entirely for trivial single-step tasks.

The list renders as a live checklist under the tool call:

```
Todos:
  ✓ Design the todo tool
  ✓ Wire it into the registry
  ▶ Adding tests
  ☐ Update the README
(2 done · 1 in progress · 1 pending)
```

Run **`/todos`** at any time to reprint the agent's current list.

### Task delegation & parallel subagents

The `task` tool lets the agent hand an independent sub-task to a **subagent** — a fresh,
isolated agent with its own context window and tool set — that runs autonomously and returns
a concise report (modeled on Claude Code's Task tool, opencode/codex sub-agents):

- **Isolation**: the subagent's intermediate work (reading many files, searches, trial runs)
  stays in *its* context; only the final report returns to the main conversation, keeping the
  parent's context lean.
- **Parallelism**: each delegation is one tool call, so the model can issue several in a
  single turn. The agent core **executes independent tool calls concurrently** (gating and
  result ordering stay deterministic), so independent sub-tasks run in parallel.
- **Bounded depth**: subagents are built with a Task-free tool set, so a subagent cannot
  spawn further subagents — delegation depth is exactly one.
- **Safety**: subagent tool calls pass through the same permission gate as the parent
  (serialized so concurrent subagents can't interleave prompts); in plan mode the `task` tool
  is withheld so a subagent can't escape read-only exploration.

Run **`/agents`** to see the available subagent types. The built-in `general-purpose` type is
always present; define your own under `subagents` in `config.json`:

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

Subagents inherit the parent's current provider/model, so a mid-session `/provider` or
`/model` switch applies to delegated work too.

### Hooks (third-party integration)

Hooks run **external commands at lifecycle events**, letting you wire the agent into
linters, notifiers, policy engines, or loggers without touching its code (modeled on
Claude Code's hooks and the notify/plugin hooks of codex, opencode, pi agent). Declare them
under `hooks` in `config.json`, keyed by event, and run **`/hooks`** to list them.

Extension points:

| Event | Fires | A hook can |
|-------|-------|------------|
| `SessionStart` | session begins/resumes | show a message |
| `UserPromptSubmit` | before a prompt is sent | **block** it, or inject context |
| `PreToolUse` | before a tool runs | **block** the call, or inject context |
| `PostToolUse` | after a tool runs | inject context (e.g. lint findings) |
| `Stop` | the agent finishes a turn | notify |
| `SessionEnd` | session ends | clean up / notify |

The command receives a **JSON payload on stdin** (`event`, `timestamp`, `session`, `cwd`,
and — for tool events — `tool`, `tool_input`, `tool_result`, `tool_ok`; for prompts,
`prompt`). It influences the agent by its **stdout** and **exit code**:

- **exit 0** — success. Stdout that is a JSON object is parsed as
  `{"decision","reason","additionalContext","systemMessage"}`; any other non-empty stdout
  becomes `additionalContext`. `additionalContext` is fed into the conversation;
  `systemMessage` is shown only to you.
- **non-zero exit** — the action is **blocked**; the reason is the JSON `reason` or stderr.

`matcher` (a regex on the tool name) scopes `PreToolUse`/`PostToolUse` to specific tools.
Multiple hooks per event run in order and their contexts concatenate; `timeout_seconds`
bounds each (default 30s). Hooks accumulate across config layers.

```jsonc
{
  "hooks": {
    "PostToolUse": [
      { "matcher": "^edit_file$",
        "command": "golangci-lint run --out-format tab 2>/dev/null | head -20" }
    ],
    "PreToolUse": [
      { "matcher": "^bash$",
        "command": "jq -e '.tool_input.command | test(\"rm -rf /$\")' >/dev/null && echo '{\"decision\":\"block\",\"reason\":\"refusing rm -rf /\"}' || true" }
    ],
    "Stop": [ { "command": "osascript -e 'display notification \"agent done\"'" } ]
  }
}
```

`PreToolUse` hooks run **in addition to** the permission gate — both can block, giving a
programmable policy layer on top of the built-in classifier.

#### Triggering a third-party HTTP call

Because a hook is just a shell command, it can call any HTTP API with `curl` (or `wget`,
`http`, …). The payload is on **stdin**, so pipe it straight into the request body or pick
fields out with `jq`.

**Fire-and-forget notification** — POST to a Slack incoming webhook when the agent finishes
a turn (backgrounded with `&` so it never delays the session):

```jsonc
{
  "hooks": {
    "Stop": [
      { "command": "curl -sf -X POST -H 'Content-Type: application/json' -d '{\"text\":\"✅ agent finished a turn\"}' \"$SLACK_WEBHOOK_URL\" >/dev/null 2>&1 &" }
    ]
  }
}
```

**Forward every tool call to an audit/telemetry endpoint** — the stdin payload *is* the
request body, so no reshaping is needed:

```jsonc
{
  "hooks": {
    "PreToolUse": [
      { "command": "curl -sf -X POST -H 'Content-Type: application/json' --data @- https://audit.internal.example/agent-events >/dev/null 2>&1 &" }
    ]
  }
}
```

**Synchronous policy check against a remote service** — call an approval API and let its
answer decide the outcome. Return the API's JSON verdict on stdout (or exit non-zero to
block). Here `jq` turns a `{"allow":false,"reason":"…"}` API response into the agent's
`{"decision":"block","reason":"…"}` contract:

```jsonc
{
  "hooks": {
    "PreToolUse": [
      { "matcher": "^bash$",
        "command": "curl -sf -X POST --data @- https://policy.internal.example/check | jq -c 'if .allow then {} else {decision:\"block\", reason:.reason} end'" }
    ]
  }
}
```

Notes:
- The hook inherits the process environment, so secrets like `$SLACK_WEBHOOK_URL` come from
  your shell (or the config's env). Keep tokens out of `config.json`.
- For **blocking** calls (`PreToolUse`/`UserPromptSubmit`), don't background the request —
  the agent waits for stdout/exit code. Keep it fast and set `timeout_seconds` (default 30s)
  so a slow endpoint can't stall a turn; a timeout counts as a block.
- For **observational** events (`PostToolUse`, `Stop`, `SessionStart`/`End`), append `&` to
  fire-and-forget so the network round-trip never blocks the agent.

### Web tools

`web_search` and `web_fetch` let the agent look things up on its own — current library
versions, an API reference, a changelog, or the meaning of an error message — instead of
guessing from stale training data.

- **`web_search`** returns titles, URLs, and snippets. The default backend is **DuckDuckGo,
  which needs no API key** (a best-effort HTML scrape). For higher reliability, configure
  **Brave** or **Tavily** under `web_search` in `config.json`:

  ```jsonc
  { "web_search": { "provider": "brave", "env_key": "BRAVE_API_KEY" } }
  // or: { "web_search": { "provider": "tavily", "api_key": "tvly-..." } }
  ```
  (Brave/Tavily read the key from `api_key`, the configured `env_key`, or the standard
  `BRAVE_API_KEY` / `TAVILY_API_KEY` variable.)

- **`web_fetch`** GETs an http(s) URL and returns its content as **Markdown** — headings,
  links (`[text](url)`), lists, and code survive, so the model can cite a section or follow a
  link (plain text/JSON pass through). Following Claude Code, an optional **`prompt`** returns
  only the parts relevant to it, distilled by the model, instead of the whole page — keeping
  the conversation lean. Fetches are **cached ~15 min**, bounded by a size cap and a 30 s
  timeout, and refuse non-http(s) schemes and cloud-metadata/link-local addresses.

A typical loop: `web_search "deepseek api streaming 2026"` → pick a result →
`web_fetch(url, prompt: "streaming request example")` → use the extracted snippet. Both tools
are available to subagents too, so a delegated research task can browse on its own.

### Whitespace-tolerant editing

Exact string replacement is brittle — one differing space, tab, or trailing character makes
an edit fail even when the target is obvious. `edit_file` uses **context-anchored, fuzzy
matching** (the approach behind Aider's SEARCH/REPLACE blocks and Claude Code's edit tool),
implemented in `internal/editmatch` as three tiers tried in order:

1. **exact** — byte-for-byte substring (fastest, zero ambiguity).
2. **line-trim** — whole-line match after trimming each line's leading/trailing whitespace;
   the replacement is **re-indented** to the target's actual indentation.
3. **ws-collapse** — whole-line match after collapsing internal whitespace runs; also
   re-indented. The most forgiving tier.

So the model can send a block at the wrong indent (or with a stray trailing space) and the
edit still lands correctly, reflowed to fit. Safety is preserved: an **ambiguous** match
(more than one location) is still rejected unless `replace_all` is set, so an edit never
silently hits the wrong place. When nothing matches, the error **points at the closest
region** ("closest region starts near line N, X% of lines similar") so the model can
self-correct instead of guessing. A report notes when a fuzzy tier (not exact) was used, so
the applied change can be double-checked against the returned diff.

## Architecture

```
cmd/agent/            CLI entry, subcommands, composition root
internal/provider/    Provider/Streamer interfaces + factory registry
                      openai.go    — OpenAI-compatible wire format (OpenAI, DeepSeek, custom)
                      anthropic.go — Anthropic Messages API adapter (official Go SDK)
internal/tool/        Tool interface, registry, built-in tools
internal/editmatch/   Context-anchored, whitespace-tolerant edit matching (edit_file)
internal/subagent/    Task delegation: Spawner + task tool (isolated, parallel subagents)
internal/permission/  Risk classifier (tokenizing, evasion-resistant), policy rules, audit log
internal/sandbox/     Command confinement backends (sandbox-exec, bwrap, noop)
internal/hook/        Lifecycle hooks: external-command integration at extension points
internal/webtool/     Web tools: web_search (DuckDuckGo/Brave/Tavily) + web_fetch (HTML→text)
internal/usage/       Token/cost tracking with models.dev pricing (/usage)
internal/skill/       SKILL.md parsing, discovery (FSRepository), installer
internal/memory/      AGENT.md loading + file-backed memory store
internal/session/     Session persistence for /resume (one JSON file per session)
internal/diff/        Line-oriented unified diff engine (pure; used by file tools)
internal/home/        Resolves the agent home directory (~/.agent or ~/.agents)
internal/agent/       Agentic loop (agent.go) + system prompt assembly (prompt.go)
internal/repl/        Interactive session: full-screen TUI (tui.go — viewport +
                      bottom-pinned input, clean resize), live completion
                      (complete.go), slash commands, @path expansion
```

Design notes (SOLID):

- **SRP** — each tool, the prompt builder, the loop, and the installer are separate units.
- **OCP** — new providers register a factory; new tools implement one interface. No
  existing code changes either way.
- **LSP** — every provider/tool/store implementation is substitutable; tests run the real
  loop against a fake provider.
- **ISP** — the UI observes the loop through a 3-method `Events` interface; vendors' wire
  formats never leak past `internal/provider`.
- **DIP** — the agent core depends on `provider.Provider`, `tool.Tool`,
  `skill.Repository`, and `memory.Store` interfaces; concrete wiring lives only in
  `cmd/agent` (the composition root).

## Development

```bash
go build ./...   # build
go test ./...    # run unit tests
go vet ./...     # static checks
```

## Limitations

- The completion popup requires a real terminal; piped stdin uses plain line input.
- No permission prompts before tool execution — run it in projects you trust it with.
- Context is not compacted; very long sessions may exceed the model's window.
