# Interactive session

Running `agent` with no `-p` flag starts the interactive REPL. On a real terminal
it's a **full-screen UI** — a single persistent bubbletea program in the alternate
screen — with a scrolling **conversation viewport** on top and a **bottom-pinned
input box**.

```
 … conversation scrolls here (PgUp/PgDn to scroll back) …
 ❯ refactor the config loader
 ● EditFile(internal/config/config.go)
   ⎿ Edited internal/config/config.go (+8 -2)

╭──────────────────────────────────────────────────────────────╮
│ >                                                            │
╰──────────────────────────────────────────────────────────────╯
  ↑↓ scroll/menu · ctrl-p/ctrl-n history · tab accept · /exit
```

Because the program owns the whole viewport, **resizing the terminal repaints
cleanly** — the input stays pinned at the bottom, history stays visible, and there
are no stacked "ghost box" artifacts.

When stdin is piped (scripts, CI), the REPL falls back to plain line-by-line
reading. See **[Non-interactive mode](non-interactive.md)** for `agent -p`.

## Keybindings

| Key | Action |
|---|---|
| `↑` / `↓` | Navigate an open candidate menu; otherwise scroll the conversation (including mouse wheel/trackpad alternate-scroll input) |
| `Ctrl-P` / `Ctrl-N` | Navigate an open candidate menu, or input history when no menu is open |
| `Tab` | Accept the highlighted candidate |
| `Enter` | Submit (or run a highlighted `/`-command directly) |
| `PgUp` / `PgDn` | Scroll the conversation |
| `Ctrl-V` | Paste an image from the clipboard (see [File references & vision](file-references-and-vision.md)) |
| `Ctrl-C` | Interrupt a running turn, or exit when idle |
| `Esc` | Dismiss a menu / cancel an overlay |

Typing `/` at the start of a line or `@` anywhere pops up a **live-filtered
candidate menu**. Mid-turn interactions (permission prompts, `/resume` and
`/config` pickers) appear as overlays in the same program — **arrow-navigable**,
type-to-filter, `Enter` to choose, `Esc` to cancel.

## Slash commands

Type `/` and the popup lists all commands **and** skills, narrowing as you type.
Long lists scroll (`↑ n more` / `↓ n more`). On piped stdin, submitting a bare `/`
opens a numbered picker.

| Command | Effect |
|---|---|
| `/help` | List commands, skills, and usage hints |
| `/model [name]` | Show or switch the model mid-session |
| `/provider [<name> [model]]` | Switch provider (prompts for the API key if none is set, offers to save it); bare lists them all |
| `/provider custom` | Define a custom provider — asks for name, base URL, API style, model, key |
| `/provider remove <name>` | Delete a custom provider |
| `/login [provider] [method]` | Sign in with a provider account/subscription (OpenAI or GitHub Copilot) |
| `/logout [provider]` | Remove a stored provider login |
| `/auth [provider]` | Show safe login status and supported methods |
| `/<skill-name> [task]` | Run any installed skill as a slash command, optionally with a task |
| `/<command> [args]` | Run a [custom slash command](custom-commands.md) |
| `/skills` | List installed skills |
| `/commands` | List user-defined slash commands |
| `/tools` | List available tools |
| `/todos` | Show the agent's current task list (from `todo_write`) |
| `/mcp` | List connected MCP servers, their transport, and contributed tools |
| `/lsp` | List language servers backing the code-navigation tools |
| `/agents` | List subagent types the `task` tool can delegate to |
| `/hooks` | List configured lifecycle hooks |
| `/config` | Open the settings panel (view + edit combined) |
| `/theme [name]` | Switch the color theme (picker, or set one directly) |
| `/memory` | List saved project memories |
| `/goal <text>` | Set a session goal the agent works toward until met (`/goal clear` drops it) |
| `/plan [task]` | Plan mode: explore read-only, propose a plan, implement on approval |
| `/mode [hitl\|bypass]` | Show or switch the permission mode |
| `/effort [level]` | Show or set reasoning effort — only the levels the active model accepts ([details](providers.md#reasoning-effort-is-per-model-not-per-vendor)) |
| `/usage [provider]` | Local usage/cost plus separately labelled live subscription limits |
| `/compact` | Summarize earlier turns to free up context now |
| `/rewind` | Undo — restore files and trim the conversation to an earlier state |
| `/rename [title]` | Rename the current session |
| `/new` | Start a new session (the current one stays resumable) |
| `/resume [id]` | Resume a previous session (picker, or jump to an ID/prefix) |
| `/clear` | Clear history (keeps system prompt; old session stays resumable) |
| `/exit` | Quit |

Example session:

```
> /provider openai gpt-5.6-terra    # hop providers without losing context
> /commit-helper stage and commit my changes
> explain @internal/repl/repl.go
> /clear
```

## Undo with `/rewind`

`/rewind` opens a picker of restore points — one is taken **before every turn**.
Each entry is labelled by the **state it restores to** (the file contents at that
point), newest first. Selecting one:

1. shows a plan — which files will be **restored** and which will be **deleted**
   (files that were *created* after that point), and asks for confirmation;
2. restores those files on disk and trims the conversation back to that turn.

Only `write_file` / `edit_file` edits are snapshotted; file changes made by `bash`
(`rm`, `>`, `sed -i`) are **not** captured.

## Output rendering

- **Streaming** — assistant text and thinking arrive token by token over SSE (with
  `stream_options.include_usage`, so token stats stay accurate). Providers or
  display sinks without streaming fall back to blocking completions automatically.
- **Thinking** (reasoning models like `deepseek-v4-pro`) — shown dim + italic
  under a `✻ Thinking…` header, visually separate from the final answer.
- **Tool calls** — rendered as `● ToolName(args)` with CamelCase names. The dot is
  yellow while running, then repainted **green** on success or **red** on failure,
  with a dim `⎿ result preview` underneath.
- **File edits show a diff** — `edit_file` / `write_file` return a line-numbered
  unified diff, rendered with **red removals, green additions**, dim context:

  ```
  ● EditFile(demo.go)
    ⎿ Edited demo.go (+1 -1)
          5   func greet(name string) {
          6 - 	fmt.Println("Hello, " + name)
          6 + 	fmt.Printf("Hello, %s\n", name)
  ```

  The diff is sent to the model too — the most precise confirmation of what
  changed. Output is bounded (3 context lines, ≤12 change blocks, ≤40 printed
  lines). A brand-new file reports `Created <path>` instead.
- **Stats** — after every turn a dim summary line reports elapsed time, the turn's
  token split, the current context occupancy, and session totals:

  ```
  ⏱ 2.4s · turn: 3,023 in + 123 out (2 rounds) · context: 1,602 tok · session: 3,146 tok, 2.4s
  ```

  `/clear` and `/new` reset the context estimate (it reads "unknown" until the next
  reply).

Colors follow the active **[theme](themes.md)** and are dropped entirely under
`NO_COLOR` or when output is piped.
