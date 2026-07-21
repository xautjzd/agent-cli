// Package permission classifies tool calls by risk and defines the
// permission modes that decide what happens when a dangerous call occurs.
//
// Two modes exist:
//   - ModeHITL (default): dangerous operations pause for human approval.
//   - ModeBypass: nothing pauses, but every dangerous operation is
//     annotated with an audit note that is fed into the conversation
//     context (and therefore persisted in the session) for traceability.
package permission

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Mode selects how dangerous operations are handled.
type Mode string

const (
	// ModeHITL requires human-in-the-loop confirmation for dangerous
	// operations. This is the default.
	ModeHITL Mode = "hitl"
	// ModeBypass auto-approves everything, recording audit notes instead.
	ModeBypass Mode = "bypass"
)

// dangerousPatterns matches shell commands with destructive or
// irreversible potential. Each entry carries a human-readable reason used
// in prompts and audit notes.
var dangerousPatterns = []struct {
	re     *regexp.Regexp
	reason string
}{
	{regexp.MustCompile(`\brm\s`), "file deletion (rm)"},
	{regexp.MustCompile(`\brmdir\b`), "directory deletion (rmdir)"},
	{regexp.MustCompile(`\bsudo\b`), "privilege escalation (sudo)"},
	{regexp.MustCompile(`\bch(mod|own)\b`), "permission/ownership change"},
	{regexp.MustCompile(`\b(kill|pkill|killall)\b`), "process termination"},
	{regexp.MustCompile(`\bdd\s`), "raw disk/file writing (dd)"},
	{regexp.MustCompile(`\bmkfs`), "filesystem formatting"},
	{regexp.MustCompile(`\b(shutdown|reboot|halt)\b`), "system power control"},
	{regexp.MustCompile(`\bgit\s+push\b`), "publishing to a remote (git push)"},
	{regexp.MustCompile(`\bgit\s+reset\s+--hard\b`), "discarding work (git reset --hard)"},
	{regexp.MustCompile(`\bgit\s+clean\b`), "deleting untracked files (git clean)"},
	{regexp.MustCompile(`(curl|wget)[^|;]*\|\s*(ba|z)?sh\b`), "piping a download into a shell"},
	{regexp.MustCompile(`\btruncate\b`), "file truncation"},
	{regexp.MustCompile(`\bmv\s+[^ ]+\s+/(?:\s|$)`), "moving files to filesystem root"},
}

// Classify reports whether a tool call is dangerous and why.
//
// Key flow: bash commands are matched against the destructive-pattern list;
// file-writing tools are dangerous only when they target a path outside the
// project working directory (in-project edits are the agent's normal job).
// Everything else is safe.
func Classify(toolName string, args json.RawMessage, workDir string) (bool, string) {
	switch toolName {
	case "bash":
		var a struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return false, ""
		}
		for _, p := range dangerousPatterns {
			if p.re.MatchString(a.Command) {
				return true, p.reason
			}
		}
	case "write_file", "edit_file":
		var a struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(args, &a); err != nil || a.Path == "" {
			return false, ""
		}
		path := a.Path
		if !filepath.IsAbs(path) {
			return false, "" // relative paths resolve inside the project
		}
		clean := filepath.Clean(path)
		root := filepath.Clean(workDir)
		if clean != root && !strings.HasPrefix(clean, root+string(filepath.Separator)) {
			return true, fmt.Sprintf("writing outside the project directory (%s)", clean)
		}
	}
	return false, ""
}
