package repl

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/xautjzd/agent-cli/internal/config"
	"github.com/xautjzd/agent-cli/internal/permission"
	"github.com/xautjzd/agent-cli/internal/provider"
	"github.com/xautjzd/agent-cli/internal/theme"
)

// Interactive configuration, in the spirit of Claude Code's /config panel:
// pick a setting, type a value, choose where it persists. Values apply to
// the running session immediately.

// currentValue renders the live value of a settable key for the editor.
func (r *Repl) currentValue(key string) string {
	switch key {
	case "provider":
		return r.Cfg.Provider
	case "model":
		return r.Cfg.Model
	case "api_key":
		if r.Cfg.APIKey == "" {
			return "(not set)"
		}
		return "****"
	case "base_url":
		if r.Cfg.BaseURL == "" {
			return "(provider default)"
		}
		return r.Cfg.BaseURL
	case "max_turns":
		return strconv.Itoa(r.Cfg.MaxTurns)
	case "permission_mode":
		return string(r.permMode())
	case "goal_max_rounds":
		n := r.GoalMaxRounds
		if n <= 0 {
			n = defaultGoalRounds
		}
		return strconv.Itoa(n)
	case "thinking":
		if r.Cfg.Thinking == "" {
			return "adaptive"
		}
		return r.Cfg.Thinking
	case "vision_provider":
		return orDefault(r.Cfg.VisionProvider, "(none)")
	case "vision_model":
		return orDefault(r.Cfg.VisionModel, "(none)")
	case "auto_compact":
		if r.Cfg.AutoCompact == "off" {
			return "off"
		}
		return "on"
	case "context_limit":
		n := r.Cfg.ContextLimit
		if n <= 0 {
			n = config.DefaultContextLimit
		}
		return strconv.Itoa(n)
	case "bash_policy":
		if r.Cfg.BashPolicy == "strict" {
			return "strict"
		}
		return "standard"
	case "sandbox":
		return orDefault(r.Cfg.Sandbox, "off")
	case "theme":
		return orDefault(r.Cfg.Theme, theme.Default())
	}
	return ""
}

// configEdit runs the interactive editor: setting → value → scope.
//
// Key flow: the setting list is presented through the same picker used by
// /resume (arrow keys + search on a terminal, numbered list when piped);
// the chosen value is applied to the running session immediately and then
// persisted to the selected scope — or kept session-only.
func (r *Repl) configEdit(ctx context.Context) error {
	// Inside the full-screen TUI: an arrow-navigable settings overlay that
	// stays open so several settings can be changed in a row.
	if r.tuiSelect != nil {
		return r.configEditTUI(ctx)
	}
	// Legacy inline TTY path: the standalone live panel program.
	if r.useTUI {
		return r.runConfigPanel(ctx)
	}

	// Piped stdin (scripts, tests) falls back to the numbered select-then-
	// enter-value flow with an explicit scope.
	keys := config.Keys()
	items := make([]pickerItem, len(keys))
	for i, k := range keys {
		items[i] = pickerItem{
			label:      fmt.Sprintf("%-16s %s", k, r.currentValue(k)),
			filterText: k,
		}
	}

	var idx int
	{
		fmt.Fprintln(r.Out, "Settings:")
		for i, it := range items {
			fmt.Fprintf(r.Out, "  %2d. %s\n", i+1, it.label)
		}
		line, ok := r.readInput("Select a setting (Enter to cancel): ")
		if !ok {
			return errExit
		}
		choice := strings.TrimSpace(line)
		if choice == "" {
			return nil
		}
		n, err := strconv.Atoi(choice)
		if err != nil || n < 1 || n > len(items) {
			return fmt.Errorf("invalid selection %q", choice)
		}
		idx = n - 1
	}
	key := keys[idx]

	value, ok := r.readInput(fmt.Sprintf("New value for %s (current: %s): ", key, r.currentValue(key)))
	if !ok {
		return errExit
	}
	value = strings.TrimSpace(value)
	if value == "" {
		fmt.Fprintln(r.Out, "Cancelled.")
		return nil
	}

	scope, ok := r.readInput("Persist to [g]lobal / [p]roject / [s]ession only? (default g) ")
	if !ok {
		return errExit
	}
	return r.applySetting(ctx, key, value, strings.ToLower(strings.TrimSpace(scope)))
}

// configEditTUI is the arrow-driven settings editor shown inside the
// full-screen TUI: pick a setting with ↑/↓, then choose an enum value from a
// list or type a new value; changes apply live and persist to the global
// config. It stays open (loops) until Esc, mirroring the live panel.
func (r *Repl) configEditTUI(ctx context.Context) error {
	for {
		items := make([]pickerItem, len(configSettings))
		for i, s := range configSettings {
			items[i] = pickerItem{
				label:      fmt.Sprintf("%-26s %s", s.label, r.currentValue(s.key)),
				filterText: s.label + " " + s.key,
			}
		}
		idx, ok := r.tuiSelect("Settings — ↑↓ select · enter edit · esc done", items)
		if !ok {
			return nil
		}
		s := configSettings[idx]

		var value string
		if s.kind == kindEnum && len(s.choices) > 0 {
			cur := r.currentValue(s.key)
			choices := make([]pickerItem, len(s.choices))
			for i, c := range s.choices {
				label := c
				if c == cur {
					label += "  (current)"
				}
				choices[i] = pickerItem{label: label, filterText: c}
			}
			ci, ok := r.tuiSelect(s.label+":", choices)
			if !ok {
				continue
			}
			value = s.choices[ci]
		} else {
			v, ok := r.tuiAsk(fmt.Sprintf("New %s (current: %s):", s.label, r.currentValue(s.key)))
			if !ok {
				continue
			}
			value = strings.TrimSpace(v)
			if value == "" {
				continue
			}
		}

		if err := r.setConfigValue(ctx, s.key, value); err != nil {
			fmt.Fprintf(r.Out, "⚠ %s: %v\n", s.label, err)
		} else {
			fmt.Fprintf(r.Out, "✓ %s = %s (saved)\n", s.label, r.currentValue(s.key))
		}
	}
}

// applySetting applies a value to the running session and persists it
// according to scope ("g"/"global", "p"/"project", "s"/"session").
func (r *Repl) applySetting(ctx context.Context, key, value, scope string) error {
	if err := r.applyLive(ctx, key, value); err != nil {
		return err
	}
	switch scope {
	case "s", "session":
		fmt.Fprintf(r.Out, "%s = %s (session only, not persisted)\n", key, value)
		return nil
	case "p", "project":
		if err := config.SetScoped(config.ScopeProject, r.WorkDir, key, value); err != nil {
			return err
		}
		fmt.Fprintf(r.Out, "%s = %s (saved to %s)\n", key, value, config.ProjectPath(r.WorkDir))
		return nil
	default:
		if err := config.SetScoped(config.ScopeGlobal, "", key, value); err != nil {
			return err
		}
		path, _ := config.Path()
		fmt.Fprintf(r.Out, "%s = %s (saved to %s)\n", key, value, path)
		return nil
	}
}

// applyLive makes a setting take effect in the running session.
func (r *Repl) applyLive(ctx context.Context, key, value string) error {
	switch key {
	case "provider":
		return r.cmdProvider(ctx, value)
	case "model":
		r.Cfg.Model = value
		r.Agent.SetModel(value)
		return nil
	case "api_key":
		r.Cfg.APIKey = value
		return r.rebuildProvider()
	case "base_url":
		r.Cfg.BaseURL = value
		return r.rebuildProvider()
	case "max_turns":
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return fmt.Errorf("max_turns must be a positive integer")
		}
		r.Cfg.MaxTurns = n
		r.Agent.MaxTurns = n
		return nil
	case "permission_mode":
		switch value {
		case string(permission.ModeHITL):
			r.Mode = permission.ModeHITL
		case string(permission.ModeBypass):
			r.Mode = permission.ModeBypass
		default:
			return fmt.Errorf("permission_mode must be hitl or bypass")
		}
		return nil
	case "goal_max_rounds":
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return fmt.Errorf("goal_max_rounds must be a positive integer")
		}
		r.GoalMaxRounds = n
		return nil
	case "thinking":
		effort, ok := provider.ParseEffort(value)
		if !ok {
			return fmt.Errorf("thinking must be one of off, low, medium, high, adaptive")
		}
		r.Cfg.Thinking = string(effort)
		// Rebuild so the change takes effect now; ignore a rebuild error
		// (e.g. no credential yet) — the field is set and applies on the
		// next provider build regardless.
		_ = r.rebuildProvider()
		return nil
	case "vision_provider":
		r.Cfg.VisionProvider = value
		return nil
	case "vision_model":
		r.Cfg.VisionModel = value
		return nil
	case "auto_compact":
		if value != "on" && value != "off" {
			return fmt.Errorf("auto_compact must be on or off")
		}
		r.Cfg.AutoCompact = value
		r.Agent.AutoCompact = value != "off"
		return nil
	case "context_limit":
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return fmt.Errorf("context_limit must be a positive integer")
		}
		r.Cfg.ContextLimit = n
		r.Agent.ContextLimit = n
		return nil
	case "bash_policy":
		if value != "standard" && value != "strict" {
			return fmt.Errorf("bash_policy must be standard or strict")
		}
		r.Cfg.BashPolicy = value
		r.policyOrDefault().SetPosture(permission.Posture(value))
		return nil
	case "sandbox":
		if value != "off" && value != "on" && value != "auto" {
			return fmt.Errorf("sandbox must be off, on, or auto")
		}
		r.Cfg.Sandbox = value
		fmt.Fprintln(r.Out, "note: sandbox change takes effect on restart")
		return nil
	case "theme":
		if !theme.Has(value) {
			return fmt.Errorf("unknown theme %q", value)
		}
		r.switchTheme(value)
		return nil
	}
	return fmt.Errorf("unknown config key %q (valid: %v)", key, config.Keys())
}

// setConfigValue applies a value to the running session and persists it to
// the global config file — the write path for the interactive panel, which
// never interrupts to ask a scope.
func (r *Repl) setConfigValue(ctx context.Context, key, value string) error {
	if err := r.applyLive(ctx, key, value); err != nil {
		return err
	}
	return config.SetScoped(config.ScopeGlobal, "", key, value)
}

// rebuildProvider reconstructs the provider client after a credential or
// endpoint change so the new values take effect immediately.
func (r *Repl) rebuildProvider() error {
	p, err := r.Cfg.BuildProvider()
	if err != nil {
		return err
	}
	r.Agent.SetProvider(p, r.Cfg.Model)
	return nil
}
