// Package sandbox optionally confines bash commands to reduce the blast radius
// of a mistaken or malicious command. It is a defense-in-depth layer beneath
// the permission gate: the gate decides whether a command may run at all; the
// sandbox limits what a running command can touch.
//
// Sandboxing is inherently OS-specific and best-effort. This package exposes a
// single Sandbox interface (DIP) with pluggable backends selected at startup:
//
//   - macOS: sandbox-exec (Seatbelt) with a generated profile that permits
//     reads broadly but confines writes to the working directory and,
//     optionally, denies outbound network.
//   - Linux: bubblewrap (bwrap) with a read-only root bind and a writable
//     working directory.
//   - noop: used when no backend is available or sandboxing is disabled, so
//     callers need not special-case the unsandboxed path.
//
// A backend that is requested but unavailable degrades to noop with a reason,
// surfaced to the user rather than silently pretending to sandbox.
package sandbox

import (
	"os/exec"
	"runtime"
)

// shellArgv is the unconfined command line: run the command through bash -c.
func shellArgv(command string) []string {
	return []string{"bash", "-c", command}
}

// Sandbox wraps a shell command so it runs confined. Available reports whether
// real confinement is in effect; Argv returns the full command line (program +
// arguments) to execute, so the caller can attach it to its own context and
// timeout via exec.CommandContext.
type Sandbox interface {
	// Name identifies the backend ("sandbox-exec", "bwrap", "none").
	Name() string
	// Available reports whether real confinement is active.
	Available() bool
	// Reason explains why confinement is or is not active (for display).
	Reason() string
	// Argv builds the argv that runs the given shell command line confined to
	// workDir. denyNetwork requests blocking outbound network where the
	// backend supports it. argv[0] is the program to exec.
	Argv(command, workDir string, denyNetwork bool) []string
}

// Options configure sandbox selection.
type Options struct {
	// Mode is "off", "on", or "auto". "auto" enables sandboxing when a
	// backend is available and stays silent (noop) otherwise; "on" enables it
	// and reports when unavailable; "off" disables it.
	Mode string
	// DenyNetwork requests network isolation where supported.
	DenyNetwork bool
}

// New selects a sandbox backend for the current OS per opts.
func New(opts Options) Sandbox {
	if opts.Mode == "off" || opts.Mode == "" {
		return noopSandbox{reason: "sandboxing disabled"}
	}
	switch runtime.GOOS {
	case "darwin":
		if path, err := exec.LookPath("sandbox-exec"); err == nil {
			return &seatbelt{bin: path}
		}
		return noopSandbox{reason: "sandbox-exec not found on this macOS"}
	case "linux":
		if path, err := exec.LookPath("bwrap"); err == nil {
			return &bubblewrap{bin: path}
		}
		return noopSandbox{reason: "bubblewrap (bwrap) not installed"}
	default:
		return noopSandbox{reason: "no sandbox backend for " + runtime.GOOS}
	}
}

// noopSandbox runs commands without confinement.
type noopSandbox struct{ reason string }

func (n noopSandbox) Name() string    { return "none" }
func (n noopSandbox) Available() bool { return false }
func (n noopSandbox) Reason() string  { return n.reason }
func (n noopSandbox) Argv(command, _ string, _ bool) []string {
	return shellArgv(command)
}
