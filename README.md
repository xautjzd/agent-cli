# agent-cli

A Claude Code–style coding agent CLI written in Go. It runs an agentic tool-use loop
against the **Anthropic Messages API** (via the official Go SDK) or any
**OpenAI-compatible** provider (OpenAI, DeepSeek, or any custom endpoint), with built-in
file/shell tools, reusable skills from `~/.agent/skills`, and project-scoped persistent
memory.

## Features

- **Agentic loop** — the model plans, calls tools, reads results, and iterates until done.
- **Built-in tools** — `bash`, `read_file`, `write_file`, `edit_file`, `glob`, `grep`,
  `list_dir`, `use_skill`, `remember`, `forget`.
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
On a real terminal the input line is a rich editor (built on bubbletea): the pending
input renders inside a **rounded frame** that clearly separates it from the output
above, and typing `/` at the start of the line or `@` anywhere pops up a
**live-filtered candidate menu** as you type. On submit the frame collapses to a
compact `❯ input` line so scrollback stays dense (transcript replay uses the same
form).

```
╭──────────────────────────────────────────────╮
│ > refactor the config loader                 │
╰──────────────────────────────────────────────╯
```

Keys: `↑`/`↓` navigate candidates (or input history when no popup is open),
`Tab` or `Enter` accept the highlighted candidate, `Esc` dismisses, `Ctrl-C` exits.
When stdin is piped (scripts, CI), the REPL automatically falls back to plain
line-by-line reading.

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
| `/provider <name> [model]` | Switch provider mid-session (history preserved; credentials re-resolved) |
| `/skill <name> [task]` | Explicitly run a skill, optionally with a task |
| `/<skill-name> [task]` | Shorthand — any installed skill works as a slash command |
| `/skills` | List installed skills (aligned; `agent skill show <name>` for full text) |
| `/tools` | List available tools (aligned two-column layout) |
| `/mcp` | List connected MCP servers, their transport, and the tools each contributed |
| `/agents` | List subagent types the `task` tool can delegate to |
| `/config` | Open the settings panel (view + edit combined) |
| `/memory` | List saved project memories |
| `/goal <text>` | Set a session goal the agent keeps working toward until met (`/goal` shows it, `/goal clear` drops it) |
| `/plan [task]` | Plan mode: explore read-only, propose a plan, implement on approval (`/plan off` exits) |
| `/mode [hitl\|bypass]` | Show or switch the permission mode for dangerous operations |
| `/usage` | Show session token totals, model time, and current context occupancy |
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

- **`hitl` (default)** — dangerous operations pause and ask:

  ```
  ⚠ Dangerous operation requested
    tool: bash
    reason: file deletion (rm)
    args: {"command": "rm victim.txt"}
  Allow this operation? [y/N]
  ```

  Denying feeds the refusal back to the model as the tool result, so it adjusts course
  instead of aborting.

- **`bypass`** — no confirmations; the agent completes the task autonomously. Every
  dangerous operation is auto-approved **with an audit note injected into the
  conversation context** (tool, risk reason, full arguments, time, cwd). Because tool
  results are part of the context, the note is sent to the model *and* persisted in the
  session file — `agent session show <id>` reveals exactly what ran unattended.

Switch with `/mode bypass` / `/mode hitl`, or start one-shot runs with `agent -bypass -p "..."`.
**An active `/goal` always runs in bypass mode** (goal pursuit must not stall on
confirmations); the effective mode reverts when the goal clears. `/config` shows the
effective mode.

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

  `/usage` shows the session summary on demand. `/clear` and `/new` reset the context
  estimate (it reads "unknown" until the next model reply).

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
internal/skill/       SKILL.md parsing, discovery (FSRepository), installer
internal/memory/      AGENT.md loading + file-backed memory store
internal/session/     Session persistence for /resume (one JSON file per session)
internal/diff/        Line-oriented unified diff engine (pure; used by file tools)
internal/home/        Resolves the agent home directory (~/.agent or ~/.agents)
internal/agent/       Agentic loop (agent.go) + system prompt assembly (prompt.go)
internal/repl/        Interactive session: bubbletea line editor with live completion
                      (editor.go, complete.go), slash commands, @path expansion
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
