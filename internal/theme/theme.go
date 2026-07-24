// Package theme centralizes the terminal colors the CLI renders with, exposing
// them as semantic roles (accent, success, error, …) rather than raw ANSI
// codes. A handful of built-in themes are registered here; the user picks one
// with the /theme command or the "theme" config key, and every render site
// reads theme.Current() so a switch takes effect everywhere at once.
//
// Colors are compiled to ANSI SGR sequences through termenv, which degrades
// true-color hex down to 256- or 16-color palettes based on the detected
// terminal, and emits nothing at all when color is disabled (NO_COLOR, a pipe,
// or a non-terminal test buffer). That keeps piped output clean for free.
package theme

import (
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Palette defines a theme's colors. Each field is either a hex string
// ("#7aa2f7") or an ANSI palette index ("6"); an index tracks the user's own
// terminal color scheme, while hex pins an exact shade. An empty string means
// "no color" — the role falls back to attributes only (used by "monochrome").
type Palette struct {
	Accent  string // prompt marker, headings, links, tool names, input frame
	Success string // succeeded tool calls, added diff lines
	Error   string // failed tool calls, removed diff lines, denials
	Warning string // running tools, approval prompts, warnings
	Muted   string // dimmed secondary text: previews, hints, stats
	Border  string // input box border (unused when a theme frames with Accent)
	Text    string // assistant answer body tint; "" keeps the terminal default
}

// Theme is a resolved palette: the trailing fields hold ready-to-print SGR
// open-sequences (empty when color is disabled), so callers concatenate them
// directly or go through Paint.
type Theme struct {
	Name        string
	Description string
	pal         Palette

	Reset    string // sequence that clears styling ("\033[0m", or "" when disabled)
	Accent   string
	Success  string
	Error    string
	Warning  string
	Muted    string // muted color + faint attribute
	Thinking string // muted color + faint + italic (chain-of-thought)
	Text     string // assistant answer body tint ("" = terminal default)
}

// Paint wraps s in the given role sequence, resetting after. When the sequence
// is empty (color disabled) it returns s unchanged, so it is always safe.
func (t Theme) Paint(seq, s string) string {
	if seq == "" {
		return s
	}
	return seq + s + t.Reset
}

// AccentColor and BorderColor expose palette colors to lipgloss-styled UI (the
// input box, selection marker). lipgloss performs its own capability
// degradation, matching the SGR path above.
func (t Theme) AccentColor() lipgloss.TerminalColor { return lipgloss.Color(t.pal.Accent) }
func (t Theme) BorderColor() lipgloss.TerminalColor { return lipgloss.Color(t.pal.Border) }

// definition pairs a palette with its human description, in registry order.
type definition struct {
	name string
	desc string
	pal  Palette
}

// definitions lists the built-in themes in display order. Adaptive themes use
// ANSI indices so they inherit the terminal's own palette; the named schemes
// pin true-color hex values.
var definitions = []definition{
	{"dark", "Terminal ANSI colors, tuned for a dark background (default)", Palette{
		Accent: "6", Success: "2", Error: "1", Warning: "3", Muted: "8", Border: "8", Text: ""}},
	{"light", "Terminal ANSI colors, tuned for a light background", Palette{
		Accent: "5", Success: "2", Error: "1", Warning: "3", Muted: "8", Border: "7", Text: ""}},
	{"monochrome", "No colors — faint/italic attributes only (accessibility)", Palette{
		Accent: "", Success: "", Error: "", Warning: "", Muted: "", Border: "", Text: ""}},
	{"daltonized", "Colorblind-friendly: blue for success, orange for error", Palette{
		Accent: "#3b82f6", Success: "#3b82f6", Error: "#e69f00", Warning: "#e69f00", Muted: "8", Border: "#3b82f6", Text: ""}},
	{"dracula", "The Dracula color scheme", Palette{
		Accent: "#bd93f9", Success: "#50fa7b", Error: "#ff5555", Warning: "#f1fa8c", Muted: "#6272a4", Border: "#bd93f9", Text: "#f8f8f2"}},
	{"tokyonight", "The Tokyo Night color scheme", Palette{
		Accent: "#7aa2f7", Success: "#9ece6a", Error: "#f7768e", Warning: "#e0af68", Muted: "#565f89", Border: "#7aa2f7", Text: "#c0caf5"}},
	{"catppuccin", "Catppuccin Mocha", Palette{
		Accent: "#89b4fa", Success: "#a6e3a1", Error: "#f38ba8", Warning: "#f9e2af", Muted: "#6c7086", Border: "#89b4fa", Text: "#cdd6f4"}},
	{"gruvbox", "The Gruvbox color scheme", Palette{
		Accent: "#fabd2f", Success: "#b8bb26", Error: "#fb4934", Warning: "#fe8019", Muted: "#928374", Border: "#fabd2f", Text: "#ebdbb2"}},
	{"nord", "The Nord color scheme", Palette{
		Accent: "#88c0d0", Success: "#a3be8c", Error: "#bf616a", Warning: "#ebcb8b", Muted: "#616e88", Border: "#88c0d0", Text: "#d8dee9"}},
	{"solarized", "Solarized", Palette{
		Accent: "#268bd2", Success: "#859900", Error: "#dc322f", Warning: "#b58900", Muted: "#93a1a1", Border: "#268bd2", Text: "#839496"}},
}

const defaultName = "dark"

var (
	mu sync.RWMutex
	// profile is the detected terminal color capability; it decides how hex
	// degrades and whether any color is emitted at all. Overridable in tests.
	profile = termenv.EnvColorProfile()
	current = build(byName(defaultName))
)

func byName(name string) definition {
	for _, d := range definitions {
		if d.name == name {
			return d
		}
	}
	return definitions[0]
}

// colorSeq returns the SGR color parameters for a palette color under the
// active profile ("" for an empty color, an invalid one, or when color is off).
func colorSeq(color string) string {
	if color == "" {
		return ""
	}
	c := profile.Color(color)
	if c == nil {
		return ""
	}
	return c.Sequence(false)
}

// seq builds a full SGR open-sequence from optional attributes plus a color.
// It returns "" when nothing would be styled or when color is disabled entirely.
func seq(color string, attrs ...string) string {
	if profile == termenv.Ascii {
		return ""
	}
	parts := append([]string{}, attrs...)
	if cs := colorSeq(color); cs != "" {
		parts = append(parts, cs)
	}
	if len(parts) == 0 {
		return ""
	}
	return "\033[" + strings.Join(parts, ";") + "m"
}

func build(d definition) Theme {
	reset := "\033[0m"
	if profile == termenv.Ascii {
		reset = ""
	}
	// The faint attribute (SGR 2) is only a fallback for themes with no muted
	// color of their own (monochrome): there it is the sole cue distinguishing
	// secondary text. When a muted color exists, the color already carries the
	// dimming, and stacking faint on top renders hints and labels nearly
	// invisible — so we drop it and let the color do the work, matching how
	// mainstream agents keep dim text legible. Thinking stays italic regardless.
	mutedAttrs := []string{}
	thinkingAttrs := []string{"3"}
	if d.pal.Muted == "" {
		mutedAttrs = []string{"2"}
		thinkingAttrs = []string{"2", "3"}
	}
	return Theme{
		Name:        d.name,
		Description: d.desc,
		pal:         d.pal,
		Reset:       reset,
		Accent:      seq(d.pal.Accent),
		Success:     seq(d.pal.Success),
		Error:       seq(d.pal.Error),
		Warning:     seq(d.pal.Warning),
		Muted:       seq(d.pal.Muted, mutedAttrs...),
		Thinking:    seq(d.pal.Muted, thinkingAttrs...),
		Text:        seq(d.pal.Text),
	}
}

// Names returns the built-in theme names in display order.
func Names() []string {
	names := make([]string, len(definitions))
	for i, d := range definitions {
		names[i] = d.name
	}
	return names
}

// Has reports whether name is a known theme.
func Has(name string) bool {
	for _, d := range definitions {
		if d.name == name {
			return true
		}
	}
	return false
}

// Get resolves a theme by name without changing the current one.
func Get(name string) (Theme, bool) {
	for _, d := range definitions {
		if d.name == name {
			return build(d), true
		}
	}
	return Theme{}, false
}

// Default returns the default theme name.
func Default() string { return defaultName }

// Current returns the active theme.
func Current() Theme {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// Set switches the active theme, returning false (and leaving it unchanged)
// for an unknown name.
func Set(name string) bool {
	t, ok := Get(name)
	if !ok {
		return false
	}
	mu.Lock()
	current = t
	mu.Unlock()
	return true
}

// SetColorProfile overrides the detected terminal color capability and rebuilds
// the active theme. The default is auto-detected from the environment
// (DetectedProfile); forcing a profile is useful for tests and for a future
// --color flag.
func SetColorProfile(p termenv.Profile) {
	mu.Lock()
	profile = p
	current = build(byName(current.Name))
	mu.Unlock()
}

// DetectedProfile returns the color profile auto-detected from the environment,
// so callers can restore it after forcing another.
func DetectedProfile() termenv.Profile { return termenv.EnvColorProfile() }

// ActiveProfile returns the color capability the active theme was built with.
// Unlike DetectedProfile it reflects any override set via SetColorProfile, so a
// render site can pick a matching syntax-highlighter formatter (or skip color
// entirely when it is termenv.Ascii).
func ActiveProfile() termenv.Profile {
	mu.RLock()
	defer mu.RUnlock()
	return profile
}
