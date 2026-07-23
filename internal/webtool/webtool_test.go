package webtool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTMLToText(t *testing.T) {
	h := `<!doctype html><html><head><title>My &amp; Docs</title>
	<style>.x{color:red}</style><script>alert(1)</script></head>
	<body><h1>Heading</h1><p>First&nbsp;paragraph with <a href="x">a link</a>.</p>
	<p>Second   paragraph.</p></body></html>`
	title, text := htmlToText(h)
	if title != "My & Docs" {
		t.Errorf("title = %q", title)
	}
	if strings.Contains(text, "alert") || strings.Contains(text, "color:red") {
		t.Errorf("script/style leaked into text:\n%s", text)
	}
	if !strings.Contains(text, "First paragraph with a link.") {
		t.Errorf("paragraph text wrong:\n%s", text)
	}
	if !strings.Contains(text, "Heading") || !strings.Contains(text, "Second paragraph.") {
		t.Errorf("missing content:\n%s", text)
	}
}

func TestWebFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head><title>Doc</title></head><body><p>Hello world content.</p></body></html>`))
	}))
	defer srv.Close()

	f := &WebFetch{}
	out, err := f.Execute(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "# Doc") || !strings.Contains(out, "Hello world content.") {
		t.Errorf("fetch output wrong:\n%s", out)
	}
}

func TestWebFetchPlainText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("raw error log line: panic at 0x0"))
	}))
	defer srv.Close()
	f := &WebFetch{}
	out, _ := f.Execute(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`"}`))
	if !strings.Contains(out, "raw error log line: panic at 0x0") {
		t.Errorf("plain text should pass through:\n%s", out)
	}
}

func TestWebFetchRejectsBadURL(t *testing.T) {
	f := &WebFetch{}
	for _, u := range []string{`{"url":"ftp://x"}`, `{"url":"not a url"}`, `{"url":"http://169.254.169.254/latest"}`} {
		if _, err := f.Execute(context.Background(), json.RawMessage(u)); err == nil {
			t.Errorf("should reject %s", u)
		}
	}
}

func TestBraveSearchParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Subscription-Token") != "k" {
			t.Errorf("missing brave token header")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"web": map[string]any{"results": []map[string]string{
				{"title": "Go docs", "url": "https://go.dev", "description": "The Go programming language"},
			}},
		})
	}))
	defer srv.Close()

	b := &braveSearcher{key: "k", client: srv.Client()}
	// Point the backend at the test server by overriding the URL via a custom
	// round trip: simplest is to test through NewSearcher won't hit test URL,
	// so exercise the parser by calling the server directly.
	res, err := b.searchAt(context.Background(), srv.URL+"/res/v1/web/search?q=go&count=5")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Title != "Go docs" || res[0].URL != "https://go.dev" {
		t.Errorf("brave parse wrong: %+v", res)
	}
}

func TestTavilySearchParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]string{
				{"title": "Result", "url": "https://x.com", "content": "snippet text"},
			},
		})
	}))
	defer srv.Close()
	tv := &tavilySearcher{key: "k", client: srv.Client()}
	res, err := tv.searchAt(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Snippet != "snippet text" {
		t.Errorf("tavily parse wrong: %+v", res)
	}
}

func TestDDGDecodeURL(t *testing.T) {
	got := ddgDecodeURL(`//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc&rut=abc`)
	if got != "https://go.dev/doc" {
		t.Errorf("ddg url decode = %q", got)
	}
	if ddgDecodeURL("https://direct.example.com") != "https://direct.example.com" {
		t.Error("direct url should pass through")
	}
}

func TestNewSearcherSelectsBackend(t *testing.T) {
	if NewSearcher("brave", "k", nil).Name() != "brave" {
		t.Error("brave not selected")
	}
	if NewSearcher("tavily", "k", nil).Name() != "tavily" {
		t.Error("tavily not selected")
	}
	if NewSearcher("", "", nil).Name() != "duckduckgo" {
		t.Error("default should be duckduckgo")
	}
	if NewSearcher("unknown", "", nil).Name() != "duckduckgo" {
		t.Error("unknown should fall back to duckduckgo")
	}
}

func TestWebSearchEmptyQuery(t *testing.T) {
	s := &WebSearch{Searcher: NewSearcher("", "", nil)}
	if _, err := s.Execute(context.Background(), json.RawMessage(`{"query":"  "}`)); err == nil {
		t.Error("empty query should error")
	}
}

func TestHTMLToMarkdown(t *testing.T) {
	h := `<html><head><title>API</title></head><body>
	<h2>Timeouts</h2>
	<p>Set a <a href="https://go.dev/pkg/net/http">client timeout</a>.</p>
	<ul><li>read</li><li>write</li></ul>
	<pre>client := &http.Client{}</pre></body></html>`
	title, md := htmlToMarkdown(h)
	if title != "API" {
		t.Errorf("title = %q", title)
	}
	if !strings.Contains(md, "## Timeouts") {
		t.Errorf("heading not markdown:\n%s", md)
	}
	if !strings.Contains(md, "[client timeout](https://go.dev/pkg/net/http)") {
		t.Errorf("link not markdown:\n%s", md)
	}
	if !strings.Contains(md, "- read") || !strings.Contains(md, "- write") {
		t.Errorf("list not markdown:\n%s", md)
	}
	if !strings.Contains(md, "```") {
		t.Errorf("pre not fenced:\n%s", md)
	}
}

func TestWebFetchPromptExtraction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><body><p>The default timeout is 30 seconds. Other stuff.</p></body></html>`))
	}))
	defer srv.Close()

	var gotPrompt, gotContent string
	f := &WebFetch{Extract: func(_ context.Context, prompt, content string) (string, error) {
		gotPrompt, gotContent = prompt, content
		return "timeout = 30s", nil
	}}
	out, err := f.Execute(context.Background(), json.RawMessage(
		`{"url":"`+srv.URL+`","prompt":"what is the default timeout?"}`))
	if err != nil {
		t.Fatal(err)
	}
	if gotPrompt != "what is the default timeout?" {
		t.Errorf("extractor prompt = %q", gotPrompt)
	}
	if !strings.Contains(gotContent, "30 seconds") {
		t.Errorf("extractor content missing page: %q", gotContent)
	}
	if !strings.Contains(out, "timeout = 30s") || !strings.Contains(out, "Extracted from") {
		t.Errorf("extraction output wrong:\n%s", out)
	}
}

func TestWebFetchCaches(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><body><p>cached body</p></body></html>`))
	}))
	defer srv.Close()

	f := &WebFetch{}
	args := json.RawMessage(`{"url":"` + srv.URL + `/cache-test"}`)
	f.Execute(context.Background(), args)
	f.Execute(context.Background(), args)
	if hits != 1 {
		t.Errorf("expected 1 server hit (cached), got %d", hits)
	}
}
