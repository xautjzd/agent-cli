package provider

import (
	"strconv"
	"strings"
)

// Extended thinking is not one feature with one switch: whether it can be
// turned off, and which strengths it accepts, differ per *model* — not even
// consistently within a vendor (kimi-k3 always thinks and takes low/high/max,
// while kimi-k2.6 takes no strength at all but can be switched off, and
// kimi-k2.7-code errors if you try to switch it off).
//
// This file is the single place that knowledge lives. Everything else — the
// /effort menu, its validation, and the wire encoding — derives from
// SupportedThinking, so adding a vendor means adding one case here (OCP).
//
// Sources: api-docs.deepseek.com/guides/thinking_mode,
// docs.bigmodel.cn/cn/guide/start/concept-param,
// platform.kimi.com/docs/guide/use-thinking-models,
// developers.openai.com/api/docs/guides/reasoning,
// docs.x.ai/developers/model-capabilities/text/reasoning,
// ai.google.dev/gemini-api/docs/thinking and .../docs/openai.

// disableStyle is how "no thinking" is spelled on an OpenAI-compatible wire.
type disableStyle int

const (
	// disableUnsupported means the model cannot stop thinking.
	disableUnsupported disableStyle = iota
	// disableThinkingType sends {"thinking": {"type": "disabled"}}
	// (DeepSeek, GLM 4.5+, Kimi K2.5/K2.6).
	disableThinkingType
	// disableEnableThinking sends "enable_thinking": false (DashScope/Qwen).
	disableEnableThinking
	// disableEffortNone sends "reasoning_effort": "none" (GPT-5 era).
	disableEffortNone
	// disableUnknown is the fallback for models this table does not
	// recognize: nothing is sent, because an unknown field fails the whole
	// request and a wrong guess is worse than an ignored switch.
	disableUnknown
)

// ThinkingSupport describes one model's extended-thinking controls.
type ThinkingSupport struct {
	// Thinks reports whether the model reasons before answering at all.
	// False means the effort setting has nothing to act on.
	Thinks bool
	// CanDisable reports whether thinking can be turned off.
	CanDisable bool
	// Levels are the strength levels the model accepts, ascending. Empty
	// means it thinks but exposes no strength control.
	Levels []Effort
	// disable is how CanDisable is expressed on the wire.
	disable disableStyle
}

// Supports reports whether a level can be selected for this model.
func (s ThinkingSupport) Supports(e Effort) bool {
	switch e {
	case EffortOff:
		return s.CanDisable
	case EffortAdaptive:
		// "Adaptive" means "send nothing, take the vendor default", which is
		// always available as long as the model thinks.
		return s.Thinks
	}
	for _, l := range s.Levels {
		if l == e {
			return true
		}
	}
	return false
}

// Options lists the levels /effort should offer for this model, in the fixed
// display order (see displayOrder) with the unsupported ones removed. It is
// empty for a model with no thinking at all.
func (s ThinkingSupport) Options() []Effort {
	if !s.Thinks {
		return nil
	}
	out := make([]Effort, 0, len(s.Levels)+2)
	for _, e := range displayOrder {
		if s.Supports(e) {
			out = append(out, e)
		}
	}
	return out
}

// clamp maps a level the model does not accept onto the nearest one it does,
// preferring a weaker level over a stronger one. It exists because a level can
// arrive from configuration written for a different model — the menu hides
// unsupported levels, but a config file is not a menu, and sending an
// unsupported value would fail the request.
func (s ThinkingSupport) clamp(e Effort) (Effort, bool) {
	if len(s.Levels) == 0 {
		return "", false
	}
	if s.Supports(e) {
		return e, true
	}
	want := e.rank()
	best := s.Levels[0]
	for _, l := range s.Levels {
		if l.rank() <= want {
			best = l
		}
	}
	return best, true
}

// SupportedThinking returns the thinking controls of a model, keyed by family.
// Namespaced identifiers ("deepseek/deepseek-v4-pro" on OpenRouter) resolve to
// the underlying family.
func SupportedThinking(model string) ThinkingSupport {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	// DeepSeek V4: toggle plus effort control. low/medium are mapped to high
	// and xhigh to max server-side, but all five are accepted.
	case strings.HasPrefix(m, "deepseek"):
		return ThinkingSupport{
			Thinks: true, CanDisable: true, disable: disableThinkingType,
			Levels: []Effort{EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax},
		}

	case strings.HasPrefix(m, "glm"):
		v := familyVersion(m, "glm")
		switch {
		case v >= 5.2: // reasoning_effort arrived with GLM-5.2
			return ThinkingSupport{
				Thinks: true, CanDisable: true, disable: disableThinkingType,
				Levels: []Effort{EffortMinimal, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax},
			}
		case v >= 4.5: // thinking, but toggle only
			return ThinkingSupport{Thinks: true, CanDisable: true, disable: disableThinkingType}
		}
		return ThinkingSupport{} // GLM-4.4 and older do not think

	case strings.HasPrefix(m, "kimi"), strings.HasPrefix(m, "moonshot"):
		switch {
		case strings.HasPrefix(m, "kimi-k3"):
			// Always thinks; strength only.
			return ThinkingSupport{
				Thinks: true, disable: disableUnsupported,
				Levels: []Effort{EffortLow, EffortHigh, EffortMax},
			}
		case strings.HasPrefix(m, "kimi-k2.7"):
			// Always thinks; "disabled" is an error, no strength control.
			return ThinkingSupport{Thinks: true, disable: disableUnsupported}
		case strings.HasPrefix(m, "kimi-k2"):
			// K2.5/K2.6: on by default, switchable, no strength control.
			return ThinkingSupport{Thinks: true, CanDisable: true, disable: disableThinkingType}
		}
		return ThinkingSupport{} // moonshot-v1 and older Kimi do not think

	case strings.HasPrefix(m, "qwen"):
		// DashScope switches thinking with enable_thinking; its strength knob
		// is a token budget rather than named levels, so none are offered.
		return ThinkingSupport{Thinks: true, CanDisable: true, disable: disableEnableThinking}

	case strings.HasPrefix(m, "gpt-5"):
		// The GPT-5 line spans the whole ladder; "none" is how it is switched
		// off. developers.openai.com/api/docs/guides/reasoning notes the exact
		// set is model-dependent, so a value a specific model rejects is a
		// possibility the clamp cannot catch — the ladder here is the family's.
		return ThinkingSupport{
			Thinks: true, CanDisable: true, disable: disableEffortNone,
			Levels: []Effort{EffortMinimal, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax},
		}

	case strings.HasPrefix(m, "gemini"):
		// Over the OpenAI-compatible surface every Gemini model takes
		// minimal/low/medium/high, which Google maps onto each model's own
		// thinking_level or budget (a model without "minimal" receives "low").
		// No value turns thinking off, and thinking_level/thinking_budget must
		// not be combined with reasoning_effort — so the effort ladder is the
		// only control used here.
		return ThinkingSupport{
			Thinks: true, disable: disableUnsupported,
			Levels: []Effort{EffortMinimal, EffortLow, EffortMedium, EffortHigh},
		}

	case strings.HasPrefix(m, "grok"):
		// Reasoning cannot be disabled on the Grok line
		// (docs.x.ai/developers/model-capabilities/text/reasoning), except on
		// the checkpoints that are non-reasoning models in their own right.
		// The multi-agent checkpoint adds xhigh, where the level selects how
		// many agents run rather than how long one thinks.
		switch {
		case strings.Contains(m, "non-reasoning"):
			return ThinkingSupport{}
		case strings.Contains(m, "multi-agent"):
			return ThinkingSupport{
				Thinks: true, disable: disableUnsupported,
				Levels: []Effort{EffortLow, EffortMedium, EffortHigh, EffortXHigh},
			}
		}
		return ThinkingSupport{
			Thinks: true, disable: disableUnsupported,
			Levels: []Effort{EffortLow, EffortMedium, EffortHigh},
		}

	case strings.HasPrefix(m, "claude"):
		// The Anthropic adapter turns these into thinking budgets; the wire
		// switch here is unused but the menu is driven by the same table.
		return ThinkingSupport{
			Thinks: true, CanDisable: true, disable: disableThinkingType,
			Levels: []Effort{EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax},
		}

	// Non-reasoning models: nothing to control.
	case strings.HasPrefix(m, "gpt-4"), strings.HasPrefix(m, "llama"):
		return ThinkingSupport{}
	}

	// Unknown model (a custom gateway, a model newer than this table): assume
	// the common OpenAI-compatible shape, but do not guess how to switch
	// thinking off — see disableUnknown.
	return ThinkingSupport{
		Thinks: true, CanDisable: true, disable: disableUnknown,
		Levels: []Effort{EffortLow, EffortMedium, EffortHigh},
	}
}

// EffortsFor lists the levels selectable for a model, for menus, completion
// and validation. An empty result means the model has no thinking controls.
func EffortsFor(model string) []Effort {
	return SupportedThinking(model).Options()
}

// familyVersion extracts the numeric version that follows a family prefix,
// e.g. "glm-5.2-air" → 5.2, "glm-5" → 5, returning 0 when there is none.
func familyVersion(model, prefix string) float64 {
	rest := strings.TrimPrefix(model, prefix)
	rest = strings.TrimLeft(rest, "-")
	end := 0
	for end < len(rest) && (rest[end] >= '0' && rest[end] <= '9' || rest[end] == '.') {
		end++
	}
	// A trailing dot belongs to the next segment, not the version.
	rest = strings.TrimSuffix(rest[:end], ".")
	v, err := strconv.ParseFloat(rest, 64)
	if err != nil {
		return 0
	}
	return v
}
