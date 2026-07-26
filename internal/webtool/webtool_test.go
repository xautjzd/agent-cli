package webtool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestGoogleSearchParsing(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]string{
				{"title": "The Go Programming Language", "link": "https://go.dev/", "snippet": "Build simple systems."},
				{"title": "Go Docs", "link": "https://go.dev/doc", "snippet": "Documentation."},
			},
		})
	}))
	defer srv.Close()

	g := &googleSearcher{key: "k", engineID: "cx123", client: srv.Client(), endpoint: srv.URL}
	res, err := g.Search(context.Background(), "go docs", 5)
	if err != nil {
		t.Fatal(err)
	}
	// Both credentials must reach the API: a missing cx is a 400 in production.
	if gotQuery.Get("key") != "k" || gotQuery.Get("cx") != "cx123" {
		t.Errorf("credentials not sent: %v", gotQuery)
	}
	if gotQuery.Get("q") != "go docs" || gotQuery.Get("num") != "5" {
		t.Errorf("query params wrong: %v", gotQuery)
	}
	if len(res) != 2 || res[0].URL != "https://go.dev/" || res[1].Snippet != "Documentation." {
		t.Errorf("google parse wrong: %+v", res)
	}
}

// Google caps num at 10 and rejects more, so the request must be clamped even
// though the tool's own ceiling already is 10.
func TestGoogleClampsCount(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("num")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	g := &googleSearcher{key: "k", engineID: "cx", client: srv.Client(), endpoint: srv.URL}
	if _, err := g.Search(context.Background(), "q", 50); err != nil {
		t.Fatal(err)
	}
	if got != "10" {
		t.Errorf("num = %q, want it clamped to 10", got)
	}
}

// Google explains a refusal in the body; that reason is what the user needs.
func TestGoogleSurfacesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"code":429,"message":"Quota exceeded for queries per day"}}`))
	}))
	defer srv.Close()

	g := &googleSearcher{key: "k", engineID: "cx", client: srv.Client(), endpoint: srv.URL}
	_, err := g.Search(context.Background(), "q", 5)
	if err == nil {
		t.Fatal("a quota refusal should error")
	}
	if !strings.Contains(err.Error(), "Quota exceeded") {
		t.Errorf("error should carry Google's reason: %v", err)
	}
}

// No match omits "items" entirely — that is an empty result set, not a failure.
func TestGoogleEmptyResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"kind":"customsearch#search"}`))
	}))
	defer srv.Close()

	g := &googleSearcher{key: "k", engineID: "cx", client: srv.Client(), endpoint: srv.URL}
	res, err := g.Search(context.Background(), "q", 5)
	if err != nil {
		t.Fatalf("no results should not be an error: %v", err)
	}
	if len(res) != 0 {
		t.Errorf("expected no results, got %+v", res)
	}
}

func TestCheckCredentials(t *testing.T) {
	// Keyless engines never need anything.
	for _, p := range []string{"duckduckgo", "bing", "bing-cn", "baidu", "yahoo"} {
		if err := CheckCredentials(p, Credentials{}); err != nil {
			t.Errorf("%s should need no credentials: %v", p, err)
		}
	}
	if err := CheckCredentials("brave", Credentials{}); err == nil ||
		!strings.Contains(err.Error(), "BRAVE_API_KEY") {
		t.Errorf("brave error should name its env var: %v", err)
	}
	// Google needs both halves, and should say which one is missing.
	if err := CheckCredentials("google", Credentials{EngineID: "cx"}); err == nil ||
		!strings.Contains(err.Error(), "GOOGLE_API_KEY") {
		t.Errorf("missing key should name GOOGLE_API_KEY: %v", err)
	}
	if err := CheckCredentials("google", Credentials{APIKey: "k"}); err == nil ||
		!strings.Contains(err.Error(), "GOOGLE_SEARCH_ENGINE_ID") {
		t.Errorf("missing engine id should name GOOGLE_SEARCH_ENGINE_ID: %v", err)
	}
	if err := CheckCredentials("google", Credentials{APIKey: "k", EngineID: "cx"}); err != nil {
		t.Errorf("complete google credentials should pass: %v", err)
	}
}

// The backend refuses before spending a request when a credential is missing.
func TestGoogleRefusesWithoutCredentials(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()

	g := &googleSearcher{key: "k", client: srv.Client(), endpoint: srv.URL} // no engine ID
	if _, err := g.Search(context.Background(), "q", 5); err == nil {
		t.Fatal("missing engine ID should error")
	}
	if called {
		t.Error("should not call the API with incomplete credentials")
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
	for name, want := range map[string]string{
		"brave":       "brave",
		"tavily":      "tavily",
		"baidu":       "baidu",
		"BAIDU":       "baidu",
		"百度":          "baidu",
		"bing":        "bing",
		" Bing.com ":  "bing",
		"bing-cn":     "bing-cn",
		"cn.bing.com": "bing-cn",
		"bing_cn":     "bing-cn",
		"yahoo":       "yahoo",
		"google":      "google",
		"google.com":  "google",
		"":            "duckduckgo",
		"unknown":     "duckduckgo",
	} {
		if got := NewSearcher(name, Credentials{APIKey: "k"}, nil).Name(); got != want {
			t.Errorf("NewSearcher(%q) = %q, want %q", name, got, want)
		}
	}
}

// Each keyless backend is a scrape, so the parser is the part worth pinning
// down: sample markup in, results out.
func TestHTMLSearcherParsing(t *testing.T) {
	tests := []struct {
		name     string
		searcher *htmlSearcher
		page     string
		want     []Result
	}{
		{
			name:     "duckduckgo",
			searcher: newDuckDuckGo(nil),
			page: `<div class="result results_links web-result">
				<a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc&amp;rut=x">Go <b>Docs</b></a>
				<a class="result__snippet" href="x">The Go docs.</a></div>
			<div class="result results_links web-result">
				<a rel="nofollow" class="result__a" href="https://pkg.go.dev">pkg.go.dev</a>
				<a class="result__snippet" href="x">Package index.</a></div>`,
			want: []Result{
				{Title: "Go Docs", URL: "https://go.dev/doc", Snippet: "The Go docs."},
				{Title: "pkg.go.dev", URL: "https://pkg.go.dev", Snippet: "Package index."},
			},
		},
		{
			name:     "bing",
			searcher: newBing(nil),
			page: `<li class="b_algo"><h2><a href="https://go.dev/" h="ID=SERP,1">Go</a></h2>
				<div><p class="b_lineclamp2">Build simple, secure systems.</p></div></li>
			<li class="b_algo"><h2><a href="https://go.dev/blog/" h="ID=SERP,2">The Go Blog</a></h2>
				<div><p>News from the Go team.</p></div></li>`,
			want: []Result{
				{Title: "Go", URL: "https://go.dev/", Snippet: "Build simple, secure systems."},
				{Title: "The Go Blog", URL: "https://go.dev/blog/", Snippet: "News from the Go team."},
			},
		},
		{
			name:     "bing-cn keeps relative links fetchable",
			searcher: newBingCN(nil),
			page: `<li class="b_algo"><h2><a href="/ck/a?u=x">中文标题</a></h2>
				<p>中文摘要。</p></li>`,
			want: []Result{{Title: "中文标题", URL: "https://cn.bing.com/ck/a?u=x", Snippet: "中文摘要。"}},
		},
		{
			name:     "baidu",
			searcher: newBaidu(nil),
			page: `<div class="result-op c-container" tpl="wenda_generate"><h3><a href="/ai">AI 卡片</a></h3></div>
			<div class="result c-container" tpl="www_index"><h3 class="cosc-title t"><a class="cosc-title-a" href="http://www.baidu.com/link?url=AbC" target="_blank" href="http://www.baidu.com/link?url=AbC"><span><!--s-text-->Go <em>语言</em>教程<!--/s-text--></span></a></h3>
				<div data-sanssr-cmpt="card/www-summary-1"><!--s-data:{"summaryData":{"generalLines":[{"data":[{"text":"Go 是一门<em>开源</em>编程语言。"}],"clamp":3}]},"isPc":true}--></div></div>
			<div class="result c-container" tpl="www_index"><h3 class="cosc-title t"><a href="/link?url=DeF">Go 官网</a></h3>
				<div><!--s-data:{"summaryData":{"generalLines":[{"data":[{"text":"官方网站。"}]}]}}--></div></div>`,
			want: []Result{
				{Title: "Go 语言教程", URL: "http://www.baidu.com/link?url=AbC", Snippet: "Go 是一门开源编程语言。"},
				{Title: "Go 官网", URL: "https://www.baidu.com/link?url=DeF", Snippet: "官方网站。"},
			},
		},
		{
			// Yahoo opens the anchor before the heading and pads results with
			// "see more" links and code samples that must not be mistaken for
			// the next result's title.
			name:     "yahoo",
			searcher: newYahoo(nil),
			page: `<div class="dd algo algo-sr relsrch"><a target="_blank" referrerpolicy="origin" href="https://stackoverflow.com/questions/16895294"><div class="thmb">Stack Overflow</div><h3 class="title fc-2015C2-imp"><span>How to set a timeout</span></h3></a>
				<div class="compText"><p class="fc-dustygray">Use <b>client</b> timeout.</p></div>
				<a href="https://stackoverflow.com/other">See more on stackoverflow</a>
				<div class="compTitle"><h3 class="title"><span>Code sample</span></h3></div></div>
			<div class="dd lst algo algo-sr"><a referrerpolicy="origin" href="https://r.search.yahoo.com/_ylt=A0/RV=2/RU=https%3a%2f%2fgo.dev%2fdoc/RK=2/RS=abc"><h3 class="title"><span>Go Docs</span></h3></a>
				<div class="compText"><p>The Go programming language.</p></div></div>`,
			want: []Result{
				{Title: "How to set a timeout", URL: "https://stackoverflow.com/questions/16895294", Snippet: "Use client timeout."},
				{Title: "Go Docs", URL: "https://go.dev/doc", Snippet: "The Go programming language."},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.searcher.parse(tc.page, 10)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d results, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("result %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestHTMLSearcherHonorsCount(t *testing.T) {
	page := strings.Repeat(`<li class="b_algo"><h2><a href="https://x.com/">X</a></h2><p>s</p></li>`, 8)
	if got := newBing(nil).parse(page, 3); len(got) != 3 {
		t.Errorf("count not honored: got %d results, want 3", len(got))
	}
}

// A keyless backend must reach the engine it is named after — a wrong host is
// invisible in parser tests but breaks every search.
func TestKeylessBackendEndpoints(t *testing.T) {
	for _, tc := range []struct{ provider, wantHost, wantQuery string }{
		{"duckduckgo", "html.duckduckgo.com", "q=go+docs"},
		{"bing", "www.bing.com", "q=go+docs"},
		{"bing-cn", "cn.bing.com", "q=go+docs"},
		{"baidu", "www.baidu.com", "wd=go+docs"},
		{"yahoo", "search.yahoo.com", "p=go+docs"},
	} {
		s, ok := NewSearcher(tc.provider, Credentials{}, nil).(*htmlSearcher)
		if !ok {
			t.Fatalf("%s is not an htmlSearcher", tc.provider)
		}
		got := s.endpoint("go docs", 5)
		if !strings.HasPrefix(got, "https://"+tc.wantHost+"/") || !strings.Contains(got, tc.wantQuery) {
			t.Errorf("%s endpoint = %q, want host %s with %s", tc.provider, got, tc.wantHost, tc.wantQuery)
		}
	}
}

func TestHTMLSearcherSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Language") == "" {
			t.Error("Accept-Language header not sent")
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<li class="b_algo"><h2><a href="https://go.dev/">Go</a></h2><p>Snippet.</p></li>`))
	}))
	defer srv.Close()

	s := newBing(srv.Client())
	s.endpoint = func(string, int) string { return srv.URL }
	res, err := s.Search(context.Background(), "go", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].URL != "https://go.dev/" || res[0].Snippet != "Snippet." {
		t.Errorf("search parse wrong: %+v", res)
	}
}

// Baidu answers a cold search with a captcha and only serves results once it
// has seen the session cookie from its home page.
func TestHTMLSearcherWarmsUpAfterBotCheck(t *testing.T) {
	var searches int
	mux := http.NewServeMux()
	mux.HandleFunc("/home", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SESSION", Value: "ok", Path: "/"})
		w.Write([]byte("<html>home</html>"))
	})
	mux.HandleFunc("/s", func(w http.ResponseWriter, r *http.Request) {
		searches++
		if c, err := r.Cookie("SESSION"); err != nil || c.Value != "ok" {
			w.Write([]byte(`<html><body>安全验证</body></html>`))
			return
		}
		w.Write([]byte(`<div class="result c-container"><h3><a href="/link?url=A">标题</a></h3>` +
			`<!--s-data:{"summaryData":{"data":[{"text":"摘要"}]}}--></div>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := newBaidu(srv.Client())
	s.endpoint = func(string, int) string { return srv.URL + "/s" }
	s.warmupURL = srv.URL + "/home"
	s.decodeURL = absoluteURL(srv.URL)

	res, err := s.Search(context.Background(), "标题", 5)
	if err != nil {
		t.Fatal(err)
	}
	if searches != 2 {
		t.Errorf("expected a retry after the bot check, got %d searches", searches)
	}
	if len(res) != 1 || res[0].Title != "标题" || res[0].Snippet != "摘要" {
		t.Fatalf("result wrong: %+v", res)
	}

	// The cookie is kept, so a second search does not pay for another warm-up.
	if _, err := s.Search(context.Background(), "标题", 5); err != nil {
		t.Fatal(err)
	}
	if searches != 3 {
		t.Errorf("second search should not re-warm: %d searches total", searches)
	}
}

func TestHTMLSearcherReportsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "blocked", http.StatusForbidden)
	}))
	defer srv.Close()

	s := newBaidu(srv.Client())
	s.endpoint = func(string, int) string { return srv.URL }
	if _, err := s.Search(context.Background(), "go", 5); err == nil {
		t.Error("a bot-check response should surface as an error, not empty results")
	}
}

func TestYahooDecodeURL(t *testing.T) {
	got := yahooDecodeURL(`https://r.search.yahoo.com/_ylt=A0/RV=2/RU=https%3a%2f%2fgo.dev%2fdoc/RK=2/RS=x`)
	if got != "https://go.dev/doc" {
		t.Errorf("yahoo url decode = %q", got)
	}
	if yahooDecodeURL("https://direct.example.com") != "https://direct.example.com" {
		t.Error("direct url should pass through")
	}
}

func TestWebSearchEmptyQuery(t *testing.T) {
	s := &WebSearch{Searcher: NewSearcher("", Credentials{}, nil)}
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
