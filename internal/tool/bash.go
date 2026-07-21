package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/xautjzd/agent-cli/internal/sandbox"
)

// Bash executes shell commands in the project working directory.
type Bash struct {
	// WorkDir is the directory commands run in.
	WorkDir string
	// DefaultTimeout bounds command execution; zero means 2 minutes.
	DefaultTimeout time.Duration
	// Sandbox optionally confines commands (defense in depth). Nil runs
	// commands unconfined via bash -c.
	Sandbox sandbox.Sandbox
	// DenyNetwork asks the sandbox to block outbound network where supported.
	DenyNetwork bool
}

func (b *Bash) Name() string { return "bash" }

func (b *Bash) Description() string {
	return "Execute a shell command in the project directory and return its combined stdout/stderr. " +
		"Use for builds, tests, git, and anything without a dedicated tool."
}

func (b *Bash) Schema() json.RawMessage {
	return mustSchema(`{
		"type": "object",
		"properties": {
			"command": {"type": "string", "description": "The shell command to execute"},
			"timeout_seconds": {"type": "integer", "description": "Optional timeout in seconds (default 120, max 600)"}
		},
		"required": ["command"]
	}`)
}

// Execute runs the command via `sh -c` with a hard timeout. Output is
// truncated to keep a single tool result from flooding the context window.
func (b *Bash) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Command        string `json:"command"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(args.Command) == "" {
		return "", fmt.Errorf("command must not be empty")
	}

	timeout := b.DefaultTimeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	if args.TimeoutSeconds > 0 {
		timeout = time.Duration(args.TimeoutSeconds) * time.Second
		if timeout > 10*time.Minute {
			timeout = 10 * time.Minute
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Build the command line, confining it through the sandbox when one is
	// configured and active; otherwise fall back to a plain bash -c.
	argv := []string{"bash", "-c", args.Command}
	if b.Sandbox != nil && b.Sandbox.Available() {
		argv = b.Sandbox.Argv(args.Command, b.WorkDir, b.DenyNetwork)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = b.WorkDir
	out, err := cmd.CombinedOutput()

	result := truncateOutput(string(out), 30000)
	if ctx.Err() == context.DeadlineExceeded {
		return result + "\n(command timed out)", nil
	}
	if err != nil {
		// Non-zero exit is normal information for the model, not a failure.
		return fmt.Sprintf("%s\n(exit error: %v)", result, err), nil
	}
	return result, nil
}

// truncateOutput keeps the head and tail of very long output, which is where
// build errors and summaries usually live.
func truncateOutput(s string, max int) string {
	if len(s) <= max {
		return s
	}
	half := max / 2
	return s[:half] + fmt.Sprintf("\n... (%d bytes truncated) ...\n", len(s)-max) + s[len(s)-half:]
}
