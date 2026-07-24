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

Pick one provider and export its credential. DeepSeek is the default provider, so
just exporting its key is enough:

```bash
export DEEPSEEK_API_KEY=sk-...                                  # DeepSeek (default)
export AGENT_PROVIDER=anthropic ANTHROPIC_API_KEY=sk-ant-...    # or Anthropic
export AGENT_PROVIDER=openai   OPENAI_API_KEY=sk-...            # or OpenAI
```

Many other vendors ship as zero-config presets (GLM, Kimi, Qwen, OpenRouter,
SiliconFlow, Ollama…). See **[Providers & models](providers.md)**.

To persist a choice instead of exporting env vars every time:

```bash
agent config init                      # write a starter ~/.agent/config.json (0600)
agent config set provider deepseek
agent config set model deepseek-chat
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
