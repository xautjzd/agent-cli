# Permissions & sandbox

Every tool call passes through a permission gate before it runs. This guide covers
the **permission modes**, the **risk classifier**, **approval rules**, the
**command sandbox**, and the **audit log** — layers that decide *whether* a
dangerous operation runs and limit *what* it can touch.

## Permission modes

A classifier flags dangerous operations: destructive shell commands (`rm`, `sudo`,
`git push`, `git reset --hard`, `chmod`, `kill`, piping downloads into a shell, …)
and file writes **outside** the project directory. Two modes decide what happens:

### `hitl` (default) — human in the loop

Dangerous operations pause and ask, showing a **diff preview** for file edits so
you approve the concrete change, not just a path:

```
⚠ Approval required
  tool: edit_file
  reason: writing outside the project directory (/etc/hosts)
  change:
      1 - 127.0.0.1 localhost
      1 + 127.0.0.1 evil
Allow? [y]es / [n]o / [a]lways (this session) / [d]eny always
```

- **`a`** remembers the choice for the rest of the session (scoped to the command's
  program or the file's directory) so you aren't re-prompted.
- **`d`** denies it for the session.
- Denying feeds the refusal back to the model as the tool result, so it adjusts
  course instead of aborting.

### `bypass` — autonomous

No confirmations; the agent completes the task autonomously. Every dangerous
operation is auto-approved **with a structured audit note injected into the
conversation context** (tool, risk reason, full arguments, time, cwd). Because tool
results are part of the context, the note is sent to the model *and* persisted in
the session file — `agent session show <id>` reveals exactly what ran unattended.

### Switching modes

```
> /mode bypass
> /mode hitl
```

```bash
agent -bypass -p "..."                       # one-shot run in bypass
agent config set permission_mode bypass      # persist
```

> **An active `/goal` always runs in bypass mode**; the effective mode reverts when
> the goal clears. `/config` shows the effective mode.

In **[non-interactive mode](non-interactive.md)** with no human present, a
dangerous operation under `hitl` is **denied** (never blocks) unless `-bypass` is
passed.

## Hardened command classification

The classifier does **not** match the raw command string (a deny-list that `\rm`,
`/bin/rm`, `"rm"`, or `sh -c 'rm …'` would slip past). Instead it:

1. **tokenizes** the command into the programs it will actually run — splitting on
   `; | && ||` and extracting `$(…)` substitutions, respecting quotes;
2. **normalizes** each program name (strips the directory, a leading backslash,
   surrounding quotes) and classifies the normalized name.

Obfuscating a command name is itself flagged; shell interpreters (`sh -c`,
`eval`, …) are dangerous because they run anything; command wrappers (`timeout`,
`env`, `xargs`, …) are unwrapped and their payload re-checked; anything unparseable
**fails closed**.

Two postures via `bash_policy`:

- **`standard`** (default) — flag known-dangerous commands, allow the rest.
- **`strict`** — additionally require approval for any command **not** on the
  known-safe allow-list (default-deny for the unknown).

```jsonc
{ "bash_policy": "strict" }
```

## Approval rules

Beyond the classifier, declare granular rules under `permissions`. Rules are
evaluated in order; the **first match wins**; `action` is `allow`, `ask`, or
`deny` (a `deny` blocks in **every** mode, even bypass):

```jsonc
{
  "permissions": [
    { "tool": "bash", "command": "\\bgit\\s+push\\b", "action": "deny" },   // never push
    { "tool": "edit_file", "path": "vendor/**", "action": "deny" },         // vendor off-limits
    { "tool": "write_file", "path": "src/**", "action": "allow" }           // auto-approve src edits
  ]
}
```

- `command` — a regex matched against a bash command.
- `path` — a glob (`**` spans directories) matched against a file path (relative or
  absolute).

Rules **accumulate** across config layers, so a project can add its own without
repeating the global ones.

## Command sandbox (defense in depth)

Optionally confine bash commands so a mistaken or malicious command cannot write
outside the project — a layer *beneath* the gate (the gate decides *whether* a
command runs; the sandbox limits *what* it can touch).

```jsonc
{ "sandbox": "auto", "sandbox_deny_network": false }
```

- `sandbox` — `off` (default), `on`, or `auto` (`auto` is silent when no backend is
  present).
- `sandbox_deny_network` — also block network access inside the sandbox.

Backends:

- **macOS** — `sandbox-exec` (Seatbelt) with a generated profile: reads allowed
  broadly, **writes confined to the working directory** (plus temp/cache),
  optionally no network.
- **Linux** — `bubblewrap` (`bwrap`): host bind-mounted read-only, the project
  writable, optionally `--unshare-net`.

Changing `sandbox` needs a restart; `bash_policy` and `permission_mode` apply live.

## Structured audit log

Every gated decision — approved, denied, or auto-approved in bypass — is appended
as a JSON line to a per-project audit log:

```
<agent-home>/projects/<encoded>/audit.log
```

Each record carries the time, tool, full arguments, decision, reason, matched rule,
mode, cwd, and whether the command was sandboxed. This is a durable record,
separate from the in-context note the model sees.

## Related

[Hooks](hooks.md) `PreToolUse` commands run **in addition to** the permission gate
— both can block — giving a programmable policy layer on top of the classifier.
