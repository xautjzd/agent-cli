// Package log provides a lightweight, dependency-free leveled logger for the
// agent CLI. It writes to stderr so stdout stays clean for piping.
//
// Levels — DEBUG, INFO, WARN, ERROR — are controlled by the AGENT_DEBUG
// environment variable: when set to "1" or "true", DEBUG and above are emitted;
// otherwise only WARN and ERROR are shown (so existing warning behavior is
// preserved without the env var).
//
// All functions are safe for concurrent use. The output format is:
//
//	2026-07-26T10:30:00.123 DEBUG [component] message
//
// The package is initialized once from the environment at init time. A test
// can override the level and writer via SetLevel/SetWriter.
package log

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Level is the logging severity.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// String renders the level tag for log lines.
func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "?????"
	}
}

var (
	mu      sync.Mutex
	level   = defaultLevel()
	w       io.Writer = os.Stderr
	nowFunc           = time.Now
)

// defaultLevel reads AGENT_DEBUG at init; the package-level functions use it.
func defaultLevel() Level {
	v := os.Getenv("AGENT_DEBUG")
	switch v {
	case "1", "true", "TRUE", "True", "yes", "on":
		return LevelDebug
	}
	return LevelWarn
}

func init() {
	level = defaultLevel()
}

// SetLevel overrides the minimum level (for testing).
func SetLevel(l Level) {
	mu.Lock()
	defer mu.Unlock()
	level = l
}

// SetWriter overrides the output destination (for testing).
func SetWriter(wr io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	w = wr
}

// SetNow overrides the time source (for testing).
func SetNow(f func() time.Time) {
	mu.Lock()
	defer mu.Unlock()
	nowFunc = f
}

// Enabled reports whether DEBUG-level logging is active (AGENT_DEBUG is set).
func Enabled() bool {
	mu.Lock()
	defer mu.Unlock()
	return level <= LevelDebug
}

// emit writes one line if the level passes the threshold.
func emit(l Level, component, format string, args ...any) {
	mu.Lock()
	if l < level {
		mu.Unlock()
		return
	}
	ts := nowFunc().Format("2006-01-02T15:04:05.000")
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(w, "%s %s [%s] %s\n", ts, l, component, msg)
	mu.Unlock()
}

// Debug logs at DEBUG level. Use for detailed tracing at key nodes.
func Debug(component, format string, args ...any) {
	emit(LevelDebug, component, format, args...)
}

// Info logs at INFO level. Use for milestones visible during a debug session.
func Info(component, format string, args ...any) {
	emit(LevelInfo, component, format, args...)
}

// Warn logs at WARN level. Visible by default (without the env var).
func Warn(component, format string, args ...any) {
	emit(LevelWarn, component, format, args...)
}

// Error logs at ERROR level. Visible by default.
func Error(component, format string, args ...any) {
	emit(LevelError, component, format, args...)
}
