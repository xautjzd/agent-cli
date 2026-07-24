package repl

import (
	"context"
	"fmt"
	"strings"

	"github.com/xautjzd/agent-cli/internal/config"
	"github.com/xautjzd/agent-cli/internal/provider"
	"github.com/xautjzd/agent-cli/internal/theme"
)

// cmdEffort implements /effort: show or switch the reasoning-effort level that
// governs extended thinking. With no argument it lists the levels and marks the
// active one; with an argument it switches, applies the change to the running
// provider, and persists it — mirroring how Claude Code and Codex expose a
// quick thinking/effort toggle.
func (r *Repl) cmdEffort(_ context.Context, args string) error {
	current, _ := provider.ParseEffort(r.Cfg.Thinking)

	arg := strings.TrimSpace(args)
	if arg == "" {
		th := theme.Current()
		fmt.Fprintf(r.Out, "reasoning effort: %s\n", th.Paint(th.Accent, string(current)))
		for _, e := range provider.Efforts() {
			marker := "  "
			if e == current {
				marker = th.Paint(th.Accent, "▸ ")
			}
			fmt.Fprintf(r.Out, "%s%-9s %s\n", marker, e, e.Describe())
		}
		return nil
	}

	effort, ok := provider.ParseEffort(arg)
	if !ok {
		return fmt.Errorf("unknown effort %q (use off, low, medium, high, or adaptive)", arg)
	}

	r.Cfg.Thinking = string(effort)
	// Rebuild so the change takes effect on the next turn; a rebuild error
	// (e.g. no credential yet) is non-fatal — the field is stored and applies
	// on the next successful provider build regardless.
	_ = r.rebuildProvider()

	th := theme.Current()
	fmt.Fprintf(r.Out, "%s\n", th.Paint(th.Accent, "✓ reasoning effort set to "+string(effort)))
	fmt.Fprintf(r.Out, "  %s\n", th.Paint(th.Muted, effort.Describe()))
	// Persist to the global config so the choice survives restarts.
	if err := config.SetScoped(config.ScopeGlobal, r.WorkDir, "thinking", string(effort)); err != nil {
		fmt.Fprintf(r.Out, "%s\n", th.Paint(th.Warning, "  (not saved: "+err.Error()+")"))
	}
	return nil
}
