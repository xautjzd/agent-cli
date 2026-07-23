package webtool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// NewSearcher builds a search backend by name. "duckduckgo" (default) needs no
// key; "brave" and "tavily" require an API key. An unknown name falls back to
// DuckDuckGo.
func NewSearcher(provider, apiKey string, client *http.Client) Searcher {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "brave":
		return &braveSearcher{key: apiKey, client: client}
	case "tavily":
		return &tavilySearcher{key: apiKey, client: client}
	default:
		return &ddgSearcher{client: client}
	}
}

// --- DuckDuckGo (keyless) ---------------------------------------------------

// ddgSearcher scrapes DuckDuckGo's no-JavaScript HTML endpoint. It needs no API
// key, which makes it the zero-config default, but it is a scrape: if the page
// markup changes it may stop returning results, in which case configure Brave
// or Tavily.
type ddgSearcher struct{ client *http.Client }

func (d *ddgSearcher) Name() string { return "duckduckgo" }

var (
	reDDGLink    = regexp.MustCompile(`(?is)<a[^>]+class="[^"]*result__a[^"]*"[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	reDDGSnippet = regexp.MustCompile(`(?is)<a[^>]+class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</a>`)
)

func (d *ddgSearcher) Search(ctx context.Context, query string, count int) ([]Result, error) {
	endpoint := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", defaultUA)
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	h := string(body)

	links := reDDGLink.FindAllStringSubmatch(h, -1)
	snippets := reDDGSnippet.FindAllStringSubmatch(h, -1)

	var out []Result
	for i, m := range links {
		if len(out) >= count {
			break
		}
		r := Result{URL: ddgDecodeURL(m[1]), Title: normalizeInline(m[2])}
		if i < len(snippets) {
			r.Snippet = normalizeInline(snippets[i][1])
		}
		if r.URL != "" && r.Title != "" {
			out = append(out, r)
		}
	}
	return out, nil
}

// ddgDecodeURL unwraps DuckDuckGo's redirect links (…/l/?uddg=<encoded>).
func ddgDecodeURL(href string) string {
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	if real := u.Query().Get("uddg"); real != "" {
		return real
	}
	return href
}

// --- Brave (API key) --------------------------------------------------------

type braveSearcher struct {
	key      string
	client   *http.Client
	endpoint string // overridable for tests; empty uses the real API
}

func (b *braveSearcher) Name() string { return "brave" }

func (b *braveSearcher) base() string {
	if b.endpoint != "" {
		return b.endpoint
	}
	return "https://api.search.brave.com/res/v1/web/search"
}

func (b *braveSearcher) Search(ctx context.Context, query string, count int) ([]Result, error) {
	if b.key == "" {
		return nil, fmt.Errorf("brave search needs an API key (set web_search.api_key or BRAVE_API_KEY)")
	}
	return b.searchAt(ctx, fmt.Sprintf("%s?q=%s&count=%d", b.base(), url.QueryEscape(query), count))
}

func (b *braveSearcher) searchAt(ctx context.Context, endpoint string) ([]Result, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	req.Header.Set("X-Subscription-Token", b.key)
	req.Header.Set("Accept", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	var payload struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	var out []Result
	for _, r := range payload.Web.Results {
		out = append(out, Result{Title: normalizeInline(r.Title), URL: r.URL, Snippet: normalizeInline(r.Description)})
	}
	return out, nil
}

// --- Tavily (API key) -------------------------------------------------------

type tavilySearcher struct {
	key      string
	client   *http.Client
	endpoint string // overridable for tests
}

func (t *tavilySearcher) Name() string { return "tavily" }

func (t *tavilySearcher) Search(ctx context.Context, query string, count int) ([]Result, error) {
	if t.key == "" {
		return nil, fmt.Errorf("tavily search needs an API key (set web_search.api_key or TAVILY_API_KEY)")
	}
	reqBody, _ := json.Marshal(map[string]any{"api_key": t.key, "query": query, "max_results": count})
	return t.searchAt(ctx, orDefault(t.endpoint, "https://api.tavily.com/search"), reqBody)
}

func (t *tavilySearcher) searchAt(ctx context.Context, endpoint string, reqBody ...[]byte) ([]Result, error) {
	var body []byte
	if len(reqBody) > 0 {
		body = reqBody[0]
	} else {
		body, _ = json.Marshal(map[string]any{"api_key": t.key, "query": "test", "max_results": 5})
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	var payload struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	var out []Result
	for _, r := range payload.Results {
		out = append(out, Result{Title: normalizeInline(r.Title), URL: r.URL, Snippet: normalizeInline(r.Content)})
	}
	return out, nil
}
