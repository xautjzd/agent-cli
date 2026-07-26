// Package usage tracks token consumption and estimated cost across sessions,
// broken down by model and provider — the data behind the /usage report
// (modeled on Claude Code's Usage panel).
//
// A Recorder accumulates one Entry per (provider, model) pair and persists the
// running totals to a JSON file, so "total consumed" survives restarts. Cost is
// estimated from a built-in per-model price table; models with no known price
// contribute tokens but no dollar figure (shown as "—").
package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Price is a model's cost in USD per one million tokens. Input and output are
// billed at different rates, so the two are tracked separately.
type Price struct {
	// InputPerM is the USD cost per 1M input (prompt) tokens.
	InputPerM float64
	// OutputPerM is the USD cost per 1M output (completion) tokens, usually
	// several times higher than the input rate.
	OutputPerM float64
}

// prices is the built-in, approximate price table (USD per 1M tokens). The
// Anthropic figures are authoritative; the others are public list prices that
// vendors change over time — treat the dollar estimates as a guide.
//
// Each entry is written as {InputPerM, OutputPerM}: the first number is the
// input/prompt rate, the second is the output/completion rate. For example,
// "claude-opus-4-8": {5, 25} means $5 per 1M input tokens and $25 per 1M
// output tokens.
var prices = map[string]Price{
	// Anthropic (Messages API).
	"claude-opus-4-8":   {5, 25},
	"claude-opus-4-7":   {5, 25},
	"claude-opus-4-6":   {5, 25},
	"claude-sonnet-5":   {3, 15},
	"claude-sonnet-4-6": {3, 15},
	"claude-haiku-4-5":  {1, 5},
	"claude-fable-5":    {10, 50},
	"claude-mythos-5":   {10, 50},
	// OpenAI (standard tier; from developers.openai.com/api/docs/pricing).
	"gpt-5.6-sol":   {5, 30},
	"gpt-5.6-terra": {2.5, 15},
	"gpt-5.6-luna":  {1, 6},
	"gpt-5.5":       {5, 30},
	"gpt-5.5-pro":   {30, 180},
	"gpt-5.4":       {2.5, 15},
	"gpt-5.4-mini":  {0.75, 4.5},
	"gpt-5.4-nano":  {0.2, 1.25},
	"gpt-5.4-pro":   {30, 180},
	"gpt-5.3-codex": {1.75, 14},
	// DeepSeek (approximate, cache-miss rate).
	"deepseek-v4-flash": {0.14, 0.28},
	"deepseek-v4-pro":   {0.435, 0.87},
	// MiniMax (platform.minimaxi.com pay-as-you-go standard tier, CNY per 1M
	// converted at 6.7819 CNY/USD; M3 above 512k input costs double).
	"MiniMax-M3":             {0.3096, 1.2386},
	"MiniMax-M2.7":           {0.3096, 1.2386},
	"MiniMax-M2.7-highspeed": {0.6193, 2.4772},
	"MiniMax-M2.5":           {0.3096, 1.2386},
	"MiniMax-M2.5-highspeed": {0.6193, 2.4772},
	"MiniMax-M2.1":           {0.3096, 1.2386},
	"MiniMax-M2.1-highspeed": {0.6193, 2.4772},
	"MiniMax-M2":             {0.3096, 1.2386},
	// Google Gemini (ai.google.dev/gemini-api/docs/pricing, paid tier, the
	// text/image/video input rate; audio input and the > 200k tier cost more
	// and are not modeled here).
	"gemini-3.6-flash":       {1.5, 7.5},
	"gemini-3.5-flash":       {1.5, 9},
	"gemini-3.5-flash-lite":  {0.3, 2.5},
	"gemini-3.1-pro-preview": {2, 12},
	"gemini-3.1-flash-lite":  {0.25, 1.5},
	"gemini-2.5-pro":         {1.25, 10},
	"gemini-2.5-flash":       {0.3, 2.5},
	"gemini-2.5-flash-lite":  {0.1, 0.4},
	// xAI (docs.x.ai/developers/pricing, the < 200k prompt-token tier; the
	// long-context tier is double and is not modeled here).
	"grok-4.5":                     {2, 6},
	"grok-4.3":                     {1.25, 2.5},
	"grok-4.20-0309-reasoning":     {1.25, 2.5},
	"grok-4.20-0309-non-reasoning": {1.25, 2.5},
	"grok-4.20-multi-agent-0309":   {1.25, 2.5},
	"grok-build-0.1":               {1, 2},
	// Zhipu GLM (from bigmodel.cn/pricing, RMB standard/base tier — input
	// length [0,32) — converted to USD at 6.7819 CNY/USD).
	"glm-5.2":        {1.1796, 4.1286},
	"glm-5.1":        {0.8847, 3.5388},
	"glm-5-turbo":    {0.7373, 3.2439},
	"glm-5":          {0.5898, 2.6541},
	"glm-4.7":        {0.2949, 1.1796},
	"glm-4.7-flashx": {0.0737, 0.4424},
	"glm-4.7-flash":  {0, 0}, // free tier
	"glm-4.5-air":    {0.1180, 0.2949},
}

// overrides holds user-configured prices (from config.json "prices"). They
// take precedence over the built-in table, so any model — including ones the
// built-in table doesn't know — can be priced. Set once at startup before any
// recording, so no locking is needed for concurrent reads.
var overrides = map[string]Price{}

// RegisterPrices merges user-supplied per-model prices (USD per 1M tokens),
// overriding the built-in table. Call once at startup.
func RegisterPrices(m map[string]Price) {
	for k, v := range m {
		overrides[k] = v
	}
}

// PriceFor returns a model's price for a given provider. Sources are tried in
// order: live models.dev prices (primary, matched by provider then by the
// model's most-common price), user config overrides (backstop), then the small
// built-in table (offline default). Within each, the exact name is matched
// before a known family prefix. ok is false when no source has a price.
func PriceFor(provider, model string) (Price, bool) {
	if p, ok := modelsDevPrice(provider, model); ok {
		return p, true
	}
	if p, ok := lookup(overrides, model); ok {
		return p, true
	}
	return lookup(prices, model)
}

func lookup(table map[string]Price, model string) (Price, bool) {
	if p, ok := table[model]; ok {
		return p, true
	}
	for name, p := range table {
		if strings.HasPrefix(model, name) {
			return p, true
		}
	}
	return Price{}, false
}

// Cost estimates the USD cost of a request for a provider/model. ok is false
// when there is no known price (callers show tokens without a dollar figure).
func Cost(provider, model string, input, output int) (float64, bool) {
	p, ok := PriceFor(provider, model)
	if !ok {
		return 0, false
	}
	return float64(input)/1e6*p.InputPerM + float64(output)/1e6*p.OutputPerM, true
}

// Entry is the running total for one (provider, model) pair.
type Entry struct {
	Provider   string        `json:"provider"`
	Model      string        `json:"model"`
	Input      int           `json:"input"`
	Output     int           `json:"output"`
	Requests   int           `json:"requests"`
	DurationNS time.Duration `json:"duration_ns"`
	Cost       float64       `json:"cost"`
	// Priced is false once any recorded request used a model with no known
	// price, so the display can flag the cost as a partial estimate.
	Priced bool `json:"priced"`
}

// Tokens returns the entry's total tokens.
func (e Entry) Tokens() int { return e.Input + e.Output }

// estimate computes the entry's cost from its tokens at the current prices, so
// configuring a price applies to already-accumulated tokens too.
func (e Entry) estimate() (float64, bool) { return Cost(e.Provider, e.Model, e.Input, e.Output) }

// Recorder accumulates usage and persists it. It is safe for concurrent use so
// parallel subagents can record against a shared recorder.
type Recorder struct {
	mu      sync.Mutex
	byKey   map[string]*Entry
	path    string
	noWrite bool // true for an in-memory recorder (tests)
}

// NewRecorder loads any existing totals from path (a missing file is fine) and
// returns a recorder that persists back to it. An empty path yields an
// in-memory recorder that never writes.
func NewRecorder(path string) *Recorder {
	r := &Recorder{byKey: map[string]*Entry{}, path: path, noWrite: path == ""}
	r.load()
	return r
}

func key(provider, model string) string { return provider + "\x00" + model }

// Record adds one completed request's usage and persists the update.
func (r *Recorder) Record(provider, model string, input, output int, dur time.Duration) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	k := key(provider, model)
	e := r.byKey[k]
	if e == nil {
		e = &Entry{Provider: provider, Model: model, Priced: true}
		r.byKey[k] = e
	}
	e.Input += input
	e.Output += output
	e.Requests++
	e.DurationNS += dur
	r.save()
}

// Totals sums every entry.
func (r *Recorder) Totals() (input, output, requests int, dur time.Duration, cost float64, fullyPriced bool) {
	if r == nil {
		return 0, 0, 0, 0, 0, true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	fullyPriced = true
	for _, e := range r.byKey {
		input += e.Input
		output += e.Output
		requests += e.Requests
		dur += e.DurationNS
		c, ok := e.estimate()
		cost += c
		if !ok {
			fullyPriced = false
		}
	}
	return
}

// ByModel returns per-model entries, highest token count first.
func (r *Recorder) ByModel() []Entry {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Entry, 0, len(r.byKey))
	for _, e := range r.byKey {
		c := *e
		c.Cost, c.Priced = e.estimate()
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tokens() > out[j].Tokens() })
	return out
}

// ByProvider aggregates entries by provider, highest token count first.
func (r *Recorder) ByProvider() []Entry {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	agg := map[string]*Entry{}
	for _, e := range r.byKey {
		a := agg[e.Provider]
		if a == nil {
			a = &Entry{Provider: e.Provider, Priced: true}
			agg[e.Provider] = a
		}
		a.Input += e.Input
		a.Output += e.Output
		a.Requests += e.Requests
		a.DurationNS += e.DurationNS
		// Cost is per-model, so sum each model's estimate into its provider.
		c, ok := e.estimate()
		a.Cost += c
		if !ok {
			a.Priced = false
		}
	}
	out := make([]Entry, 0, len(agg))
	for _, e := range agg {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tokens() > out[j].Tokens() })
	return out
}

// storeFile is the on-disk JSON shape.
type storeFile struct {
	Entries []Entry `json:"entries"`
}

// SumTokens totals input+output tokens across the given usage.json files
// (typically every project's, for the all-time stats view). Missing or corrupt
// files contribute nothing rather than failing the sum.
func SumTokens(paths []string) int {
	var total int
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var sf storeFile
		if json.Unmarshal(data, &sf) != nil {
			continue
		}
		for _, e := range sf.Entries {
			total += e.Input + e.Output
		}
	}
	return total
}

func (r *Recorder) load() {
	if r.path == "" {
		return
	}
	data, err := os.ReadFile(r.path)
	if err != nil {
		return
	}
	var sf storeFile
	if json.Unmarshal(data, &sf) != nil {
		return
	}
	for i := range sf.Entries {
		e := sf.Entries[i]
		r.byKey[key(e.Provider, e.Model)] = &e
	}
}

// save writes the totals; callers must hold the lock. Failures are ignored:
// usage tracking must never break a session.
func (r *Recorder) save() {
	if r.noWrite || r.path == "" {
		return
	}
	sf := storeFile{Entries: make([]Entry, 0, len(r.byKey))}
	for _, e := range r.byKey {
		c := *e
		c.Cost, c.Priced = e.estimate() // persist the derived cost for readability
		sf.Entries = append(sf.Entries, c)
	}
	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(r.path, data, 0o644)
}
