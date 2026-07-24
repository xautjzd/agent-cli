# Themes

The interactive UI ships with several built-in color themes.

## Usage

`/theme` opens a picker (arrow keys, current theme marked) that **previews each
theme live** as you scroll — the whole transcript recolors in real time:

- **Enter** keeps the highlighted theme (and persists it),
- **Esc** reverts to what you started with.

`/theme <name>` switches directly. `/theme <TAB>` completes theme names.

## Built-in themes

| Theme | Notes |
|---|---|
| `dark` (default), `light` | Use the terminal's own ANSI palette, so they track your terminal color scheme |
| `monochrome` | No colors — faint/italic attributes only (accessibility) |
| `daltonized` | Colorblind-friendly (blue for success, orange for error) |
| `dracula`, `tokyonight`, `catppuccin`, `gruvbox`, `nord`, `solarized` | Fixed true-color schemes |

Colors are **semantic roles** (accent, success, error, warning, muted, text,
border) resolved through the terminal's detected capability: true-color hex
degrades to 256- or 16-color automatically, and color is dropped entirely under
`NO_COLOR` or when output is piped.

## Configuration

Set it in config or live in `/config`:

```jsonc
{ "theme": "dracula" }
```

```bash
agent config set theme nord
```

The choice persists to the **global** config. A live re-color (the whole visible
transcript recoloring immediately) needs the interactive TUI; the piped/non-TTY
path simply applies and persists the value.
