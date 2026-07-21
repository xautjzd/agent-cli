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

// Posture selects how bash commands are classified.
type Posture string

const (
	// PostureStandard flags known-dangerous commands (robust deny-list with
	// obfuscation-resistant parsing) and treats the rest as safe. Default.
	PostureStandard Posture = "standard"
	// PostureStrict additionally treats any command not on the known-safe
	// allow-list as requiring approval (default-deny for the unknown).
	PostureStrict Posture = "strict"
)

// Classify reports whether a tool call is dangerous and why, using the
// standard posture. It is the backward-compatible entry point; ClassifyWith
// takes an explicit posture.
func Classify(toolName string, args json.RawMessage, workDir string) (bool, string) {
	return ClassifyWith(PostureStandard, toolName, args, workDir)
}

// ClassifyWith reports whether a tool call is dangerous and why.
//
// Key flow: bash commands are tokenized and each resolved command classified
// (see analyze.go), which resists deny-list evasion; under the strict posture
// an unrecognized command is also flagged. File-writing tools are dangerous
// only when they target a path outside the project working directory
// (in-project edits are the agent's normal job). Everything else is safe.
func ClassifyWith(posture Posture, toolName string, args json.RawMessage, workDir string) (bool, string) {
	switch toolName {
	case "bash":
		var a struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return true, "unparseable bash arguments" // fail closed
		}
		if dangerous, reason := analyzeCommand(a.Command, workDir); dangerous {
			return true, reason
		}
		if posture == PostureStrict && !isKnownSafe(a.Command, workDir) {
			return true, "unrecognized command (strict mode requires approval)"
		}
	case "write_file", "edit_file":
		var a struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(args, &a); err != nil || a.Path == "" {
			return false, ""
		}
		if filepath.IsAbs(a.Path) && !withinDir(a.Path, workDir) {
			return true, fmt.Sprintf("writing outside the project directory (%s)", filepath.Clean(a.Path))
		}
	}
	return false, ""
}
