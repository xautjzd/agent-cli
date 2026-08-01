package repl

import (
	"context"
	"fmt"
	"strings"

	"github.com/xautjzd/agent-cli/internal/config"
	"github.com/xautjzd/agent-cli/internal/theme"
)

// cmdTheme lists or switches the color theme. "/theme" opens a picker of the
// built-in themes (current one marked); "/theme <name>" switches directly. The
// choice is applied live, persisted to the global config, and — in the
// full-screen TUI — the whole visible transcript is re-colored by replaying it
// through the newly themed event sink.
func (r *Repl) cmdTheme(_ context.Context, args string) error {
	name := strings.TrimSpace(args)
	if name == "" {
		names := theme.Names()
		labels := make([]string, len(names))
		original := theme.Current().Name
		for i, n := range names {
			th, _ := theme.Get(n)
			labels[i] = fmt.Sprintf("%-11s %s", n, th.Description)
			if n == original {
				labels[i] += "  (current)"
			}
		}
		// When the TUI can preview, apply each highlighted theme live so the
		// whole UI recolors as you scroll; a cancel reverts to the original.
		if r.tuiSelectPreview != nil {
			items := make([]pickerItem, len(labels))
			for i, l := range labels {
				items[i] = pickerItem{label: l, filterText: l}
			}
			idx, ok := r.tuiSelectPreview("Choose a theme (previews live · enter to keep · esc to cancel)",
				items, func(i int) { r.switchTheme(names[i]) })
			if !ok {
				r.switchTheme(original) // revert the preview
				return nil
			}
			name = names[idx]
		} else {
			idx, ok := r.selectIndex("Choose a theme", labels)
			if !ok {
				return nil
			}
			name = names[idx]
		}
	}

	if !theme.Has(name) {
		return fmt.Errorf("unknown theme %q; choose one of %s", name, strings.Join(theme.Names(), ", "))
	}

	r.switchTheme(name)

	// Persist to the global config so the choice survives restarts. A write
	// failure is surfaced but does not abort the live switch.
	th := theme.Current()
	fmt.Fprintf(r.Out, "%s\n", th.Paint(th.Accent, "✓ theme set to "+name))
	if err := config.SetScoped(config.ScopeGlobal, r.WorkDir, "theme", name); err != nil {
		fmt.Fprintf(r.Out, "%s\n", th.Paint(th.Warning, "  (not saved: "+err.Error()+")"))
	}
	return nil
}

// switchTheme applies a theme to the running session: it activates the palette,
// restyles the input UI, and — in the full-screen TUI — re-colors the visible
// transcript by clearing the scrollback and replaying it through the newly
// themed event sink. It does not persist; callers decide whether to save.
func (r *Repl) switchTheme(name string) {
	theme.Set(name)
	r.Cfg.Theme = name
	applyThemeStyles()

	r.redrawTranscript()
}

// redrawTranscript rebuilds the full-screen scrollback from scratch — banner
// then the current conversation — so header info (provider/model/effort/context
// and theme) is reflected immediately. Build it off-screen and replace the
// scrollback atomically: refresh notifications can otherwise render the banner
// midway through replay and briefly leave duplicate/stale terminal output.
// No-op outside the TUI.
func (r *Repl) redrawTranscript() {
	if r.sb == nil {
		return
	}

	var rebuilt strings.Builder
	r.printBanner(&rebuilt)
	func() {
		oldEvents := r.Agent.Events
		r.Agent.Events = newTUIEvents(&rebuilt)
		defer func() { r.Agent.Events = oldEvents }()
		r.replayTranscript(r.buildRecords())
	}()
	r.sb.Replace(rebuilt.String())
}
