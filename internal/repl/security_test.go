package repl

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xautjzd/agent-cli/internal/permission"
)

// gateArgs builds edit_file arguments.
func editArgs(t *testing.T, path, oldS, newS string) string {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"path": path, "old_string": oldS, "new_string": newS})
	return string(b)
}

func TestGateDenyRuleBlocks(t *testing.T) {
	r, _, out := newTestRepl(t, "")
	r.Policy, _ = permission.NewPolicy(permission.PostureStandard, []permission.Rule{
		{Tool: "bash", Command: `\bgit\s+push\b`, Action: permission.ActionDeny},
	})
	allow, _ := r.BeforeToolCall("bash", `{"command":"git push origin main"}`)
	if allow {
		t.Error("deny rule must block the call")
	}
	if !strings.Contains(out.String(), "Denied by policy") {
		t.Errorf("expected a denial message: %s", out.String())
	}
}

func TestGateShowsDiffPreviewOnApproval(t *testing.T) {
	r, _, out := newTestRepl(t, "y\n") // approve
	// A file edit that lands OUTSIDE the project is dangerous → prompts.
	dir := t.TempDir() // outside r.WorkDir
	path := filepath.Join(dir, "f.txt")
	os.WriteFile(path, []byte("hello world\n"), 0o644)

	allow, _ := r.BeforeToolCall("edit_file", editArgs(t, path, "hello", "goodbye"))
	if !allow {
		t.Fatal("approval 'y' should allow")
	}
	got := out.String()
	// The prompt shows a unified diff of the prospective change, not just args.
	if !strings.Contains(got, "change:") || !strings.Contains(got, "- hello world") || !strings.Contains(got, "+ goodbye world") {
		t.Errorf("expected a diff preview before confirmation:\n%s", got)
	}
}

func TestGateAlwaysAllowRemembersForSession(t *testing.T) {
	// First call: user answers "a" (always). Second identical-program call
	// must not prompt again.
	r, _, _ := newTestRepl(t, "a\n")
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	os.WriteFile(path, []byte("x\n"), 0o644)

	// Use bash rm (dangerous) so it prompts; "always" should remember rm.
	if allow, _ := r.BeforeToolCall("bash", `{"command":"rm `+path+`"}`); !allow {
		t.Fatal("'always' should allow this call")
	}
	// A second rm — the reader is now empty (EOF). Without a remembered rule
	// this would be denied (EOF → not approved); with it, it is auto-allowed.
	if allow, _ := r.BeforeToolCall("bash", `{"command":"rm otherfile"}`); !allow {
		t.Error("second rm should be auto-allowed by the remembered session rule")
	}
}

func TestGateBypassAuditsWithNote(t *testing.T) {
	r, _, out := newTestRepl(t, "")
	r.Mode = permission.ModeBypass
	// Audit to a temp file so we can inspect the structured record.
	auditPath := filepath.Join(t.TempDir(), "audit.log")
	r.Audit = permission.NewAuditLogger(auditPath)

	allow, note := r.BeforeToolCall("bash", `{"command":"rm -rf build"}`)
	if !allow {
		t.Fatal("bypass must auto-approve")
	}
	if !strings.Contains(note, "[AUDIT]") {
		t.Errorf("bypass should return a structured audit note, got %q", note)
	}
	if !strings.Contains(out.String(), "auto-approved") {
		t.Errorf("bypass should print an auto-approval line: %s", out.String())
	}
	// The structured record was persisted.
	data, err := os.ReadFile(auditPath)
	if err != nil || !strings.Contains(string(data), `"tool":"bash"`) {
		t.Errorf("audit record not written: %v / %s", err, data)
	}
}

func TestGateSafeCallPassesSilently(t *testing.T) {
	r, _, out := newTestRepl(t, "")
	allow, note := r.BeforeToolCall("bash", `{"command":"go test ./..."}`)
	if !allow || note != "" {
		t.Errorf("safe call should pass silently: allow=%v note=%q", allow, note)
	}
	if out.String() != "" {
		t.Errorf("safe call should print nothing, got: %s", out.String())
	}
}
