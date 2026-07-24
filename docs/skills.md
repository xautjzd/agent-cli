# Skills

A skill is a directory containing `SKILL.md` with YAML frontmatter followed by
markdown instructions — the same layout Claude Code uses.

```markdown
---
name: commit-helper
description: Generates conventional commit messages from staged changes.
---

# Commit Helper

1. Run `git diff --cached` to inspect staged changes.
2. ...
```

## Discovery & precedence

Skills are discovered from two roots:

- `<agent-home>/skills/<name>/SKILL.md` — **global**, shared with other agent tools.
- `<project>/.agent/skills/<name>/SKILL.md` — **project-local**, shadows a global
  skill of the same name.

Project skills stay in the repo on purpose — they're shared knowledge about the
codebase.

## Usage

At runtime, only skill **names + descriptions** are placed in the system prompt.
When a task matches a skill, the model calls the `use_skill` tool to load its full
instructions — keeping context cheap until a skill is actually needed.

You can also invoke a skill explicitly as a slash command:

```
> /commit-helper stage and commit my changes
```

List installed skills with `/skills` in-session.

## Managing skills

```bash
agent skill list                                  # list installed skills
agent skill install ./my-skill                    # install from a local directory
agent skill install https://github.com/org/skills # install every skill in a git repo
agent skill show commit-helper                    # print a skill's full instructions
agent skill remove commit-helper                  # uninstall
```

## Related

- [Custom slash commands](custom-commands.md) — simpler reusable prompt templates
  (no `use_skill` tool, no on-demand loading).
- [Memory](memory.md) — durable project facts, a different mechanism.
