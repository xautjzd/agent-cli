package permission

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditLoggerWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "audit.log") // parent created on write
	l := NewAuditLogger(path)

	l.Log(AuditRecord{Tool: "bash", Args: `{"command":"rm -rf x"}`, Decision: ActionAllow, Mode: ModeBypass, Dangerous: true, Approved: true})
	l.Log(AuditRecord{Tool: "edit_file", Decision: ActionDeny, Mode: ModeHITL, Approved: false})

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 audit lines, got %d", len(lines))
	}
	// Each line is valid JSON with the expected fields and an auto-set time.
	var rec AuditRecord
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Tool != "bash" || !rec.Dangerous || rec.Time.IsZero() {
		t.Errorf("record decoded wrong: %+v", rec)
	}
}

func TestAuditNilAndEmptySafe(t *testing.T) {
	var l *AuditLogger
	l.Log(AuditRecord{Tool: "x"})                  // nil receiver must not panic
	NewAuditLogger("").Log(AuditRecord{Tool: "x"}) // empty path writes nothing
}

func TestAuditNoteStructured(t *testing.T) {
	r := AuditRecord{Tool: "bash", Reason: "file deletion (rm)", Mode: ModeBypass, Dangerous: true}
	note := r.Note()
	if !strings.HasPrefix(note, "[AUDIT] ") {
		t.Errorf("note should be tagged: %q", note)
	}
	// The note payload is valid JSON carrying the reason.
	var back AuditRecord
	if err := json.Unmarshal([]byte(strings.TrimPrefix(note, "[AUDIT] ")), &back); err != nil {
		t.Fatalf("note payload not JSON: %v", err)
	}
	if back.Reason != "file deletion (rm)" {
		t.Errorf("note lost the reason: %+v", back)
	}
}
