package repl

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xautjzd/agent-cli/internal/diff"
	"github.com/xautjzd/agent-cli/internal/editmatch"
	"github.com/xautjzd/agent-cli/internal/permission"
)

// Security helpers for the permission gate: policy access, the file-edit diff
// preview shown before confirmation, and the small utilities the "remember my
// choice" flow needs.

// policyOrDefault returns the configured policy, lazily creating a default
// (standard posture, no rules) so the gate always has one.
func (r *Repl) policyOrDefault() *permission.Policy {
	if r.Policy == nil {
		r.Policy, _ = permission.NewPolicy(permission.PostureStandard, nil)
	}
	return r.Policy
}

// sessionID returns the current session's id for audit records ("" when none
// has been created yet).
func (r *Repl) sessionID() string {
	if r.current != nil {
		return r.current.ID
	}
	return ""
}

// sandboxActive reports whether bash commands are being confined.
func (r *Repl) sandboxActive() bool { return r.SandboxActive }

// editPreview renders the exact change a write_file/edit_file call would make,
// as a unified diff, so the user approves the concrete diff — not just a path.
// It returns "" for non-file tools or when the change cannot be previewed
// (the caller then falls back to showing raw arguments).
func (r *Repl) editPreview(name, args string) string {
	switch name {
	case "write_file":
		var a struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if json.Unmarshal([]byte(args), &a) != nil || a.Path == "" {
			return ""
		}
		before, _ := os.ReadFile(r.resolve(a.Path))
		return "  change:\n" + indentDiff(diff.Compute(a.Path, string(before), a.Content, diff.DefaultOptions()).Unified())
	case "edit_file":
		var a struct {
			Path       string `json:"path"`
			OldString  string `json:"old_string"`
			NewString  string `json:"new_string"`
			ReplaceAll bool   `json:"replace_all"`
		}
		if json.Unmarshal([]byte(args), &a) != nil || a.Path == "" {
			return ""
		}
		data, err := os.ReadFile(r.resolve(a.Path))
		if err != nil {
			return ""
		}
		res, err := editmatch.Replace(string(data), a.OldString, a.NewString, a.ReplaceAll)
		if err != nil {
			return "" // the edit won't apply cleanly; show raw args instead
		}
		return "  change:\n" + indentDiff(diff.Compute(a.Path, string(data), res.Updated, diff.DefaultOptions()).Unified())
	}
	return ""
}

// resolve makes a tool path absolute relative to the working directory, the
// same way the file tools do, so previews read the right file.
func (r *Repl) resolve(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(r.WorkDir, p)
}

// indentDiff indents each line of a diff by two spaces so it nests under the
// "change:" label in the prompt.
func indentDiff(d string) string {
	if d == "" {
		return "  (no textual change)\n"
	}
	lines := strings.Split(strings.TrimRight(d, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n") + "\n"
}

// pathGlobForDir builds a glob that matches any file in the same directory as
// path (relative to workDir), used when the user chooses to always allow edits
// under a directory.
func pathGlobForDir(path, workDir string) string {
	rel := path
	if filepath.IsAbs(path) && workDir != "" {
		if r, err := filepath.Rel(workDir, filepath.Clean(path)); err == nil {
			rel = r
		}
	}
	dir := filepath.ToSlash(filepath.Dir(rel))
	if dir == "." || dir == "" {
		return "*"
	}
	return dir + "/**"
}

// regexpEscape quotes a string for safe inclusion in a regular expression.
func regexpEscape(s string) string { return regexp.QuoteMeta(s) }

// cmdSecurity prints the effective security configuration so the user can see,
// at a glance, what is enforced this session.
func (r *Repl) cmdSecurity(_ context.Context, _ string) error {
	fmt.Fprintln(r.Out, "Security settings:")
	fmt.Fprintf(r.Out, "  permission mode : %s\n", r.permMode())
	posture := "standard"
	if r.Cfg != nil && r.Cfg.BashPolicy == "strict" {
		posture = "strict"
	}
	fmt.Fprintf(r.Out, "  bash posture    : %s\n", posture)

	sandbox := "off"
	if r.SandboxActive {
		sandbox = "on (active)"
	} else if r.Cfg != nil && r.Cfg.Sandbox != "" && r.Cfg.Sandbox != "off" {
		sandbox = r.Cfg.Sandbox + " (requested, inactive — no backend)"
	}
	fmt.Fprintf(r.Out, "  command sandbox : %s\n", sandbox)

	if path := r.Audit.Path(); path != "" {
		fmt.Fprintf(r.Out, "  audit log       : %s\n", path)
	} else {
		fmt.Fprintln(r.Out, "  audit log       : (disabled)")
	}

	rules := r.policyOrDefault().Rules()
	if len(rules) == 0 {
		fmt.Fprintln(r.Out, "  approval rules  : none (built-in risk classifier only)")
	} else {
		fmt.Fprintf(r.Out, "  approval rules  : %d\n", len(rules))
		for _, d := range rules {
			fmt.Fprintf(r.Out, "      • %s\n", d)
		}
	}
	return nil
}
