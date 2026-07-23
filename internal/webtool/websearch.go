package webtool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Result is one web-search hit.
type Result struct {
	Title   string
	URL     string
	Snippet string
}

// Searcher performs a web search. Backends: DuckDuckGo (keyless, the default),
// Brave, and Tavily (API-key). The agent depends only on this interface (DIP).
type Searcher interface {
	// Name identifies the backend for diagnostics.
	Name() string
	// Search returns up to count results for the query.
	Search(ctx context.Context, query string, count int) ([]Result, error)
}

// WebSearch is the tool the agent calls to find current information.
type WebSearch struct {
	Searcher Searcher
}

func (t *WebSearch) Name() string { return "web_search" }

func (t *WebSearch) Description() string {
	return "Search the web and return titles, URLs, and snippets. Use it to find current " +
		"documentation, API references, library versions, or explanations of errors, then " +
		"web_fetch a result URL to read it in full."
}

func (t *WebSearch) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "The search query"},
			"count": {"type": "integer", "description": "Max results to return (default 5, max 10)"}
		},
		"required": ["query"]
	}`)
}

func (t *WebSearch) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Query string `json:"query"`
		Count int    `json:"count"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	q := strings.TrimSpace(args.Query)
	if q == "" {
		return "", fmt.Errorf("query must not be empty")
	}
	if t.Searcher == nil {
		return "", fmt.Errorf("web search is not configured")
	}
	count := args.Count
	if count <= 0 {
		count = 5
	}
	if count > 10 {
		count = 10
	}

	results, err := t.Searcher.Search(ctx, q, count)
	if err != nil {
		return "", fmt.Errorf("search failed (%s): %w", t.Searcher.Name(), err)
	}
	if len(results) == 0 {
		return fmt.Sprintf("No results for %q (via %s).", q, t.Searcher.Name()), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Results for %q (via %s):\n", q, t.Searcher.Name())
	for i, r := range results {
		fmt.Fprintf(&b, "\n%d. %s\n   %s\n", i+1, r.Title, r.URL)
		if r.Snippet != "" {
			fmt.Fprintf(&b, "   %s\n", oneLine(r.Snippet, 300))
		}
	}
	return b.String(), nil
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}
