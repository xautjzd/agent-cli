package provider

import (
	"context"
	"strings"
	"testing"
)

// The menu a user sees must match what the model actually accepts, in one
// fixed order — adaptive, the strength ladder, then off — with unsupported
// levels dropped rather than the list reordered. These
// expectations come from the vendors' own docs: DeepSeek thinking-mode guide,
// docs.bigmodel.cn core parameters, platform.kimi.com thinking models, and
// developers.openai.com reasoning guide.
func TestEffortsForModel(t *testing.T) {
	cases := []struct {
		model string
		want  []Effort
	}{
		// GLM-5.2 introduced reasoning_effort; 4.5+ can only toggle.
		{"glm-5.2", []Effort{EffortAdaptive, EffortMinimal, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax, EffortOff}},
		{"glm-4.6", []Effort{EffortAdaptive, EffortOff}},
		{"glm-4.5-air", []Effort{EffortAdaptive, EffortOff}},
		{"glm-4-long", nil}, // predates thinking entirely

		// Kimi differs *within* the vendor: K3 always thinks but takes a
		// strength, K2.7-code cannot be switched off and takes no strength,
		// K2.5/K2.6 are the opposite.
		{"kimi-k3", []Effort{EffortAdaptive, EffortLow, EffortHigh, EffortMax}},
		{"kimi-k2.7-code", []Effort{EffortAdaptive}},
		{"kimi-k2.7-code-highspeed", []Effort{EffortAdaptive}},
		{"kimi-k2.6", []Effort{EffortAdaptive, EffortOff}},
		{"kimi-k2.5", []Effort{EffortAdaptive, EffortOff}},
		{"moonshot-v1-128k", nil},

		{"deepseek-v4-pro", []Effort{EffortAdaptive, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax, EffortOff}},
		{"deepseek/deepseek-v4-pro", []Effort{EffortAdaptive, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax, EffortOff}},
		{"gpt-5.6-terra", []Effort{EffortAdaptive, EffortMinimal, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax, EffortOff}},
		{"gpt-4o", nil},

		// Grok reasons unconditionally; only the multi-agent checkpoint takes
		// xhigh, and the non-reasoning checkpoint is a plain model.
		{"grok-4.5", []Effort{EffortAdaptive, EffortLow, EffortMedium, EffortHigh}},
		{"grok-4.20-0309-reasoning", []Effort{EffortAdaptive, EffortLow, EffortMedium, EffortHigh}},
		{"grok-4.20-multi-agent-0309", []Effort{EffortAdaptive, EffortLow, EffortMedium, EffortHigh, EffortXHigh}},
		{"grok-4.20-0309-non-reasoning", nil},

		// Gemini always thinks; the OpenAI surface takes minimal…high and
		// Google maps each onto the model's own level or budget.
		{"gemini-3.6-flash", []Effort{EffortAdaptive, EffortMinimal, EffortLow, EffortMedium, EffortHigh}},
		{"gemini-2.5-pro", []Effort{EffortAdaptive, EffortMinimal, EffortLow, EffortMedium, EffortHigh}},
	}
	for _, tc := range cases {
		got := EffortsFor(tc.model)
		if len(got) != len(tc.want) {
			t.Errorf("EffortsFor(%s) = %v, want %v", tc.model, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("EffortsFor(%s) = %v, want %v", tc.model, got, tc.want)
				break
			}
		}
	}
}

// A model that cannot stop thinking must not be told to, and a model with no
// strength control must not be sent one.
func TestSupportsRejectsUnavailableLevels(t *testing.T) {
	if SupportedThinking("kimi-k3").Supports(EffortOff) {
		t.Error("kimi-k3 always thinks; off must not be selectable")
	}
	if SupportedThinking("kimi-k2.7-code").Supports(EffortOff) {
		t.Error("kimi-k2.7-code errors on thinking disabled; off must not be selectable")
	}
	if SupportedThinking("kimi-k2.6").Supports(EffortHigh) {
		t.Error("kimi-k2.6 has no strength control; high must not be selectable")
	}
	if !SupportedThinking("kimi-k2.6").Supports(EffortOff) {
		t.Error("kimi-k2.6 can be switched off")
	}
	if !SupportedThinking("gpt-5.6-terra").Supports(EffortMax) {
		t.Error("the GPT-5 line accepts max")
	}
	if SupportedThinking("gpt-4o").Supports(EffortLow) {
		t.Error("gpt-4o does not reason; no level is selectable")
	}
}

func TestFamilyVersion(t *testing.T) {
	cases := map[string]float64{
		"glm-5.2": 5.2, "glm-5.2-air": 5.2, "glm-5": 5, "glm-5-turbo": 5,
		"glm-4.7-flashx": 4.7, "glm-4.5-air": 4.5, "glm": 0,
	}
	for model, want := range cases {
		if got := familyVersion(model, "glm"); got != want {
			t.Errorf("familyVersion(%q) = %v, want %v", model, got, want)
		}
	}
}

// applyThinking is what reaches the vendor. Each family gets its documented
// shape, and a level the model does not accept is clamped instead of sent —
// DeepSeek answers an unknown reasoning_effort with 400.
func TestApplyThinkingPerModel(t *testing.T) {
	cases := []struct {
		name   string
		model  string
		effort Effort
		want   chatRequest
	}{
		{"deepseek off disables", "deepseek-v4-pro", EffortOff,
			chatRequest{Thinking: &thinkingParam{Type: "disabled"}}},
		{"glm off disables", "glm-5.2", EffortOff,
			chatRequest{Thinking: &thinkingParam{Type: "disabled"}}},
		{"qwen off uses enable_thinking", "qwen3-max", EffortOff,
			chatRequest{EnableThinking: boolPtr(false)}},
		{"gpt-5 off uses effort none", "gpt-5.6-terra", EffortOff,
			chatRequest{ReasoningEffort: "none"}},
		{"kimi-k3 cannot be switched off", "kimi-k3", EffortOff, chatRequest{}},
		{"grok cannot be switched off", "grok-4.5", EffortOff, chatRequest{}},
		{"gemini cannot be switched off", "gemini-3.6-flash", EffortOff, chatRequest{}},
		{"gemini takes minimal", "gemini-3.6-flash", EffortMinimal, chatRequest{ReasoningEffort: "minimal"}},
		// Gemini stops at high: max must not reach the wire.
		{"gemini clamps max to high", "gemini-2.5-pro", EffortMax, chatRequest{ReasoningEffort: "high"}},
		{"grok takes high", "grok-4.5", EffortHigh, chatRequest{ReasoningEffort: "high"}},
		// grok-4.5 has no xhigh: only the multi-agent checkpoint does.
		{"grok clamps xhigh to high", "grok-4.5", EffortXHigh, chatRequest{ReasoningEffort: "high"}},
		{"grok multi-agent takes xhigh", "grok-4.20-multi-agent-0309", EffortXHigh, chatRequest{ReasoningEffort: "xhigh"}},
		{"non-thinking model is left alone", "gpt-4o", EffortOff, chatRequest{}},

		{"glm-5.2 takes max", "glm-5.2", EffortMax, chatRequest{ReasoningEffort: "max"}},
		{"deepseek takes xhigh", "deepseek-v4-pro", EffortXHigh, chatRequest{ReasoningEffort: "xhigh"}},
		// kimi-k3 has no "medium": clamp down to the nearest it accepts.
		{"kimi-k3 clamps medium to low", "kimi-k3", EffortMedium, chatRequest{ReasoningEffort: "low"}},
		{"gpt-5 takes max", "gpt-5.6-terra", EffortMax, chatRequest{ReasoningEffort: "max"}},
		// No strength control at all: send nothing rather than a rejected field.
		{"glm-4.6 sends no strength", "glm-4.6", EffortHigh, chatRequest{}},
		{"kimi-k2.6 sends no strength", "kimi-k2.6", EffortHigh, chatRequest{}},

		{"adaptive sends nothing", "glm-5.2", EffortAdaptive, chatRequest{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got chatRequest
			applyThinking(&got, tc.effort, tc.model)
			if got.ReasoningEffort != tc.want.ReasoningEffort {
				t.Errorf("reasoning_effort = %q, want %q", got.ReasoningEffort, tc.want.ReasoningEffort)
			}
			if (got.Thinking == nil) != (tc.want.Thinking == nil) ||
				(got.Thinking != nil && got.Thinking.Type != tc.want.Thinking.Type) {
				t.Errorf("thinking = %+v, want %+v", got.Thinking, tc.want.Thinking)
			}
			if (got.EnableThinking == nil) != (tc.want.EnableThinking == nil) ||
				(got.EnableThinking != nil && *got.EnableThinking != *tc.want.EnableThinking) {
				t.Errorf("enable_thinking = %v, want %v", got.EnableThinking, tc.want.EnableThinking)
			}
		})
	}
}

// End to end through the client: the level configured on the client is
// resolved against the model of each request, not once at construction.
func TestEffortResolvedPerRequestModel(t *testing.T) {
	var body map[string]any
	srv := captureServer(t, &body)
	defer srv.Close()

	p, err := New("custom", Config{APIKey: "k", BaseURL: srv.URL, Thinking: "max"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ model, want string }{
		{"glm-5.2", "max"},       // accepts max
		{"gpt-5.6-terra", "max"}, // accepts max
		{"kimi-k3", "max"},       // accepts max
	} {
		body = nil
		if _, err := p.Chat(context.Background(), Request{
			Model:    tc.model,
			Messages: []Message{{Role: RoleUser, Content: "hi"}},
		}); err != nil {
			t.Fatal(err)
		}
		if body["reasoning_effort"] != tc.want {
			t.Errorf("%s: reasoning_effort = %v, want %q", tc.model, body["reasoning_effort"], tc.want)
		}
	}
}

func boolPtr(b bool) *bool { return &b }

// Guards the ladder's own consistency: every level a model advertises must be
// one ParseEffort accepts, or the menu would offer something unparseable.
func TestAdvertisedLevelsRoundTrip(t *testing.T) {
	models := []string{"glm-5.2", "glm-4.6", "kimi-k3", "kimi-k2.6", "deepseek-v4-pro", "gpt-5.6-terra", "claude-opus-4-8", "grok-4.5", "gemini-3.6-flash", "some-unknown-model"}
	for _, m := range models {
		for _, e := range EffortsFor(m) {
			got, ok := ParseEffort(string(e))
			if !ok || got != e {
				t.Errorf("%s offers %q, which ParseEffort rejects", m, e)
			}
			if !strings.EqualFold(e.Describe(), e.Describe()) || e.Describe() == "" {
				t.Errorf("level %q has no description", e)
			}
		}
	}
}

// Whatever a model supports, the levels appear in the same relative order, so
// the menu never reshuffles when the model changes.
func TestEffortOrderIsStableAcrossModels(t *testing.T) {
	position := map[Effort]int{}
	for i, e := range Efforts() {
		position[e] = i
	}
	if Efforts()[0] != EffortAdaptive || Efforts()[len(Efforts())-1] != EffortOff {
		t.Errorf("display order must run adaptive → … → off, got %v", Efforts())
	}
	for _, model := range []string{"glm-5.2", "glm-4.6", "kimi-k3", "kimi-k2.6", "deepseek-v4-pro", "gpt-5.6-terra", "claude-opus-4-8", "grok-4.5", "grok-4.20-multi-agent-0309", "gemini-3.6-flash", "unknown-model"} {
		levels := EffortsFor(model)
		for i := 1; i < len(levels); i++ {
			if position[levels[i-1]] >= position[levels[i]] {
				t.Errorf("%s: %v is out of display order", model, levels)
				break
			}
		}
	}
}
