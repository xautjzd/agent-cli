package provider

import "strings"

// Effort names how much reasoning a provider should spend before answering.
// It is a single vendor-neutral ladder that maps onto each backend's own
// control: Anthropic extended-thinking budgets and OpenAI/Codex-style
// reasoning_effort levels are both derived from it, so the rest of the app
// configures "effort" once and each provider translates it (DIP).
type Effort string

const (
	// EffortOff disables reasoning entirely where the provider allows it.
	EffortOff Effort = "off"
	// EffortLow/Medium/High are the graduated budgets, mirroring the
	// low/medium/high levels Codex and Claude Code expose.
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	// EffortAdaptive lets the model choose its own budget (Anthropic adaptive
	// thinking); for backends without that mode it behaves like the default.
	EffortAdaptive Effort = "adaptive"
)

// Efforts lists the selectable levels in ascending order, for menus, help
// text, and completion.
func Efforts() []Effort {
	return []Effort{EffortOff, EffortLow, EffortMedium, EffortHigh, EffortAdaptive}
}

// ParseEffort normalizes a user- or config-supplied level. It accepts a few
// aliases so the field is forgiving ("on"/"auto" and the empty string mean
// adaptive, "none" means off, "max" means high). ok is false for an
// unrecognized value so callers can surface a clear error.
func ParseEffort(s string) (Effort, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "adaptive", "on", "auto":
		return EffortAdaptive, true
	case "off", "none", "disabled":
		return EffortOff, true
	case "low", "minimal":
		return EffortLow, true
	case "medium", "mid":
		return EffortMedium, true
	case "high", "max":
		return EffortHigh, true
	}
	return "", false
}

// Describe returns a short human-readable explanation of a level, used by the
// /effort command and help output.
func (e Effort) Describe() string {
	switch e {
	case EffortOff:
		return "no extended thinking — fastest, cheapest"
	case EffortLow:
		return "brief reasoning for simple problems"
	case EffortMedium:
		return "moderate reasoning for everyday tasks"
	case EffortHigh:
		return "deep reasoning for hard problems — slowest"
	case EffortAdaptive:
		return "let the model choose its own budget (default)"
	}
	return ""
}

// budgetTokens returns the Anthropic thinking budget for a level, or 0 when
// the level is not budget-based (off and adaptive carry no fixed budget).
func (e Effort) budgetTokens() int {
	switch e {
	case EffortLow:
		return 4000
	case EffortMedium:
		return 10000
	case EffortHigh:
		return 24000
	}
	return 0
}

// reasoningEffort returns the OpenAI-compatible reasoning_effort value for a
// level, or "" when nothing should be sent — off and adaptive leave the
// endpoint's own default in place, and, crucially, keep the parameter off
// requests to non-reasoning models that would reject it.
func (e Effort) reasoningEffort() string {
	switch e {
	case EffortLow:
		return "low"
	case EffortMedium:
		return "medium"
	case EffortHigh:
		return "high"
	}
	return ""
}
