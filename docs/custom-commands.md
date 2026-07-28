# Custom slash commands

Drop a markdown file into a `commands` directory and it becomes a `/`-command — a
reusable prompt template, the same idea as Claude Code's `.claude/commands`.

## Locations & precedence

- **Personal** — `<agent-home>/commands/<name>.md` (available in every project).
- **Project** — `<project>/.agent/commands/<name>.md` (checked into the repo,
  shared with the team; shadows a personal command of the same name).

Nested directories namespace the command: `commands/git/commit.md` → `/git:commit`.

Dispatch precedence is **built-in command > custom command > skill** — a
same-named custom command beats a skill, since you authored it.

## File format

An optional `---` frontmatter block (`description`, `argument-hint`) followed by the
prompt body. Unlike skills, **no field is required** — a bare prompt file is valid.

```markdown
---
description: Review a pull request
argument-hint: <pr-number>
---
Review PR #$1 for correctness, tests, and style. Summarize risks first.
```

## Argument substitution

- `$ARGUMENTS` — the full argument string.
- `$1`, `$2`, … — positional arguments (whitespace-split). `$10` works.
- If the body has **no** placeholder, the arguments are appended to it.

`@path` file references in the result are expanded just like a typed prompt. Once
filled, the prompt is sent to the agent as an ordinary turn.

## Usage

```
> /review 42
```

List what's available with `/commands` (shows name, argument-hint, description, and
whether it's a user or project command). `/<TAB>` completes command names.

## Complete examples

Each block below is one file — create it at the given path and the command appears
immediately.

### `$ARGUMENTS` — pass the whole argument string

`~/.agents/commands/explain.md` → run as `/explain the retry logic in @client.go`

```markdown
---
description: Explain a piece of code or behavior in depth
argument-hint: <what to explain>
---
Explain the following clearly, step by step, and call out any edge cases or bugs:

$ARGUMENTS
```

### Positional arguments — `$1`, `$2`, …

`.agent/commands/review.md` (project, checked into the repo) → run as `/review 42`

```markdown
---
description: Review a pull request
argument-hint: <pr-number>
---
Review PR #$1 for correctness, tests, and style.
Start with the highest-risk issues, then list the rest with file:line references.
End with a verdict: APPROVE or REQUEST_CHANGES.
```

### No placeholder — arguments are appended

`~/.agents/commands/commit.md` → run as `/commit` (or `/commit use present tense`)

```markdown
---
description: Stage and commit changes with a conventional-commit message
---
Run `git status` and `git diff --cached` to see what is staged (stage relevant files
first if nothing is). Write a Conventional Commits message (feat/fix/docs/refactor…)
with a concise subject and a short body explaining *why*, then create the commit.
```

Because the body has no `$ARGUMENTS`/`$N`, any text after `/commit` is appended as
an extra instruction — so `/commit keep it to one line` still works.

### Namespaced via a nested directory

`~/.agents/commands/git/sync.md` → run as `/git:sync`

```markdown
---
description: Fetch, rebase onto the upstream default branch, and report conflicts
argument-hint: [base-branch]
---
Fetch the remote, rebase the current branch onto origin/${1} (default: main if no
argument), and if there are conflicts, stop and summarize each conflicting file
instead of guessing a resolution.
```

### Combining `@path` references

File references in the body (or in the arguments) are expanded before the prompt is
sent:

`.agent/commands/test-file.md` → run as `/test-file internal/config/config.go`

```markdown
---
description: Write table-driven tests for a Go source file
argument-hint: <path/to/file.go>
---
Write thorough table-driven Go tests for @$1. Cover edge cases and error paths.
Put them in the matching `_test.go` file and run `go test ./...` to verify.
```

Here `@$1` becomes `@internal/config/config.go`, whose contents are inlined for the
model.
