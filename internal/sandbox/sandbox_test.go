package sandbox

import (
	"runtime"
	"strings"
	"testing"
)

func TestOffIsNoop(t *testing.T) {
	s := New(Options{Mode: "off"})
	if s.Available() {
		t.Error("off mode must not be available")
	}
	argv := s.Argv("echo hi", "/proj", false)
	if len(argv) != 3 || argv[0] != "bash" || argv[1] != "-c" || argv[2] != "echo hi" {
		t.Errorf("noop argv wrong: %v", argv)
	}
}

func TestNoopArgvPreservesCommand(t *testing.T) {
	n := noopSandbox{}
	argv := n.Argv("go test ./... && echo done", "/w", true)
	if argv[len(argv)-1] != "go test ./... && echo done" {
		t.Errorf("command mangled: %v", argv)
	}
}

func TestSeatbeltProfile(t *testing.T) {
	// Profile generation is OS-agnostic to test even off darwin.
	p := seatbeltProfile("/Users/me/proj", true)
	for _, want := range []string{
		"(deny file-write*)",
		`(subpath "/Users/me/proj")`,
		"(deny network*)",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("profile missing %q:\n%s", want, p)
		}
	}
	// Without denyNetwork, no network deny line.
	if strings.Contains(seatbeltProfile("/p", false), "(deny network*)") {
		t.Error("network should not be denied unless requested")
	}
}

func TestSeatbeltArgvWraps(t *testing.T) {
	s := &seatbelt{bin: "/usr/bin/sandbox-exec"}
	argv := s.Argv("echo hi", "/proj", false)
	if argv[0] != "/usr/bin/sandbox-exec" || argv[len(argv)-1] != "echo hi" {
		t.Errorf("seatbelt argv wrong: %v", argv)
	}
	// The profile is passed via -p.
	if argv[1] != "-p" {
		t.Errorf("expected -p flag, got %v", argv)
	}
}

func TestBubblewrapArgv(t *testing.T) {
	b := &bubblewrap{bin: "/usr/bin/bwrap"}
	argv := b.Argv("make", "/proj", true)
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--ro-bind / /") {
		t.Errorf("expected read-only host bind: %v", argv)
	}
	if !strings.Contains(joined, "--bind /proj /proj") {
		t.Errorf("expected writable project bind: %v", argv)
	}
	if !strings.Contains(joined, "--unshare-net") {
		t.Errorf("expected network isolation: %v", argv)
	}
	if argv[len(argv)-1] != "make" {
		t.Errorf("command should be last: %v", argv)
	}
}

func TestNewSelectsBackendForOS(t *testing.T) {
	s := New(Options{Mode: "auto"})
	// On darwin/linux a backend may or may not be installed; either way New
	// must return a usable (non-nil) Sandbox with a reason.
	if s == nil || s.Reason() == "" {
		t.Error("New must always return a Sandbox with a reason")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" && s.Available() {
		t.Error("unsupported OS should not report an available sandbox")
	}
}
