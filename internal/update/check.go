// Package update checks and installs agent-cli releases.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	Repository       = "xautjzd/agent-cli"
	latestReleaseURL = "https://api.github.com/repos/" + Repository + "/releases/latest"
	maxMetadataBytes = 1 << 20
)

// Release is the trusted subset of GitHub release metadata used by the
// updater. URLs are constructed from the fixed repository rather than accepted
// from the API response.
type Release struct {
	Version  string
	Tag      string
	NotesURL string
}

// Checker queries the latest stable GitHub release.
type Checker struct {
	client   *http.Client
	endpoint string
}

// NewChecker returns a checker with a short timeout suitable for CLI startup.
func NewChecker() Checker {
	return Checker{
		client:   &http.Client{Timeout: 2 * time.Second},
		endpoint: latestReleaseURL,
	}
}

// Latest reports whether a stable release newer than current exists.
// Unparseable current versions (for example "dev") are treated as development
// builds and skip the lookup entirely.
func (c Checker) Latest(ctx context.Context, current string) (Release, bool, error) {
	currentVersion, ok := parseVersion(current)
	if !ok {
		return Release{}, false, nil
	}
	if c.client == nil {
		c.client = &http.Client{Timeout: 2 * time.Second}
	}
	if c.endpoint == "" {
		c.endpoint = latestReleaseURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return Release{}, false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "agent-cli/"+current)

	resp, err := c.client.Do(req)
	if err != nil {
		return Release{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, false, fmt.Errorf("GitHub releases returned %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadataBytes+1))
	if err != nil {
		return Release{}, false, err
	}
	if len(body) > maxMetadataBytes {
		return Release{}, false, fmt.Errorf("GitHub release metadata exceeds %d bytes", maxMetadataBytes)
	}

	var metadata struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.Unmarshal(body, &metadata); err != nil {
		return Release{}, false, fmt.Errorf("decode GitHub release metadata: %w", err)
	}
	latest, ok := parseVersion(metadata.TagName)
	if !ok || metadata.Draft || metadata.Prerelease {
		return Release{}, false, nil
	}
	if compareVersion(latest, currentVersion) <= 0 {
		return Release{}, false, nil
	}

	version := latest.String()
	return Release{
		Version:  version,
		Tag:      "v" + version,
		NotesURL: "https://github.com/" + Repository + "/releases/tag/v" + version,
	}, true, nil
}

type semanticVersion struct {
	major int
	minor int
	patch int
}

func (v semanticVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
}

func parseVersion(raw string) (semanticVersion, bool) {
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "v")
	if raw == "" || strings.ContainsAny(raw, "-+") {
		return semanticVersion{}, false
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return semanticVersion{}, false
	}
	values := make([]int, 3)
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return semanticVersion{}, false
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return semanticVersion{}, false
		}
		values[i] = n
	}
	return semanticVersion{major: values[0], minor: values[1], patch: values[2]}, true
}

func compareVersion(a, b semanticVersion) int {
	switch {
	case a.major != b.major:
		return compareInt(a.major, b.major)
	case a.minor != b.minor:
		return compareInt(a.minor, b.minor)
	default:
		return compareInt(a.patch, b.patch)
	}
}

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
