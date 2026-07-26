package webtool

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveEngines queries the real search engines. The keyless backends are
// scrapes, so they rot silently when an engine changes its markup and no unit
// test can catch that — run this to check them:
//
//	LIVE=1 go test ./internal/webtool/ -run TestLiveEngines -v
//
// It reports rather than fails on empty results: engines rate-limit by IP
// (Baidu especially, and aggressively), so an empty run means "blocked here and
// now", not "broken". Google is only queried when GOOGLE_API_KEY and
// GOOGLE_SEARCH_ENGINE_ID are set, since it has no keyless path.
func TestLiveEngines(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("set LIVE=1 to query real search engines")
	}
	// Google is included only when its credentials are in the environment: it
	// is API-only, and its free tier is 100 queries/day.
	creds := Credentials{
		APIKey:   os.Getenv("GOOGLE_API_KEY"),
		EngineID: os.Getenv("GOOGLE_SEARCH_ENGINE_ID"),
	}
	providers := []string{"duckduckgo", "bing", "bing-cn", "baidu", "yahoo"}
	if CheckCredentials("google", creds) == nil {
		providers = append(providers, "google")
	}

	for _, provider := range providers {
		t.Run(provider, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			results, err := NewSearcher(provider, creds, nil).Search(ctx, "golang http client timeout", 5)
			if err != nil {
				t.Logf("error: %v", err)
				return
			}
			t.Logf("%d results", len(results))
			for _, r := range results {
				if r.Title == "" || r.URL == "" {
					t.Errorf("incomplete result: %+v", r)
				}
				t.Logf("  %s\n    %s\n    %s", r.Title, r.URL, r.Snippet)
			}
		})
	}
}
