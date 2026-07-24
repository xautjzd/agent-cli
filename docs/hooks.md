# Hooks

Hooks run **external commands at lifecycle events**, letting you wire the agent into
linters, notifiers, policy engines, or loggers without touching its code (modeled
on Claude Code's hooks and the notify/plugin hooks of codex, opencode, pi agent).

Declare them under `hooks` in `config.json`, keyed by event, and run **`/hooks`** to
list them. Hooks **accumulate** across config layers.

## Events

| Event | Fires | A hook can |
|---|---|---|
| `SessionStart` | session begins/resumes | show a message |
| `UserPromptSubmit` | before a prompt is sent | **block** it, or inject context |
| `PreToolUse` | before a tool runs | **block** the call, or inject context |
| `PostToolUse` | after a tool runs | inject context (e.g. lint findings) |
| `Stop` | the agent finishes a turn | notify |
| `SessionEnd` | session ends | clean up / notify |

## The contract

The command receives a **JSON payload on stdin** (`event`, `timestamp`, `session`,
`cwd`, and — for tool events — `tool`, `tool_input`, `tool_result`, `tool_ok`; for
prompts, `prompt`). It influences the agent by its **stdout** and **exit code**:

- **exit 0** — success. Stdout that is a JSON object is parsed as
  `{"decision","reason","additionalContext","systemMessage"}`; any other non-empty
  stdout becomes `additionalContext`. `additionalContext` is fed into the
  conversation; `systemMessage` is shown only to you.
- **non-zero exit** — the action is **blocked**; the reason is the JSON `reason` or
  stderr.

`matcher` (a regex on the tool name) scopes `PreToolUse`/`PostToolUse` to specific
tools. Multiple hooks per event run in order and their contexts concatenate;
`timeout_seconds` bounds each (default 30 s).

`PreToolUse` hooks run **in addition to** the [permission gate](permissions.md) —
both can block — giving a programmable policy layer on top of the built-in
classifier.

## Configuration

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

| Field | Meaning |
|---|---|
| `matcher` | Regex on the tool name (`PreToolUse`/`PostToolUse` only) |
| `command` | Shell command, run via `sh -c` |
| `timeout_seconds` | Per-command timeout (default 30) |

## More examples by event

### `SessionStart` — inject project context

Prepend the output of a command to the model's context when a session begins (here,
the current branch and recent commits):

```jsonc
{
  "hooks": {
    "SessionStart": [
      { "command": "echo \"Branch: $(git branch --show-current)\"; git log --oneline -5" }
    ]
  }
}
```

Non-JSON stdout on exit 0 becomes `additionalContext`, so the model starts already
knowing where you are.

### `UserPromptSubmit` — guard or enrich prompts

Block a prompt that mentions a forbidden path, otherwise let it through:

```jsonc
{
  "hooks": {
    "UserPromptSubmit": [
      { "command": "grep -qi 'production database' && echo '{\"decision\":\"block\",\"reason\":\"no direct prod DB changes\"}' || true" }
    ]
  }
}
```

The prompt text arrives on stdin (as `prompt` in the JSON payload, or raw for a
simple `grep`).

### `PreToolUse` — programmable policy

Refuse writes to protected files, layered on top of the built-in classifier:

```jsonc
{
  "hooks": {
    "PreToolUse": [
      { "matcher": "^(write_file|edit_file)$",
        "command": "jq -e '.tool_input.path | test(\"(^|/)(go\\\\.mod|\\\\.github/)\")' >/dev/null && echo '{\"decision\":\"block\",\"reason\":\"protected path\"}' || true" }
    ]
  }
}
```

### `PostToolUse` — run checks after edits

Feed compiler/linter output back to the model after every file edit so it fixes
breakage in the same turn:

```jsonc
{
  "hooks": {
    "PostToolUse": [
      { "matcher": "^(write_file|edit_file)$",
        "command": "go build ./... 2>&1 | head -20" }
    ]
  }
}
```

Non-empty stdout becomes `additionalContext` — the model sees the build errors and
keeps going.

### `Stop` / `SessionEnd` — notify

```jsonc
{
  "hooks": {
    "Stop":       [ { "command": "osascript -e 'display notification \"turn done\" with title \"agent\"' &" } ],
    "SessionEnd": [ { "command": "echo \"$(date): session $(jq -r .session) ended\" >> ~/agent-sessions.log &" } ]
  }
}
```

### Everything together

A single project `config.json` can define hooks for several events at once — they
accumulate with any global hooks:

```jsonc
{
  "hooks": {
    "SessionStart": [ { "command": "git log --oneline -3" } ],
    "PostToolUse":  [ { "matcher": "^edit_file$", "command": "golangci-lint run 2>/dev/null | head -20" } ],
    "PreToolUse":   [ { "matcher": "^bash$",
                        "command": "jq -e '.tool_input.command | test(\"rm -rf /$\")' >/dev/null && echo '{\"decision\":\"block\",\"reason\":\"refusing rm -rf /\"}' || true" } ],
    "Stop":         [ { "command": "osascript -e 'display notification \"agent done\"' &" } ]
  }
}
```

## Calling a third-party HTTP API

A hook is just a shell command, so it can call any HTTP API with `curl`. The payload
is on **stdin** — pipe it straight into the request body or pick fields with `jq`.

**Fire-and-forget notification** (POST to a Slack webhook on turn end; `&` so it
never delays the session):

```jsonc
{
  "hooks": {
    "Stop": [
      { "command": "curl -sf -X POST -H 'Content-Type: application/json' -d '{\"text\":\"✅ agent finished a turn\"}' \"$SLACK_WEBHOOK_URL\" >/dev/null 2>&1 &" }
    ]
  }
}
```

**Forward every tool call to an audit endpoint** — the stdin payload *is* the
request body:

```jsonc
{
  "hooks": {
    "PreToolUse": [
      { "command": "curl -sf -X POST -H 'Content-Type: application/json' --data @- https://audit.internal.example/agent-events >/dev/null 2>&1 &" }
    ]
  }
}
```

**Synchronous policy check** — call an approval API and let its answer decide. `jq`
turns a `{"allow":false,"reason":"…"}` response into the agent's `{"decision":"block"}`
contract:

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

### Guidelines

- The hook inherits the process environment, so secrets like `$SLACK_WEBHOOK_URL`
  come from your shell. **Keep tokens out of `config.json`.**
- For **blocking** calls (`PreToolUse` / `UserPromptSubmit`), don't background the
  request — the agent waits for stdout/exit code. Keep it fast and set
  `timeout_seconds`; a timeout counts as a block.
- For **observational** events (`PostToolUse`, `Stop`, `SessionStart`/`End`), append
  `&` to fire-and-forget so the network round-trip never blocks the agent.

> **Note:** the `-p` one-shot path fires `PreToolUse`/`PostToolUse` (they run inside
> the agent loop) but not the session/prompt lifecycle events.
