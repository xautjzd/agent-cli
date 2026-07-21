package permission

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Structured audit logging. The old approach prepended a free-text note to the
// tool result; that is fine for the model's context but useless for after-the-
// fact review. AuditLogger additionally appends a structured JSON line per
// decision to a file, so every gated action — approved, denied, or
// auto-approved in bypass — is recorded with full detail (tool, arguments,
// decision, reason, mode, session, cwd, time). The context note is kept too,
// but derived from the same structured record.

// AuditRecord is one structured audit-log entry.
type AuditRecord struct {
	Time      time.Time `json:"time"`
	Session   string    `json:"session,omitempty"`
	Tool      string    `json:"tool"`
	Args      string    `json:"args"`
	Decision  Action    `json:"decision"`
	Reason    string    `json:"reason"`
	Mode      Mode      `json:"mode"`
	Rule      string    `json:"rule,omitempty"`
	Dangerous bool      `json:"dangerous"`
	Cwd       string    `json:"cwd"`
	Sandboxed bool      `json:"sandboxed,omitempty"`
	Approved  bool      `json:"approved"`
}

// Note renders a compact human-readable audit note for the conversation
// context, so unattended actions stay traceable inside the transcript too.
func (r AuditRecord) Note() string {
	b, _ := json.Marshal(r)
	return "[AUDIT] " + string(b)
}

// AuditLogger appends audit records to a JSONL file. The zero value and a nil
// *AuditLogger are safe no-ops, so callers need not branch on whether auditing
// is configured.
type AuditLogger struct {
	path string
	mu   sync.Mutex
}

// NewAuditLogger returns a logger writing to path (its parent directory is
// created on first write). An empty path yields a logger that writes nothing.
func NewAuditLogger(path string) *AuditLogger {
	return &AuditLogger{path: path}
}

// Log appends one record. Failures are swallowed: auditing must never break a
// tool call, and a full disk should not stop the agent. The record is returned
// unchanged for convenience.
func (l *AuditLogger) Log(r AuditRecord) AuditRecord {
	if l == nil || l.path == "" {
		return r
	}
	if r.Time.IsZero() {
		r.Time = time.Now()
	}
	line, err := json.Marshal(r)
	if err != nil {
		return r
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return r
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return r
	}
	defer f.Close()
	f.Write(append(line, '\n'))
	return r
}

// Path returns the log file location (empty when disabled).
func (l *AuditLogger) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}
