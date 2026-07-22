package repl

import (
	"strings"
	"testing"

	"github.com/xautjzd/agent-cli/internal/permission"
)

// TestNonInteractiveGateDeniesInsteadOfHanging verifies that in non-interactive
// mode a dangerous operation is denied (never prompts / never blocks on stdin).
func TestNonInteractiveGateDeniesInsteadOfHanging(t *testing.T) {
	// Empty stdin: a prompting gate would read EOF; NonInteractive must not
	// even try — it denies outright.
	r, _, out := newTestRepl(t, "")
	r.NonInteractive = true
	r.Mode = permission.ModeHITL

	allow, _ := r.BeforeToolCall("bash", `{"command":"rm -rf /tmp/x"}`)
	if allow {
		t.Fatal("dangerous op must be denied in non-interactive mode")
	}
	if !strings.Contains(out.String(), "denied (non-interactive)") {
		t.Errorf("expected a non-interactive denial message: %s", out.String())
	}

	// Safe operations still pass without prompting.
	if allow, _ := r.BeforeToolCall("bash", `{"command":"go test ./..."}`); !allow {
		t.Error("safe op should pass in non-interactive mode")
	}
}

// TestNonInteractiveBypassStillAllows confirms -bypass overrides the deny.
func TestNonInteractiveBypassStillAllows(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	r.NonInteractive = true
	r.Mode = permission.ModeBypass // -bypass sets this

	allow, note := r.BeforeToolCall("bash", `{"command":"rm -rf build"}`)
	if !allow {
		t.Error("bypass should allow even in non-interactive mode")
	}
	if !strings.Contains(note, "[AUDIT]") {
		t.Errorf("bypass should still audit: %q", note)
	}
}
