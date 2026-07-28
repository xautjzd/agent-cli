package update

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{"0.1.1", "0.1.1", true},
		{"v12.3.40", "12.3.40", true},
		{"dev", "", false},
		{"1.2", "", false},
		{"1.2.3-beta.1", "", false},
		{"01.2.3", "", false},
		{"1.2.3+build", "", false},
	}
	for _, tt := range tests {
		got, ok := parseVersion(tt.input)
		if ok != tt.ok || (ok && got.String() != tt.want) {
			t.Errorf("parseVersion(%q) = (%q, %v), want (%q, %v)", tt.input, got.String(), ok, tt.want, tt.ok)
		}
	}
}

func TestVersionOrdering(t *testing.T) {
	t.Parallel()
	tests := []struct {
		a, b string
		want int
	}{
		{"0.1.1", "0.1.0", 1},
		{"0.1.1", "0.1.1", 0},
		{"0.1.1", "0.2.0", -1},
		{"2.0.0", "1.99.99", 1},
	}
	for _, tt := range tests {
		a, _ := parseVersion(tt.a)
		b, _ := parseVersion(tt.b)
		if got := compareVersion(a, b); got != tt.want {
			t.Errorf("compareVersion(%s, %s) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestLatestDetectsNewStableRelease(t *testing.T) {
	t.Parallel()
	checker, calls := testChecker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != "agent-cli/0.1.1" {
			t.Errorf("User-Agent = %q", got)
		}
		fmt.Fprint(w, `{"tag_name":"v0.2.0","draft":false,"prerelease":false}`)
	}))

	release, available, err := checker.Latest(context.Background(), "0.1.1")
	if err != nil {
		t.Fatal(err)
	}
	if !available || release.Version != "0.2.0" || release.Tag != "v0.2.0" {
		t.Fatalf("Latest = (%+v, %v), want v0.2.0 available", release, available)
	}
	if release.NotesURL != "https://github.com/xautjzd/agent-cli/releases/tag/v0.2.0" {
		t.Errorf("NotesURL = %q", release.NotesURL)
	}
	if *calls != 1 {
		t.Fatalf("HTTP calls = %d, want 1", *calls)
	}
}

func TestLatestSkipsDevelopmentBuildWithoutNetwork(t *testing.T) {
	t.Parallel()
	checker, calls := testChecker(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("development build should not access the network")
	}))
	if _, available, err := checker.Latest(context.Background(), "dev"); err != nil || available {
		t.Fatalf("Latest(dev) = available %v, err %v", available, err)
	}
	if *calls != 0 {
		t.Fatalf("HTTP calls = %d, want 0", *calls)
	}
}

func TestLatestIgnoresNonNewOrUnstableReleases(t *testing.T) {
	t.Parallel()
	tests := []string{
		`{"tag_name":"v0.1.1"}`,
		`{"tag_name":"v0.1.0"}`,
		`{"tag_name":"v0.2.0","draft":true}`,
		`{"tag_name":"v0.2.0-beta.1","prerelease":true}`,
		`{"tag_name":"not-a-version"}`,
	}
	for _, body := range tests {
		body := body
		t.Run(body, func(t *testing.T) {
			checker, _ := testChecker(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, body)
			}))
			if _, available, err := checker.Latest(context.Background(), "0.1.1"); err != nil || available {
				t.Fatalf("Latest = available %v, err %v", available, err)
			}
		})
	}
}

func TestLatestBoundsMetadataAndPropagatesTimeout(t *testing.T) {
	t.Parallel()
	t.Run("oversized", func(t *testing.T) {
		checker, _ := testChecker(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, strings.Repeat("x", maxMetadataBytes+1))
		}))
		if _, _, err := checker.Latest(context.Background(), "0.1.1"); err == nil ||
			!strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("oversized metadata error = %v", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		checker, _ := testChecker(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			time.Sleep(100 * time.Millisecond)
		}))
		checker.client.Timeout = 10 * time.Millisecond
		if _, _, err := checker.Latest(context.Background(), "0.1.1"); err == nil {
			t.Fatal("expected timeout error")
		}
	})
}

func testChecker(t *testing.T, handler http.Handler) (Checker, *int) {
	t.Helper()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return Checker{
		client:   server.Client(),
		endpoint: server.URL,
	}, &calls
}
