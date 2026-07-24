# Code navigation (LSP)

The agent understands code the way an editor does, through **Language Server
Protocol** servers. This beats grep because the server understands imports, scope,
and shadowing.

## Tools

| Tool | Purpose |
|---|---|
| `lsp_diagnostics` | Compiler/linter errors & warnings for a file |
| `lsp_references` | Every reference to a symbol (scope-aware) |
| `lsp_definition` | Where a symbol is defined |
| `lsp_hover` | A symbol's type signature and documentation |

The model is prompted to run `lsp_diagnostics` after editing a file — catching a
broken edit before it moves on — and to use references/definition before changing
or removing a symbol. It falls back to grep when no server is available.

The reference/definition/hover tools take a `path`, the 1-based `line` the symbol is
on (as shown by `read_file`), and the `symbol` text — the model never has to
compute columns.

## Servers

Servers start **lazily on first use** and are routed by file extension. Built-in
defaults (used when the binary is installed):

| Language | Server |
|---|---|
| Go | `gopls` |
| TypeScript/JavaScript | `typescript-language-server` |
| Python | `pyright` |
| Rust | `rust-analyzer` |
| C/C++ | `clangd` |

Run **`/lsp`** to list them and see which are installed and running. A missing
binary is reported as "not installed" — the tools simply fall back to grep.

## Configuration

Add or override a server with the `lspServers` key:

```json
{
  "lspServers": {
    "go":  {"command": "gopls"},
    "zig": {"command": "zls", "extensions": [".zig"]}
  }
}
```

| Field | Meaning |
|---|---|
| `command`, `args`, `env` | The language-server process to launch |
| `extensions` | File extensions this server handles (omit to keep the built-in ones) |
| `disabled` | Skip routing to this server |

Only the fields you set override the default — e.g. `{"command": "gopls"}` keeps the
default Go extensions.

## Example configurations

### Add languages that aren't built in

```json
{
  "lspServers": {
    "zig":   {"command": "zls",                    "extensions": [".zig"]},
    "ruby":  {"command": "ruby-lsp",               "extensions": [".rb"]},
    "lua":   {"command": "lua-language-server",     "extensions": [".lua"]},
    "bash":  {"command": "bash-language-server", "args": ["start"], "extensions": [".sh", ".bash"]}
  }
}
```

### Point a built-in language at a different binary or flags

```json
{
  "lspServers": {
    // Use a project-pinned gopls and pass a flag; Go extensions stay the default
    "go": {"command": "./bin/gopls", "args": ["-remote=auto"]},

    // Swap Python's server for basedpyright
    "python": {"command": "basedpyright-langserver", "args": ["--stdio"]},

    // Disable Rust entirely (fall back to grep for .rs files)
    "rust": {"command": "rust-analyzer", "disabled": true}
  }
}
```

The **key** (`go`, `python`, …) matches a built-in language to inherit its defaults;
use any name for a brand-new language and give it `extensions`.

### Installing the servers

The built-in defaults assume these binaries are on `PATH`:

| Language | Install |
|---|---|
| Go | `go install golang.org/x/tools/gopls@latest` |
| TypeScript/JS | `npm i -g typescript typescript-language-server` |
| Python | `npm i -g pyright` (or `pip install pyright`) |
| Rust | `rustup component add rust-analyzer` |
| C/C++ | install `clangd` (LLVM) via your package manager |

Run `/lsp` to see which are detected as installed vs. missing.
