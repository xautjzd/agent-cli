package log

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// fixedClock returns a constant time so log output is deterministic in tests.
func fixedClock() time.Time {
	return time.Date(2026, 7, 26, 10, 30, 0, 123_000_000, time.UTC)
}

// setup configures the logger to write to a buffer at a given level with a
// fixed clock, returning the buffer for assertions. The previous state is
// restored by the deferred cleanup.
func setup(t *testing.T, l Level) *bytes.Buffer {
	t.Helper()
	origLevel := level
	origWriter := w
	origNow := nowFunc
	SetLevel(l)
	buf := &bytes.Buffer{}
	SetWriter(buf)
	SetNow(fixedClock)
	t.Cleanup(func() {
		SetLevel(origLevel)
		SetWriter(origWriter)
		SetNow(origNow)
	})
	return buf
}

func TestDefaultLevelIsWarn(t *testing.T) {
	// The package default is WARN unless AGENT_DEBUG is set. Save/restore the
	// env var to isolate this test from the real environment.
	t.Setenv("AGENT_DEBUG", "")
	// Re-read the default for a clean state.
	SetLevel(defaultLevel())
	defer func() { SetLevel(LevelWarn) }()

	if level != LevelWarn {
		t.Fatalf("default level = %s, want WARN", level)
	}
}

func TestEnvVarEnablesDebug(t *testing.T) {
	for _, val := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Setenv("AGENT_DEBUG", val)
		got := defaultLevel()
		if got != LevelDebug {
			t.Errorf("AGENT_DEBUG=%q → level %s, want DEBUG", val, got)
		}
	}
}

func TestEnvVarDisabledDefaultsToWarn(t *testing.T) {
	for _, val := range []string{"", "0", "false", "no", "off"} {
		t.Setenv("AGENT_DEBUG", val)
		got := defaultLevel()
		if got != LevelWarn {
			t.Errorf("AGENT_DEBUG=%q → level %s, want WARN", val, got)
		}
	}
}

func TestLevelFiltering(t *testing.T) {
	// At WARN level: Debug and Info are suppressed; Warn and Error are shown.
	buf := setup(t, LevelWarn)

	Debug("c", "debug msg")
	Info("c", "info msg")
	Warn("c", "warn msg")
	Error("c", "error msg")

	out := buf.String()
	if strings.Contains(out, "debug msg") {
		t.Error("Debug was emitted at WARN level")
	}
	if strings.Contains(out, "info msg") {
		t.Error("Info was emitted at WARN level")
	}
	if !strings.Contains(out, "warn msg") {
		t.Error("Warn was not emitted at WARN level")
	}
	if !strings.Contains(out, "error msg") {
		t.Error("Error was not emitted at WARN level")
	}
}

func TestDebugEnabledShowsAll(t *testing.T) {
	buf := setup(t, LevelDebug)

	Debug("agent", "turn %d", 2)
	Info("mcp", "connected %s", "myserver")
	Warn("main", "sandbox %s", "unavailable")
	Error("provider", "status %d", 500)

	out := buf.String()
	for _, want := range []string{"DEBUG [agent] turn 2", "INFO [mcp] connected myserver", "WARN [main] sandbox unavailable", "ERROR [provider] status 500"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestOutputFormat(t *testing.T) {
	buf := setup(t, LevelInfo)
	Info("config", "loaded from %s", "/path/to/config.json")

	out := buf.String()
	// Format: timestamp LEVEL [component] message
	expected := "2026-07-26T10:30:00.123 INFO [config] loaded from /path/to/config.json\n"
	if out != expected {
		t.Errorf("output = %q, want %q", out, expected)
	}
}

func TestEnabledReport(t *testing.T) {
	// Enabled should be true only when level is DEBUG or below.
	setup(t, LevelDebug)
	if !Enabled() {
		t.Error("Enabled() = false at DEBUG level")
	}
	setup(t, LevelWarn)
	if Enabled() {
		t.Error("Enabled() = true at WARN level")
	}
}
