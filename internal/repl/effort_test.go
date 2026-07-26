package repl

import (
	"context"
	"strings"
	"testing"
)

// /effort must offer what the active model accepts, not a fixed ladder: the
// levels differ per model even within one vendor.
func TestEffortMenuFollowsTheModel(t *testing.T) {
	cases := []struct {
		model   string
		want    []string
		notWant []string
	}{
		{"glm-5.2", []string{"off", "minimal", "xhigh", "max", "adaptive"}, nil},
		{"glm-4.6", []string{"off", "adaptive"}, []string{"high", "max"}},
		{"kimi-k3", []string{"low", "high", "max"}, []string{"off", "medium"}},
		{"kimi-k2.6", []string{"off", "adaptive"}, []string{"low", "max"}},
	}
	for _, tc := range cases {
		r, _, out := newTestRepl(t, "")
		r.Cfg.Model = tc.model
		if err := r.cmdEffort(context.Background(), ""); err != nil {
			t.Fatal(err)
		}
		got := out.String()
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("%s: menu missing %q:\n%s", tc.model, want, got)
			}
		}
		for _, absent := range tc.notWant {
			// Match the padded level column so "high" does not match "xhigh".
			if strings.Contains(got, " "+absent+" ") {
				t.Errorf("%s: menu offers unsupported %q:\n%s", tc.model, absent, got)
			}
		}
	}
}

// A model with no thinking at all says so instead of printing a menu that
// cannot do anything.
func TestEffortOnNonThinkingModel(t *testing.T) {
	r, _, out := newTestRepl(t, "")
	r.Cfg.Model = "moonshot-v1-128k"
	if err := r.cmdEffort(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no thinking controls") {
		t.Errorf("expected a no-controls notice, got:\n%s", out.String())
	}
	if err := r.cmdEffort(context.Background(), "high"); err == nil {
		t.Error("setting an effort on a non-thinking model should be an error")
	}
}

// Asking for a level the model rejects must fail with what it does support,
// rather than silently sending something the endpoint would 400 on (DeepSeek)
// or quietly ignore (Kimi).
func TestEffortRejectsUnsupportedLevel(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	r.Cfg.Model = "kimi-k3"
	err := r.cmdEffort(context.Background(), "off")
	if err == nil {
		t.Fatal("kimi-k3 always thinks; off must be rejected")
	}
	if !strings.Contains(err.Error(), "always thinks") || !strings.Contains(err.Error(), "low, high, max") {
		t.Errorf("error should name the supported levels, got: %v", err)
	}

	r.Cfg.Model = "kimi-k2.6"
	if err := r.cmdEffort(context.Background(), "high"); err == nil || !strings.Contains(err.Error(), "does not accept") {
		t.Errorf("kimi-k2.6 has no strength control; want a rejection, got: %v", err)
	}

	// A supported level still applies.
	r.Cfg.Model = "glm-5.2"
	if err := r.cmdEffort(context.Background(), "xhigh"); err != nil {
		t.Fatal(err)
	}
	if r.Cfg.Thinking != "xhigh" {
		t.Errorf("effort not applied: %q", r.Cfg.Thinking)
	}
}
