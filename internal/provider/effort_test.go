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
		"minimal":  EffortLow,
		"medium":   EffortMedium,
		"high":     EffortHigh,
		"max":      EffortHigh,
		"HIGH":     EffortHigh, // case-insensitive
		"  low  ":  EffortLow,  // trimmed
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
	// Budget-based levels ascend; off and adaptive carry no fixed budget.
	if EffortLow.budgetTokens() >= EffortMedium.budgetTokens() ||
		EffortMedium.budgetTokens() >= EffortHigh.budgetTokens() {
		t.Error("effort budgets must strictly increase with level")
	}
	if EffortOff.budgetTokens() != 0 || EffortAdaptive.budgetTokens() != 0 {
		t.Error("off and adaptive must have no fixed budget")
	}
	// reasoning_effort is only emitted for the graduated levels.
	if EffortOff.reasoningEffort() != "" || EffortAdaptive.reasoningEffort() != "" {
		t.Error("off and adaptive must not emit a reasoning_effort value")
	}
	if EffortLow.reasoningEffort() != "low" || EffortHigh.reasoningEffort() != "high" {
		t.Error("graduated levels must map to their reasoning_effort names")
	}
}
