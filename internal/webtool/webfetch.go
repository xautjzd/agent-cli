package webtool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// WebFetch retrieves a URL and returns its content as Markdown, so the agent
// can read documentation, a changelog, an API reference, or a linked error
// page. Following Claude Code, an optional prompt makes it return just the
// content relevant to that prompt (extracted by a model) instead of the whole
// page — keeping the conversation lean.
type WebFetch struct {
	// Client is the HTTP client; nil uses a default with a timeout.
	Client *http.Client
	// MaxBytes caps the response read (default 2 MiB).
	MaxBytes int64
	// UserAgent overrides the request UA.
	UserAgent string
	// Extract, when set, is used to distill fetched content against a prompt
	// (wired to a model at the composition root). Nil returns the full page.
	Extract func(ctx context.Context, prompt, content string) (string, error)
}

func (t *WebFetch) Name() string { return "web_fetch" }

func (t *WebFetch) Description() string {
	return "Fetch a URL and return its content as Markdown (HTML is converted; links and " +
		"headings are preserved). Optionally pass a prompt to get back only the parts of the " +
		"page relevant to it. Use it to read documentation, API references, changelogs, or a " +
		"page found via web_search."
}

func (t *WebFetch) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"url": {"type": "string", "description": "The absolute http(s) URL to fetch"},
			"prompt": {"type": "string", "description": "Optional: what to extract from the page (returns only the relevant content)"},
			"max_chars": {"type": "integer", "description": "Max characters of content to return when no prompt is given (default 15000)"}
		},
		"required": ["url"]
	}`)
}

func (t *WebFetch) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		URL      string `json:"url"`
		Prompt   string `json:"prompt"`
		MaxChars int    `json:"max_chars"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	u, err := url.Parse(strings.TrimSpace(args.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("url must be an absolute http(s) URL")
	}
	if isBlockedHost(u.Hostname()) {
		return "", fmt.Errorf("refusing to fetch a link-local/metadata address")
	}

	title, content, err := t.load(ctx, u.String())
	if err != nil {
		return "", err
	}

	// With a prompt and an extractor, return only the relevant content.
	if p := strings.TrimSpace(args.Prompt); p != "" && t.Extract != nil {
		src := content
		if len(src) > 40000 {
			src = src[:40000] // bound the extractor's input
		}
		out, err := t.Extract(ctx, p, "URL: "+u.String()+"\nTitle: "+title+"\n\n"+src)
		if err == nil && strings.TrimSpace(out) != "" {
			return fmt.Sprintf("Extracted from %s (prompt: %s)\n\n%s", u.String(), p, strings.TrimSpace(out)), nil
		}
		// On extractor failure, fall through to returning the page.
	}

	maxChars := args.MaxChars
	if maxChars <= 0 {
		maxChars = 15000
	}
	header := ""
	if title != "" {
		header = "# " + title + "\n\n"
	}
	trunc := ""
	if len(content) > maxChars {
		content = content[:maxChars]
		trunc = fmt.Sprintf("\n\n… (truncated at %d chars — refetch with a `prompt` to extract specifics)", maxChars)
	}
	if content == "" {
		content = "(no readable text extracted)"
	}
	return fmt.Sprintf("Fetched %s\n%s%s%s", u.String(), header, content, trunc), nil
}

// load fetches a URL (through the cache) and returns its title and Markdown.
func (t *WebFetch) load(ctx context.Context, urlStr string) (title, content string, err error) {
	if c, ok := fetchCache.get(urlStr); ok {
		return c.title, c.content, nil
	}
	maxBytes := t.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 2 << 20
	}
	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", orDefault(t.UserAgent, defaultUA))
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,application/json;q=0.9,*/*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("fetch failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	raw := string(body)

	content = strings.TrimSpace(raw)
	if looksLikeHTML(resp.Header.Get("Content-Type"), raw) {
		title, content = htmlToMarkdown(raw)
	}
	if resp.StatusCode != http.StatusOK {
		content = fmt.Sprintf("(HTTP %d)\n%s", resp.StatusCode, content)
	}
	fetchCache.put(urlStr, title, content)
	return title, content, nil
}

const defaultUA = "agent-cli/0.1 (+https://github.com/xautjzd/agent-cli)"

// --- fetch cache ------------------------------------------------------------

// A short-lived in-memory cache avoids refetching the same URL during a session
// (Claude Code caches ~15 min).
type cacheEntry struct {
	title, content string
	at             time.Time
}

type urlCache struct {
	mu  sync.Mutex
	m   map[string]cacheEntry
	ttl time.Duration
}

var fetchCache = &urlCache{m: map[string]cacheEntry{}, ttl: 15 * time.Minute}

func (c *urlCache) get(url string) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[url]
	if !ok || time.Since(e.at) > c.ttl {
		return cacheEntry{}, false
	}
	return e, true
}

func (c *urlCache) put(url, title, content string) {
	c.mu.Lock()
	c.m[url] = cacheEntry{title: title, content: content, at: time.Now()}
	c.mu.Unlock()
}

// isBlockedHost blocks the cloud metadata address and link-local range to
// avoid the classic SSRF target. Loopback/private hosts stay allowed so local
// documentation servers work.
func isBlockedHost(host string) bool {
	if host == "" {
		return true
	}
	if strings.EqualFold(host, "metadata.google.internal") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLinkLocalUnicast() || ip.Equal(net.ParseIP("169.254.169.254"))
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
