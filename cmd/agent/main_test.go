package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/muesli/termenv"

	"github.com/xautjzd/agent-cli/internal/config"
	"github.com/xautjzd/agent-cli/internal/provider"
	"github.com/xautjzd/agent-cli/internal/theme"
	"github.com/xautjzd/agent-cli/internal/update"
)

// TestMain forces a 16-color profile so the terminal renderer emits ANSI
// sequences the color-assertion tests can check (a plain test buffer would
// otherwise auto-detect no-color).
func TestMain(m *testing.M) {
	theme.SetColorProfile(termenv.ANSI)
	os.Exit(m.Run())
}

func TestStreamRendering(t *testing.T) {
	var buf strings.Builder
	e := &terminalEvents{out: &buf}

	e.OnThinkingDelta("first ")
	e.OnThinkingDelta("second")
	e.OnAssistantDelta("Hello")
	e.OnAssistantDelta(" world")
	e.OnStreamEnd()
	out := buf.String()

	if !strings.Contains(out, "✻ Thinking…") || !strings.Contains(out, "first second") {
		t.Errorf("thinking stream wrong:\n%q", out)
	}
	if !strings.Contains(out, "Hello world") {
		t.Errorf("text stream wrong:\n%q", out)
	}
	// Thinking styling is closed before the answer begins.
	if strings.Index(out, "second"+theme.Current().Reset) > strings.Index(out, "Hello") {
		t.Errorf("style not reset before answer:\n%q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("stream end must terminate the line:\n%q", out)
	}

	// A stream with no output at all (pure tool-call round) prints nothing.
	buf.Reset()
	e.OnStreamEnd()
	if buf.Len() != 0 {
		t.Errorf("empty stream should render nothing, got %q", buf.String())
	}
}

func TestDiffDetectionAndColorization(t *testing.T) {
	var buf strings.Builder
	e := &terminalEvents{out: &buf}
	e.lastCall = "EditFile(code.go)"

	e.OnToolResult("edit_file", strings.Join([]string{
		"Edited code.go (+1 -1)",
		"    1   package main",
		"    2 - old line",
		"    2 + new line",
	}, "\n"), true)
	out := buf.String()

	// The full diff is rendered, not collapsed to a one-line preview.
	if strings.Contains(out, "(+3 lines)") {
		t.Errorf("diff was collapsed to a preview:\n%q", out)
	}
	if !strings.Contains(out, "Edited code.go (+1 -1)") {
		t.Errorf("missing header in:\n%q", out)
	}
	// Removals render red, additions green (marker coloring survives even when
	// the code itself is syntax-highlighted).
	if !strings.Contains(out, theme.Current().Error) {
		t.Errorf("removal not colored:\n%q", out)
	}
	if !strings.Contains(out, theme.Current().Success) {
		t.Errorf("addition not colored:\n%q", out)
	}

	// Non-diff output keeps the compact one-line preview.
	buf.Reset()
	e.OnToolResult("bash", "line one\nline two\nline three", true)
	if !strings.Contains(buf.String(), "(+2 lines)") {
		t.Errorf("non-diff result should stay collapsed:\n%q", buf.String())
	}
}

func TestFormatTokens(t *testing.T) {
	cases := map[int]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567"}
	for in, want := range cases {
		if got := formatTokens(in); got != want {
			t.Errorf("formatTokens(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	if got := formatDuration(4200 * time.Millisecond); got != "4.2s" {
		t.Errorf("formatDuration = %q", got)
	}
	if got := formatDuration(63 * time.Second); got != "1m03s" {
		t.Errorf("formatDuration = %q", got)
	}
}

func TestCamelName(t *testing.T) {
	cases := map[string]string{
		"bash":       "Bash",
		"read_file":  "ReadFile",
		"write_file": "WriteFile",
		"edit_file":  "EditFile",
		"glob":       "Glob",
		"grep":       "Grep",
		"list_dir":   "ListDir",
		"use_skill":  "UseSkill",
		"remember":   "Remember",
		"forget":     "Forget",
	}
	for in, want := range cases {
		if got := camelName(in); got != want {
			t.Errorf("camelName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCompactArgs(t *testing.T) {
	// Primary argument is extracted.
	if got := compactArgs(`{"command":"go test ./...","timeout_seconds":60}`); got != "go test ./..." {
		t.Errorf("compactArgs command = %q", got)
	}
	if got := compactArgs(`{"path":"a/b.go"}`); got != "a/b.go" {
		t.Errorf("compactArgs path = %q", got)
	}
	// Fallback renders sorted key-value pairs.
	if got := compactArgs(`{"old_string":"x","new_string":"y"}`); got != "new_string: y, old_string: x" {
		t.Errorf("compactArgs fallback = %q", got)
	}
	// Invalid JSON degrades to a truncated one-liner.
	if got := compactArgs("not json\nsecond line"); got != "not json second line" {
		t.Errorf("compactArgs invalid = %q", got)
	}
}

// The usage text is the only place a user learns a subcommand exists, so it
// must cover every command run() dispatches — including the flags, which the
// custom usage function has to print itself.
func TestUsageCoversEverySubcommandAndFlag(t *testing.T) {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	fs.String("p", "", "prompt")
	fs.Bool("q", false, "quiet")
	var buf bytes.Buffer
	fs.SetOutput(&buf)
	printUsage(fs)
	usage := buf.String()

	// Every case in run()'s dispatch switch must be documented. Reading the
	// source keeps this honest: adding a command without a usage entry fails
	// here rather than silently shipping an undiscoverable feature.
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "func run(args []string) error {")
	end := strings.Index(body, "fs := flag.NewFlagSet")
	if start < 0 || end < start {
		t.Fatal("could not locate run()'s dispatch switch")
	}
	dispatched := regexp.MustCompile(`case "(\w+)":`).FindAllStringSubmatch(body[start:end], -1)
	if len(dispatched) == 0 {
		t.Fatal("no subcommands found in run()")
	}
	for _, m := range dispatched {
		if !strings.Contains(usage, "\n  "+m[1]) {
			t.Errorf("subcommand %q is dispatched but missing from the usage text", m[1])
		}
		// Each command must also answer "-h" itself, so a user who knows the
		// command but not its actions is never left guessing.
		var sub bytes.Buffer
		printSubcommandUsage(&sub, m[1])
		if !strings.HasPrefix(sub.String(), "Usage: agent "+m[1]) {
			t.Errorf("subcommand %q has no per-command help: %q", m[1], sub.String())
		}
		if err := run([]string{m[1], "-h"}); err != nil {
			t.Errorf("agent %s -h returned an error: %v", m[1], err)
		}
	}
	// Flags are listed too — the default flag usage is replaced, not extended.
	for _, want := range []string{"-p string", "-q"} {
		if !strings.Contains(usage, want) {
			t.Errorf("flag %q missing from the usage text:\n%s", want, usage)
		}
	}
}

// "agent provider use" is the shell-side equivalent of /provider: it must
// resolve the same wires and persist the same keys, so a switch made from the
// terminal is what the next session starts with.
func TestProviderUsePersistsTheSwitch(t *testing.T) {
	t.Setenv("AGENT_HOME", t.TempDir())
	t.Setenv("AGENT_BASE_URL", "")
	t.Setenv("AGENT_PROVIDER", "")

	if err := runProvider([]string{"use", "deepseek", "--anthropic"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "deepseek" || cfg.Format != "anthropic" {
		t.Fatalf("switch not persisted: provider=%q format=%q", cfg.Provider, cfg.Format)
	}
	if cfg.BaseURL != "https://api.deepseek.com/anthropic" {
		t.Errorf("wire not reflected in the endpoint: %q", cfg.BaseURL)
	}

	// Switching back clears the wire rather than leaving it pinned.
	if err := runProvider([]string{"use", "deepseek"}); err != nil {
		t.Fatal(err)
	}
	if cfg, err = config.Load(); err != nil {
		t.Fatal(err)
	}
	if cfg.Format != "" || cfg.BaseURL != "https://api.deepseek.com" {
		t.Errorf("stale wire kept: format=%q base=%q", cfg.Format, cfg.BaseURL)
	}

	if err := runProvider([]string{"use"}); err == nil {
		t.Error("a missing provider name should be an error")
	}
	if err := runProvider([]string{"frobnicate"}); err == nil {
		t.Error("an unknown subcommand should be an error")
	}
}

// A fresh install has no credential yet. An interactive run must still open —
// /provider inside the session is where the key gets set — while a one-shot
// run, which has nobody to fix it, must fail outright.
func TestUnconfiguredProviderStartsInteractiveOnly(t *testing.T) {
	t.Setenv("AGENT_HOME", t.TempDir())
	for _, v := range []string{"DEEPSEEK_API_KEY", "AGENT_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY"} {
		t.Setenv(v, "")
	}
	cfg := &config.Config{Provider: "deepseek", Model: "deepseek-v4-flash", MaxTurns: 40}

	sess, err := buildSession(cfg, t.TempDir(), true)
	if err != nil {
		t.Fatalf("interactive session must open without a credential: %v", err)
	}
	defer sess.MCP.Close()
	defer sess.LSP.Close()

	// The failure is deferred, not discarded: the placeholder still carries it.
	setupErr, placeholder := provider.SetupError(sess.Agent.Provider)
	if !placeholder {
		t.Fatal("expected an unconfigured placeholder provider")
	}
	if setupErr == nil || !strings.Contains(setupErr.Error(), "DEEPSEEK_API_KEY") {
		t.Errorf("placeholder should carry the setup error, got %v", setupErr)
	}

	if _, err := buildSession(cfg, t.TempDir(), false); err == nil {
		t.Error("a one-shot run must fail rather than start unconfigured")
	}

	// The same holds when no vendor has been chosen at all — the state a
	// fresh "agent config init" leaves behind.
	blank := &config.Config{MaxTurns: 40}
	fresh, err := buildSession(blank, t.TempDir(), true)
	if err != nil {
		t.Fatalf("a session with no provider chosen must still open: %v", err)
	}
	defer fresh.MCP.Close()
	defer fresh.LSP.Close()
	if setupErr, ok := provider.SetupError(fresh.Agent.Provider); !ok ||
		!strings.Contains(setupErr.Error(), "no provider configured") {
		t.Errorf("placeholder should explain that nothing is configured, got %v", setupErr)
	}
}

func TestShouldCheckForUpdateOnlyOnInteractiveReleaseStartup(t *testing.T) {
	tests := []struct {
		name                             string
		interactive, stdinTTY, stdoutTTY bool
		disabled, current                string
		want                             bool
	}{
		{"interactive release", true, true, true, "", "0.1.1", true},
		{"non-interactive prompt", false, true, true, "", "0.1.1", false},
		{"piped input", true, false, true, "", "0.1.1", false},
		{"redirected output", true, true, false, "", "0.1.1", false},
		{"disabled", true, true, true, "1", "0.1.1", false},
		{"development build", true, true, true, "", "dev", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldCheckForUpdate(
				tt.interactive,
				tt.stdinTTY,
				tt.stdoutTTY,
				tt.disabled,
				tt.current,
			)
			if got != tt.want {
				t.Fatalf("shouldCheckForUpdate = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHandleStartupUpdateChoices(t *testing.T) {
	release := update.Release{
		Version:  "0.2.0",
		Tag:      "v0.2.0",
		NotesURL: "https://github.com/xautjzd/agent-cli/releases/tag/v0.2.0",
	}
	checker := checkerFunc(func(context.Context, string) (update.Release, bool, error) {
		return release, true, nil
	})

	t.Run("skip continues", func(t *testing.T) {
		installed := false
		proceed := handleStartupUpdate(
			context.Background(),
			"0.1.1",
			checker,
			installerFunc(func(context.Context, update.Release) error {
				installed = true
				return nil
			}),
			promptAction(update.ActionSkip),
			strings.NewReader(""),
			io.Discard,
		)
		if !proceed || installed {
			t.Fatalf("proceed=%v installed=%v, want true/false", proceed, installed)
		}
	})

	t.Run("exit stops", func(t *testing.T) {
		if proceed := handleStartupUpdate(
			context.Background(),
			"0.1.1",
			checker,
			installerFunc(func(context.Context, update.Release) error {
				t.Fatal("exit must not install")
				return nil
			}),
			promptAction(update.ActionExit),
			strings.NewReader(""),
			io.Discard,
		); proceed {
			t.Fatal("exit should stop startup")
		}
	})

	t.Run("successful update stops and shows versions", func(t *testing.T) {
		var out strings.Builder
		if proceed := handleStartupUpdate(
			context.Background(),
			"0.1.1",
			checker,
			installerFunc(func(_ context.Context, got update.Release) error {
				if got != release {
					t.Fatalf("installed release = %+v", got)
				}
				return nil
			}),
			promptAction(update.ActionUpdate),
			strings.NewReader(""),
			&out,
		); proceed {
			t.Fatal("successful update should stop the old process")
		}
		if !strings.Contains(out.String(), "0.1.1 → 0.2.0") {
			t.Fatalf("success output does not show versions:\n%s", out.String())
		}
	})

	t.Run("failed update preserves startup and shows manual command", func(t *testing.T) {
		var out strings.Builder
		if proceed := handleStartupUpdate(
			context.Background(),
			"0.1.1",
			checker,
			installerFunc(func(context.Context, update.Release) error {
				return errors.New("read-only install")
			}),
			promptAction(update.ActionUpdate),
			strings.NewReader(""),
			&out,
		); !proceed {
			t.Fatal("failed update should allow the existing version to start")
		}
		if !strings.Contains(out.String(), "--version 0.2.0") {
			t.Fatalf("manual command missing:\n%s", out.String())
		}
	})
}

func TestHandleStartupUpdateLookupFailureIsSilentAndNonBlocking(t *testing.T) {
	prompted := false
	proceed := handleStartupUpdate(
		context.Background(),
		"0.1.1",
		checkerFunc(func(context.Context, string) (update.Release, bool, error) {
			return update.Release{}, false, errors.New("offline")
		}),
		installerFunc(func(context.Context, update.Release) error {
			t.Fatal("offline lookup must not install")
			return nil
		}),
		func(io.Reader, io.Writer, string, update.Release) (update.Action, error) {
			prompted = true
			return update.ActionSkip, nil
		},
		strings.NewReader(""),
		io.Discard,
	)
	if !proceed || prompted {
		t.Fatalf("proceed=%v prompted=%v, want true/false", proceed, prompted)
	}
}

type checkerFunc func(context.Context, string) (update.Release, bool, error)

func (f checkerFunc) Latest(ctx context.Context, current string) (update.Release, bool, error) {
	return f(ctx, current)
}

type installerFunc func(context.Context, update.Release) error

func (f installerFunc) Install(ctx context.Context, release update.Release) error {
	return f(ctx, release)
}

func promptAction(action update.Action) updatePrompt {
	return func(io.Reader, io.Writer, string, update.Release) (update.Action, error) {
		return action, nil
	}
}
