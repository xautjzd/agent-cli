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

// Price is a model's cost in USD per one million tokens.
type Price struct {
	InputPerM  float64
	OutputPerM float64
}

// prices is the built-in, approximate price table (USD per 1M tokens). The
// Anthropic figures are authoritative; the others are public list prices that
// vendors change over time — treat the dollar estimates as a guide.
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
	// OpenAI (approximate).
	"gpt-4o":       {2.5, 10},
	"gpt-4o-mini":  {0.15, 0.6},
	"gpt-4.1":      {2, 8},
	"gpt-4.1-mini": {0.4, 1.6},
	// DeepSeek (approximate, cache-miss rate).
	"deepseek-chat":     {0.27, 1.10},
	"deepseek-reasoner": {0.55, 2.19},
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
