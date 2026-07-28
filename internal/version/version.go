// Package version is the single source of truth for the CLI's release version,
// so the `version` subcommand, the startup banner, and the MCP handshake never
// drift apart.
package version

import (
	"runtime/debug"
	"strconv"
	"strings"
)

// Version is overridden at build time by GoReleaser via ldflags
// (-X ...Version=<tag>). Tagged `go install` builds recover their version from
// Go's embedded module metadata; local source builds stay "dev".
var Version = "dev"

func init() {
	if Version != "dev" {
		return
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if release, ok := stableModuleVersion(info.Main.Version); ok {
			Version = release
		}
	}
}

func stableModuleVersion(raw string) (string, bool) {
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "v")
	if raw == "" || strings.ContainsAny(raw, "-+") {
		return "", false
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return "", false
	}
	for _, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return "", false
		}
		if _, err := strconv.Atoi(part); err != nil {
			return "", false
		}
	}
	return strings.Join(parts, "."), true
}
