package sandbox

import (
	"os"
	"path/filepath"
)

// bubblewrap confines commands with bwrap on Linux: the host filesystem is
// bind-mounted read-only, the working directory is bound read-write, and a
// fresh /tmp is provided. With --unshare-net, outbound network is cut. Like
// the macOS backend this is a pragmatic confinement, not a full container.
type bubblewrap struct{ bin string }

func (b *bubblewrap) Name() string    { return "bwrap" }
func (b *bubblewrap) Available() bool { return true }
func (b *bubblewrap) Reason() string {
	return "confined via bubblewrap (host read-only, writes limited to the project)"
}

func (b *bubblewrap) Argv(command, workDir string, denyNetwork bool) []string {
	argv := []string{
		b.bin,
		"--ro-bind", "/", "/", // whole host visible, read-only
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--bind", workDir, workDir, // project is writable
		"--chdir", workDir,
	}
	if home := homeCaches(); home != "" {
		if _, err := os.Stat(home); err == nil {
			argv = append(argv, "--bind", home, home) // toolchain caches writable
		}
	}
	if denyNetwork {
		argv = append(argv, "--unshare-net")
	}
	return append(argv, "bash", "-c", command)
}

// homeCaches returns a per-user cache directory that build tools write to, so
// the sandbox can permit it. Empty when it cannot be determined.
func homeCaches() string {
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Clean(dir)
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Clean(home)
	}
	return ""
}
