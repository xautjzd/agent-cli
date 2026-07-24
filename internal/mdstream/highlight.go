package mdstream

import (
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/muesli/termenv"

	"github.com/xautjzd/agent-cli/internal/theme"
)

// highlight returns code rendered with ANSI syntax colors for the given
// language, or the code unchanged when color is disabled or highlighting is not
// possible. lang is the fence's info string ("go", "python", …); an empty or
// unknown lang falls back to content analysis and finally to plain text.
func highlight(code, lang string) string {
	hl := highlighter(lang, "", code)
	if hl == nil {
		return code
	}
	return hl(code)
}

// highlighter resolves a lexer/style/formatter once and returns a closure that
// syntax-highlights a fragment of source, so a diff can highlight many lines
// without re-resolving per line. It returns nil when highlighting is
// unavailable — color disabled, or no lexer could be found for lang, filename,
// or (as a last resort) the sample content. The chroma style and terminal
// formatter track the active theme so highlighted code belongs with the rest of
// the CLI's output rather than clashing with the user's palette.
func highlighter(lang, filename, sample string) func(string) string {
	formatterName := terminalFormatter(theme.ActiveProfile())
	if formatterName == "" {
		return nil // color disabled.
	}

	var lexer chroma.Lexer
	if lang != "" {
		lexer = lexers.Get(lang)
	}
	if lexer == nil && filename != "" {
		lexer = lexers.Match(filename)
	}
	if lexer == nil && sample != "" {
		lexer = lexers.Analyse(sample)
	}
	if lexer == nil {
		return nil
	}
	lexer = chroma.Coalesce(lexer)

	style := styles.Get(chromaStyle(theme.Current().Name))
	if style == nil {
		style = styles.Fallback
	}
	formatter := formatters.Get(formatterName)
	if formatter == nil {
		formatter = formatters.Fallback
	}

	return func(code string) string {
		iterator, err := lexer.Tokenise(nil, code)
		if err != nil {
			return code
		}
		var sb strings.Builder
		if err := formatter.Format(&sb, style, iterator); err != nil {
			return code
		}
		// The formatter appends a trailing newline; strip it so callers
		// control line breaks (a single diff line must stay one line).
		return strings.TrimSuffix(sb.String(), "\n")
	}
}

// terminalFormatter maps a terminal color capability to the chroma formatter
// that emits matching escape codes. Ascii (color off) returns "" so callers
// skip highlighting entirely.
func terminalFormatter(p termenv.Profile) string {
	switch p {
	case termenv.TrueColor:
		return "terminal16m"
	case termenv.ANSI256:
		return "terminal256"
	case termenv.ANSI:
		return "terminal16"
	default:
		return ""
	}
}

// chromaStyle maps a theme name to the closest built-in chroma style, so
// highlighted code tracks the user's chosen scheme where an equivalent exists
// and otherwise falls back to a neutral light/dark GitHub style.
func chromaStyle(themeName string) string {
	switch themeName {
	case "light":
		return "github"
	case "dracula":
		return "dracula"
	case "tokyonight":
		return "tokyonight-storm"
	case "catppuccin":
		return "catppuccin-mocha"
	case "gruvbox":
		return "gruvbox"
	case "nord":
		return "nord"
	case "solarized":
		return "solarized-dark"
	default:
		// dark, daltonized, monochrome (unreached — color is off) and any
		// future dark theme.
		return "github-dark"
	}
}
