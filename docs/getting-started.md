# Getting started

`agent-cli` is a Claude Code–style coding agent for your terminal. It runs an
agentic tool-use loop against the **Anthropic Messages API** or any
**OpenAI-compatible** provider, with built-in file/shell tools, skills, and
project-scoped memory.

## Install

```bash
go install github.com/xautjzd/agent-cli/cmd/agent@latest
# or from a checkout:
go build -o agent ./cmd/agent
```

Requires **Go 1.22+**. The result is a single static binary named `agent`.

## Configure a provider

No vendor is assumed — you pick one. The quickest path is from inside a session:

```bash
agent            # starts even with nothing configured
> /provider      # lists every provider; picking one prompts for the API key and saves it
```

From the shell, the same thing without starting a session:

```bash
agent provider list            # what exists, and whether its credential is exported
agent provider use anthropic   # persists the choice (add a model to pin one)
```

Or point it at a vendor for one run with environment variables:

```bash
export AGENT_PROVIDER=anthropic ANTHROPIC_API_KEY=sk-ant-...
export AGENT_PROVIDER=openai   OPENAI_API_KEY=sk-...
export AGENT_PROVIDER=deepseek DEEPSEEK_API_KEY=sk-...
```

Most vendors ship as zero-config presets (Gemini, GLM, Kimi, MiniMax, Grok, Qwen,
OpenRouter, SiliconFlow, Ollama…) — naming one is the whole configuration. See
**[Providers & models](providers.md)**.

```bash
agent config init                      # write a starter ~/.agent/config.json (0600)
agent config set provider deepseek     # equivalent to "agent provider use deepseek"
agent config show                      # see the resolved config + file paths
```

## First run

```bash
# Interactive session in your project directory
cd my-project
agent

# Or run a single prompt and exit
agent -p "explain the structure of this repository"
```

Inside the interactive session, type `/` to see all commands, or just start
typing a request. Type `/help` for an in-session overview and `/exit` to quit.

## What to read next

- **[Interactive session](interactive-session.md)** — the TUI and every slash command.
- **[Configuration](configuration.md)** — how settings are resolved and every key.
- **[Built-in tools](tools.md)** — what the agent can do on your behalf.
- **[Permissions & sandbox](permissions.md)** — how dangerous operations are gated.

## Verify your setup

```bash
agent version        # prints the version
agent config show    # confirms provider, model, and which credential is set
```
