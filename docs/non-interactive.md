# Non-interactive mode (CI, PR review, GitHub Actions)

`agent -p "<prompt>"` runs a single prompt and exits — no TUI, no prompts. It's
built for automation: **it never waits for a human**, so it can't hang in CI.

```bash
agent -p "review the staged diff; list bugs, security issues, and style problems"
git diff origin/main | agent -p "review this diff" -q     # pipe context on stdin
echo "summarize @CHANGELOG.md" | agent -p -                # "-" reads the prompt from stdin
agent -p "audit @internal/config" -output json | jq -r .result
```

## Input

- A normal `-p "…"` prompt.
- `-p -` reads the **whole prompt from stdin**.
- When stdin is piped alongside a `-p` prompt, that stdin (a diff, a log) is
  **appended as context**.
- `@path` references work as usual.

## Permissions

With no human to approve, a dangerous operation is **denied** rather than blocking
(safe default for read-only review). Pass **`-bypass`** to auto-approve and
audit-log dangerous operations for autonomous runs. See [Permissions](permissions.md).

## Output

- The final answer goes to **stdout** (cleanly pipeable).
- Tool activity goes to **stderr**; `-q` suppresses it.
- **`-output json`** emits a structured object (stdout is JSON only):

  ```json
  {
    "result": "...",
    "provider": "deepseek",
    "model": "deepseek-chat",
    "input_tokens": 3021,
    "output_tokens": 288,
    "rounds": 2,
    "duration_seconds": 4.1,
    "cost_usd": 0.0017,
    "error": ""
  }
  ```

## Exit code

`0` on success, non-zero on error (a failed API call, a denied required operation,
etc.), so a workflow step fails loudly.

## Flags

| Flag | Meaning |
|---|---|
| `-p <prompt>` | Run one prompt and exit (`-` reads from stdin) |
| `-q` | Print only the final answer (suppress tool activity) |
| `-output text\|json` | Output format (default `text`) |
| `-bypass` | Auto-approve + audit dangerous operations |
| `-provider`, `-model` | Override provider/model for this run |

## GitHub Actions: PR review

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

The review runs read-only (no `-bypass`), so the agent can `git diff` / `grep` /
read files but any accidental mutation is denied. Gate the merge by grepping the
output for `REQUEST_CHANGES`, or use `-output json` and parse `.result`.
