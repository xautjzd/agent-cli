package provider

import "strings"

// Effort names how much reasoning a provider should spend before answering.
// It is a single vendor-neutral ladder that maps onto each backend's own
// control: Anthropic extended-thinking budgets and OpenAI/Codex-style
// reasoning_effort levels are both derived from it, so the rest of the app
// configures "effort" once and each provider translates it (DIP).
type Effort string

const (
	// EffortOff disables reasoning entirely where the model allows it.
	EffortOff Effort = "off"
	// EffortMinimal through EffortMax are the graduated levels. The ladder is
	// a superset of what any one vendor exposes — GLM-5.2 spans minimal…max,
	// OpenAI stops at high, Kimi K3 offers only low/high/max — so a model's
	// actual choices come from SupportedThinking, not from this list.
	EffortMinimal Effort = "minimal"
	EffortLow     Effort = "low"
	EffortMedium  Effort = "medium"
	EffortHigh    Effort = "high"
	EffortXHigh   Effort = "xhigh"
	EffortMax     Effort = "max"
	// EffortAdaptive sends no strength at all, leaving the vendor default in
	// place (Anthropic reads it as adaptive thinking).
	EffortAdaptive Effort = "adaptive"
)

// Efforts lists every level in display order. It is the full ladder; use
// EffortsFor(model) for the subset a given model accepts, which is this same
// order with the unsupported levels removed.
func Efforts() []Effort {
	return append([]Effort(nil), displayOrder...)
}

// displayOrder fixes how levels are presented everywhere — menu, completion,
// error messages — regardless of model: the default first, then the strength
// ladder ascending, then off. A model that lacks a level drops out of the list
// without shifting the rest, so a given level keeps its position from one
// model to the next and the menu never reshuffles under the user.
var displayOrder = []Effort{
	EffortAdaptive,
	EffortMinimal, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax,
	EffortOff,
}

// rank orders the ladder so an unsupported level can be clamped to the
// nearest supported one. Off and adaptive are not on the strength scale.
func (e Effort) rank() int {
	switch e {
	case EffortMinimal:
		return 1
	case EffortLow:
		return 2
	case EffortMedium:
		return 3
	case EffortHigh:
		return 4
	case EffortXHigh:
		return 5
	case EffortMax:
		return 6
	}
	return 0
}

// ParseEffort normalizes a user- or config-supplied level. It accepts the
// vendor spellings so a value copied from any of their docs works ("none" is
// off, "very-high" is xhigh). ok is false for an unrecognized value so callers
// can surface a clear error.
func ParseEffort(s string) (Effort, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "adaptive", "on", "auto":
		return EffortAdaptive, true
	case "off", "none", "disabled":
		return EffortOff, true
	case "minimal", "min":
		return EffortMinimal, true
	case "low":
		return EffortLow, true
	case "medium", "mid":
		return EffortMedium, true
	case "high":
		return EffortHigh, true
	case "xhigh", "very-high", "veryhigh":
		return EffortXHigh, true
	case "max", "maximum":
		return EffortMax, true
	}
	return "", false
}

// Describe returns a short human-readable explanation of a level, used by the
// /effort command and help output.
func (e Effort) Describe() string {
	switch e {
	case EffortOff:
		return "no extended thinking — fastest, cheapest"
	case EffortMinimal:
		return "the shortest reasoning the model offers"
	case EffortLow:
		return "brief reasoning for simple problems"
	case EffortMedium:
		return "moderate reasoning for everyday tasks"
	case EffortHigh:
		return "deep reasoning for hard problems"
	case EffortXHigh:
		return "longer reasoning than high"
	case EffortMax:
		return "the model's longest reasoning — slowest, priciest"
	case EffortAdaptive:
		return "leave it to the model's own default"
	}
	return ""
}

// budgetTokens returns the Anthropic thinking budget for a level, or 0 when
// the level is not budget-based (off and adaptive carry no fixed budget).
func (e Effort) budgetTokens() int {
	switch e {
	case EffortMinimal:
		return 1024 // Anthropic's minimum
	case EffortLow:
		return 4000
	case EffortMedium:
		return 10000
	case EffortHigh:
		return 24000
	case EffortXHigh:
		return 32000
	case EffortMax:
		return 64000
	}
	return 0
}

// reasoningEffort returns the OpenAI-compatible reasoning_effort value for a
// level, or "" when nothing should be sent — off and adaptive leave the
// endpoint's own default in place. The vendor spellings match the ladder, so
// this is a filter rather than a translation: what varies per model is *which*
// levels are accepted, which SupportedThinking decides.
func (e Effort) reasoningEffort() string {
	if e.rank() == 0 {
		return ""
	}
	return string(e)
}
