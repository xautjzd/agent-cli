package provider

import "testing"

func TestParseEffort(t *testing.T) {
	cases := map[string]Effort{
		"":         EffortAdaptive,
		"adaptive": EffortAdaptive,
		"on":       EffortAdaptive,
		"auto":     EffortAdaptive,
		"off":      EffortOff,
		"none":     EffortOff,
		"low":      EffortLow,
		// The ladder spans every vendor spelling, so these are levels of
		// their own rather than aliases of low/high.
		"minimal": EffortMinimal,
		"medium":  EffortMedium,
		"high":    EffortHigh,
		"xhigh":   EffortXHigh,
		"max":     EffortMax,
		"HIGH":    EffortHigh, // case-insensitive
		"  low  ": EffortLow,  // trimmed
	}
	for in, want := range cases {
		got, ok := ParseEffort(in)
		if !ok || got != want {
			t.Errorf("ParseEffort(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if _, ok := ParseEffort("bogus"); ok {
		t.Error("ParseEffort should reject an unknown level")
	}
}

func TestEffortMappings(t *testing.T) {
	// Budget-based levels ascend across the whole ladder; off and adaptive
	// carry no fixed budget.
	ladder := []Effort{EffortMinimal, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax}
	for i := 1; i < len(ladder); i++ {
		if ladder[i-1].budgetTokens() >= ladder[i].budgetTokens() {
			t.Errorf("effort budgets must strictly increase: %s >= %s", ladder[i-1], ladder[i])
		}
		if ladder[i-1].rank() >= ladder[i].rank() {
			t.Errorf("effort ranks must strictly increase: %s >= %s", ladder[i-1], ladder[i])
		}
	}
	if EffortOff.budgetTokens() != 0 || EffortAdaptive.budgetTokens() != 0 {
		t.Error("off and adaptive must have no fixed budget")
	}
	// reasoning_effort is only emitted for the graduated levels.
	if EffortOff.reasoningEffort() != "" || EffortAdaptive.reasoningEffort() != "" {
		t.Error("off and adaptive must not emit a reasoning_effort value")
	}
	for _, e := range ladder {
		if e.reasoningEffort() != string(e) {
			t.Errorf("%s must map to its own reasoning_effort name, got %q", e, e.reasoningEffort())
		}
	}
}
