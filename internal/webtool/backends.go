package webtool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Credentials carries what an API-backed engine needs to authenticate. The
// keyless engines ignore it. It is a struct rather than a bare key because
// Google needs two values, and a backend needing a second one is evidently not
// a special case.
type Credentials struct {
	// APIKey authenticates against the engine's API.
	APIKey string
	// EngineID is Google's Programmable Search engine identifier ("cx"),
	// which selects *what* is searched; unused by the other backends.
	EngineID string
}

// NewSearcher builds a search backend by name. The keyless backends scrape a
// search engine's HTML page: "duckduckgo" (the default), "baidu", "bing",
// "bing-cn" (cn.bing.com), and "yahoo". "google", "brave" and "tavily" use an
// official API and require credentials. An unknown name falls back to
// DuckDuckGo.
func NewSearcher(provider string, creds Credentials, client *http.Client) Searcher {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	switch normalizeProvider(provider) {
	case "google":
		return &googleSearcher{key: creds.APIKey, engineID: creds.EngineID, client: client}
	case "brave":
		return &braveSearcher{key: creds.APIKey, client: client}
	case "tavily":
		return &tavilySearcher{key: creds.APIKey, client: client}
	case "baidu":
		return newBaidu(client)
	case "bing":
		return newBing(client)
	case "bing-cn":
		return newBingCN(client)
	case "yahoo":
		return newYahoo(client)
	default:
		return newDuckDuckGo(client)
	}
}

// Providers lists the selectable backends in the order they are offered:
// keyless engines first, since they work with no setup, then the API ones.
// This is the single source of truth for the config validator and the picker.
func Providers() []string {
	return []string{"duckduckgo", "bing", "bing-cn", "baidu", "yahoo", "google", "brave", "tavily"}
}

// CheckCredentials reports what a backend is still missing, so a switch can be
// refused while the user can still act on the reason instead of failing at the
// first search. Keyless engines never need anything.
func CheckCredentials(provider string, creds Credentials) error {
	name, _ := CanonicalProvider(provider)
	switch name {
	case "google":
		// Google needs both halves: the key authenticates, the engine ID says
		// what to search. Either alone returns HTTP 400 at query time.
		if creds.APIKey == "" {
			return fmt.Errorf("google needs an API key: set GOOGLE_API_KEY in the environment, " +
				"or web_search.api_key in the config file")
		}
		if creds.EngineID == "" {
			return fmt.Errorf("google needs a Programmable Search engine ID: set " +
				"GOOGLE_SEARCH_ENGINE_ID in the environment, or web_search.engine_id in the " +
				"config file (create one at https://programmablesearchengine.google.com/)")
		}
	case "brave", "tavily":
		if creds.APIKey == "" {
			return fmt.Errorf("%s needs an API key: set %s_API_KEY in the environment, "+
				"or web_search.api_key in the config file", name, strings.ToUpper(name))
		}
	}
	return nil
}

// CanonicalProvider resolves a user-written name to its canonical form,
// reporting whether it names a real backend. NewSearcher falls back to
// DuckDuckGo for anything unknown, which is right at runtime but wrong when
// validating input: a typo should be rejected, not silently redirected.
func CanonicalProvider(provider string) (string, bool) {
	name := normalizeProvider(provider)
	for _, p := range Providers() {
		if p == name {
			return name, true
		}
	}
	return name, false
}

// normalizeProvider lower-cases a provider name and folds the spellings users
// reasonably write ("cn.bing.com", "bing_cn", "百度") onto the canonical name.
func normalizeProvider(provider string) string {
	p := strings.ToLower(strings.TrimSpace(provider))
	switch p {
	case "cn.bing.com", "bing_cn", "bingcn", "bing.cn", "bing-china", "必应":
		return "bing-cn"
	case "www.bing.com", "bing.com":
		return "bing"
	case "baidu.com", "www.baidu.com", "百度":
		return "baidu"
	case "yahoo.com", "search.yahoo.com", "雅虎":
		return "yahoo"
	case "ddg", "duckduckgo.com":
		return "duckduckgo"
	case "google.com", "www.google.com", "谷歌":
		return "google"
	}
	return p
}

// --- Keyless HTML backends --------------------------------------------------

// htmlSearcher scrapes a search engine's result page. Every keyless backend is
// one of these, differing only in the query URL, the request headers, and the
// three patterns below — so adding an engine is data, not another Search loop.
//
// Being a scrape, it is best-effort: if the engine changes its markup or serves
// a bot-check page, results come back empty rather than wrong. Configure Brave
// or Tavily when reliability matters.
type htmlSearcher struct {
	name     string
	client   *http.Client
	endpoint func(query string, count int) string
	headers  map[string]string

	// reItem marks where each result starts, so one result's markup cannot be
	// read as another's. When nil, reLink delimits them — fine for engines that
	// emit exactly one link per result.
	reItem *regexp.Regexp
	// reLink captures a result's href (group 1) and title (group 2).
	reLink *regexp.Regexp
	// reSnippet captures the description (group 1) that follows the link.
	reSnippet *regexp.Regexp
	// decodeURL unwraps the engine's redirect wrapper into the real URL.
	decodeURL func(href string) string
	// decodeSnippet is applied before cleanup, for engines that carry the
	// description as escaped data rather than markup. Optional.
	decodeSnippet func(raw string) string
	// warmupURL is fetched to collect a session cookie when the engine answers
	// a cold search with a bot check. Optional; requires a cookie-keeping client.
	warmupURL string
}

func (s *htmlSearcher) Name() string { return s.name }

func (s *htmlSearcher) Search(ctx context.Context, query string, count int) ([]Result, error) {
	endpoint := s.endpoint(query, count)
	page, final, err := s.fetch(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	results := s.parse(page, count)

	// Some engines hand out a session cookie on their home page and answer with
	// a captcha until they see it. Pick one up and retry once — this also
	// recovers when a long-lived session's cookie expires mid-run.
	if len(results) == 0 && s.warmupURL != "" && looksBlocked(final, page) {
		if _, _, err := s.fetch(ctx, s.warmupURL); err == nil {
			if page, final, err = s.fetch(ctx, endpoint); err != nil {
				return nil, err
			}
			results = s.parse(page, count)
		}
	}
	// A bot check otherwise parses as "no results". Say so, so the user
	// reconfigures instead of concluding the web has nothing on the topic.
	if len(results) == 0 && looksBlocked(final, page) {
		return nil, fmt.Errorf("%s served a bot check instead of results; "+
			"switch web_search.provider to another engine or to brave/tavily", s.name)
	}
	return results, nil
}

// fetch GETs one page with the backend's headers and reports the URL it landed
// on, which is how a redirect to a captcha host is spotted.
func (s *htmlSearcher) fetch(ctx context.Context, endpoint string) (string, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("User-Agent", defaultUA)
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("http %d from %s", resp.StatusCode, s.name)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	return string(body), resp.Request.URL, nil
}

// looksBlocked reports whether a response is a captcha or rate-limit page
// rather than a result page.
func looksBlocked(final *url.URL, page string) bool {
	if final != nil && (strings.Contains(final.Host, "wappass.") || strings.Contains(final.Path, "/captcha")) {
		return true
	}
	head := page
	if len(head) > 4096 {
		head = head[:4096]
	}
	for _, marker := range []string{"安全验证", "/static/captcha/", "unusual traffic", "detected unusual"} {
		if strings.Contains(head, marker) {
			return true
		}
	}
	return false
}

// parse slices the page into per-result blocks and pulls the URL, title, and
// snippet out of each. Blocks matter: engines put "see more" links and inline
// code samples between results, and scanning the page as one string pairs those
// with the wrong result.
func (s *htmlSearcher) parse(page string, count int) []Result {
	marker := s.reItem
	if marker == nil {
		marker = s.reLink
	}
	starts := marker.FindAllStringIndex(page, -1)
	var out []Result
	for i, loc := range starts {
		if len(out) >= count {
			break
		}
		end := len(page)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		block := page[loc[0]:end]

		link := s.reLink.FindStringSubmatchIndex(block)
		if link == nil {
			continue
		}
		href, title := block[link[2]:link[3]], normalizeInline(block[link[4]:link[5]])
		if s.decodeURL != nil {
			href = s.decodeURL(href)
		}
		if href == "" || title == "" {
			continue
		}
		r := Result{URL: href, Title: title}
		if m := s.reSnippet.FindStringSubmatch(block[link[1]:]); m != nil {
			snippet := m[1]
			if s.decodeSnippet != nil {
				snippet = s.decodeSnippet(snippet)
			}
			r.Snippet = normalizeInline(snippet)
		}
		out = append(out, r)
	}
	return out
}

// --- DuckDuckGo (keyless, default) ------------------------------------------

// newDuckDuckGo scrapes DuckDuckGo's no-JavaScript HTML endpoint. It needs no
// API key, which makes it the zero-config default.
func newDuckDuckGo(client *http.Client) *htmlSearcher {
	return &htmlSearcher{
		name:   "duckduckgo",
		client: client,
		endpoint: func(q string, _ int) string {
			return "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(q)
		},
		reLink:    regexp.MustCompile(`(?is)<a[^>]+class="[^"]*result__a[^"]*"[^>]+href="([^"]+)"[^>]*>(.*?)</a>`),
		reSnippet: regexp.MustCompile(`(?is)<a[^>]+class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</a>`),
		decodeURL: ddgDecodeURL,
	}
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

// --- Bing (keyless) ---------------------------------------------------------

var (
	reBingLink    = regexp.MustCompile(`(?is)<li[^>]+class="[^"]*\bb_algo\b[^"]*"[^>]*>.*?<h2[^>]*>\s*<a[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	reBingSnippet = regexp.MustCompile(`(?is)<p\b[^>]*>(.*?)</p>`)
)

func newBing(client *http.Client) *htmlSearcher {
	return bingAt("bing", "www.bing.com", "en-US,en;q=0.9", client)
}

// newBingCN targets cn.bing.com — Bing's China front end, reachable from the
// mainland and ranked for Chinese queries.
func newBingCN(client *http.Client) *htmlSearcher {
	return bingAt("bing-cn", "cn.bing.com", "zh-CN,zh;q=0.9", client)
}

// bingAt builds a Bing backend for one host; both fronts share their markup.
func bingAt(name, host, lang string, client *http.Client) *htmlSearcher {
	return &htmlSearcher{
		name:   name,
		client: client,
		endpoint: func(q string, count int) string {
			return fmt.Sprintf("https://%s/search?q=%s&count=%d", host, url.QueryEscape(q), count)
		},
		headers:   map[string]string{"Accept-Language": lang},
		reLink:    reBingLink,
		reSnippet: reBingSnippet,
		decodeURL: absoluteURL("https://" + host),
	}
}

// --- Baidu (keyless) --------------------------------------------------------

// newBaidu scrapes Baidu's result page. Three quirks shape it:
//   - Baidu serves a captcha page to unrecognized clients, so this backend
//     sends browser request headers and keeps the session cookie its home page
//     issues — without either, every search returns nothing.
//   - Result blocks are "result c-container"; "result-op" cards are AI answers
//     and ads whose body is filled in by JavaScript, so they are skipped.
//   - The abstract is not markup but JSON in an s-data comment.
//
// Every URL is a /link?url=… redirect that only resolves on request, so the
// wrapper URL is what is returned — web_fetch follows it to the real page.
func newBaidu(client *http.Client) *htmlSearcher {
	return &htmlSearcher{
		name:   "baidu",
		client: withCookieJar(client),
		endpoint: func(q string, count int) string {
			return fmt.Sprintf("https://www.baidu.com/s?wd=%s&rn=%d", url.QueryEscape(q), count)
		},
		warmupURL: "https://www.baidu.com/",
		headers: map[string]string{
			"Accept-Language": "zh-CN,zh;q=0.9",
			"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
			"User-Agent":      browserUA,
		},
		reItem:        regexp.MustCompile(`(?i)<div[^>]+class="result\s+c-container`),
		reLink:        regexp.MustCompile(`(?is)<h3[^>]*>\s*<a[^>]+href="([^"]+)"[^>]*>(.*?)</a>`),
		reSnippet:     regexp.MustCompile(`(?is)"summaryData".{0,600}?"text":"((?:[^"\\]|\\.)*)"`),
		decodeURL:     absoluteURL("https://www.baidu.com"),
		decodeSnippet: unquoteJSONString,
	}
}

// unquoteJSONString turns the raw body of a JSON string literal back into text.
// It returns the input unchanged if it does not decode.
func unquoteJSONString(raw string) string {
	var s string
	if err := json.Unmarshal([]byte(`"`+raw+`"`), &s); err != nil {
		return raw
	}
	return s
}

// browserUA is sent to engines that answer the default agent User-Agent with a
// captcha instead of results.
const browserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36"

// withCookieJar returns a client that keeps cookies, copying the caller's so
// its transport and timeout are preserved. Backends that need a session cookie
// use it; the rest stay stateless.
func withCookieJar(c *http.Client) *http.Client {
	if c == nil {
		c = &http.Client{Timeout: 20 * time.Second}
	}
	if c.Jar != nil {
		return c
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return c
	}
	clone := *c
	clone.Jar = jar
	return &clone
}

// --- Yahoo (keyless) --------------------------------------------------------

// newYahoo scrapes Yahoo Search. Yahoo wraps the anchor around the title
// heading rather than inside it, and pads each result with "see more" links, so
// results are split on their algo-sr container first.
func newYahoo(client *http.Client) *htmlSearcher {
	return &htmlSearcher{
		name:   "yahoo",
		client: client,
		endpoint: func(q string, count int) string {
			return fmt.Sprintf("https://search.yahoo.com/search?p=%s&n=%d", url.QueryEscape(q), count)
		},
		reItem:    regexp.MustCompile(`(?i)<(?:div|li)[^>]+class="[^"]*\balgo-sr\b[^"]*"`),
		reLink:    regexp.MustCompile(`(?is)<a\b[^>]*\bhref="(https?://[^"]+)"[^>]*>.*?<h3[^>]*\bclass="[^"]*\btitle\b[^"]*"[^>]*>(.*?)</h3>`),
		reSnippet: regexp.MustCompile(`(?is)<p\b[^>]*>(.*?)</p>`),
		decodeURL: yahooDecodeURL,
	}
}

var reYahooRU = regexp.MustCompile(`/RU=([^/]+)/`)

// yahooDecodeURL unwraps Yahoo's redirect links (…/RU=<encoded>/RK=…).
func yahooDecodeURL(href string) string {
	m := reYahooRU.FindStringSubmatch(href)
	if m == nil {
		return absoluteURL("https://search.yahoo.com")(href)
	}
	real, err := url.QueryUnescape(m[1])
	if err != nil {
		return href
	}
	return real
}

// absoluteURL resolves protocol-relative and site-relative hrefs against the
// engine's own origin, so a scraped link is always fetchable on its own.
func absoluteURL(base string) func(string) string {
	root, _ := url.Parse(base)
	return func(href string) string {
		u, err := url.Parse(strings.TrimSpace(href))
		if err != nil {
			return ""
		}
		return root.ResolveReference(u).String()
	}
}

// --- Google (API key + engine ID) -------------------------------------------

// googleSearcher queries the Programmable Search (Custom Search JSON) API.
//
// Google is the one engine with no keyless path: its result page is rendered by
// JavaScript and carries no results in the HTML, and the old no-JS SERP now
// answers with "update your browser". So this backend is API-only, and needs
// both a key and an engine ID ("cx") naming a Programmable Search engine —
// which can be configured to search the whole web.
//
// Note the free tier is 100 queries/day; past that the API returns 429.
type googleSearcher struct {
	key      string
	engineID string
	client   *http.Client
	endpoint string // overridable for tests; empty uses the real API
}

func (g *googleSearcher) Name() string { return "google" }

func (g *googleSearcher) base() string {
	return orDefault(g.endpoint, "https://www.googleapis.com/customsearch/v1")
}

func (g *googleSearcher) Search(ctx context.Context, query string, count int) ([]Result, error) {
	if err := CheckCredentials("google", Credentials{APIKey: g.key, EngineID: g.engineID}); err != nil {
		return nil, err
	}
	// The API caps num at 10, which is also web_search's own ceiling.
	if count > 10 {
		count = 10
	}
	q := url.Values{
		"key": {g.key},
		"cx":  {g.engineID},
		"q":   {query},
		"num": {strconv.Itoa(count)},
	}
	return g.searchAt(ctx, g.base()+"?"+q.Encode())
}

func (g *googleSearcher) searchAt(ctx context.Context, endpoint string) ([]Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var payload struct {
		Items []struct {
			Title   string `json:"title"`
			Link    string `json:"link"`
			Snippet string `json:"snippet"`
		} `json:"items"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	// Decode before checking the status: Google explains the refusal (bad key,
	// quota exhausted, unknown cx) in the body, which is what the user needs.
	_ = json.Unmarshal(body, &payload)
	if resp.StatusCode != http.StatusOK {
		if payload.Error.Message != "" {
			return nil, fmt.Errorf("http %d: %s", resp.StatusCode, payload.Error.Message)
		}
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(body[:min(len(body), 300)])))
	}

	// An engine that matched nothing omits "items" entirely — not an error.
	out := make([]Result, 0, len(payload.Items))
	for _, r := range payload.Items {
		out = append(out, Result{
			Title:   normalizeInline(r.Title),
			URL:     r.Link,
			Snippet: normalizeInline(r.Snippet),
		})
	}
	return out, nil
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
