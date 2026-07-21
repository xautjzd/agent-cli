package sandbox

import (
	"fmt"
	"strings"
)

// seatbelt confines commands with macOS's sandbox-exec (Seatbelt). The
// generated profile allows reads and process execution broadly (builds and
// tools need to read the toolchain and system libraries) but restricts file
// writes to the working directory plus the standard temp/cache locations, and
// optionally denies outbound network. This is a pragmatic profile, not a
// maximum-security jail: its purpose is to stop an errant command from
// clobbering files outside the project.
type seatbelt struct{ bin string }

func (s *seatbelt) Name() string    { return "sandbox-exec" }
func (s *seatbelt) Available() bool { return true }
func (s *seatbelt) Reason() string {
	return "confined via sandbox-exec (writes limited to the project)"
}

func (s *seatbelt) Argv(command, workDir string, denyNetwork bool) []string {
	profile := seatbeltProfile(workDir, denyNetwork)
	// -p passes the profile inline; the command runs under bash so shell
	// syntax (pipes, redirects) still works, but confined by the profile.
	return []string{s.bin, "-p", profile, "bash", "-c", command}
}

// seatbeltProfile builds a Seatbelt profile string. Writes are denied by
// default and re-allowed under workDir and transient locations; network is
// denied when requested.
func seatbeltProfile(workDir string, denyNetwork bool) string {
	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(allow default)\n") // permit reads/exec/etc. by default…
	b.WriteString("(deny file-write*)\n")
	// …then re-allow writes only where builds legitimately need them.
	for _, dir := range writableRoots(workDir) {
		fmt.Fprintf(&b, "(allow file-write* (subpath %q))\n", dir)
	}
	// Writing to the tty and /dev/null must stay allowed for normal output.
	b.WriteString("(allow file-write-data (regex #\"^/dev/\"))\n")
	if denyNetwork {
		b.WriteString("(deny network*)\n")
	}
	return b.String()
}

// writableRoots is the set of directories the sandbox permits writes to.
func writableRoots(workDir string) []string {
	roots := []string{workDir, "/tmp", "/private/tmp", "/private/var/folders", "/var/folders"}
	if home := homeCaches(); home != "" {
		roots = append(roots, home)
	}
	return roots
}
