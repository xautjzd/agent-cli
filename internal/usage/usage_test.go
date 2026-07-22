package usage

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCostEstimate(t *testing.T) {
	// claude-opus-4-8: $5/1M in, $25/1M out.
	c, ok := Cost("anthropic", "claude-opus-4-8", 1_000_000, 1_000_000)
	if !ok || c != 30 {
		t.Errorf("opus cost = %v ok=%v, want 30", c, ok)
	}
	// Unknown model → no price.
	if _, ok := Cost("nope", "some-unlisted-model", 1000, 1000); ok {
		t.Error("unlisted model should have no price")
	}
	// Prefix match resolves a date-suffixed id.
	if _, ok := Cost("anthropic", "claude-haiku-4-5-20251001", 10, 10); !ok {
		t.Error("prefix price match failed")
	}
}

func TestRecorderAggregates(t *testing.T) {
	r := NewRecorder("") // in-memory
	r.Record("anthropic", "claude-opus-4-8", 1000, 2000, time.Second)
	r.Record("anthropic", "claude-opus-4-8", 500, 1000, time.Second)
	r.Record("anthropic", "claude-haiku-4-5", 100, 200, time.Second)
	r.Record("deepseek", "deepseek-chat", 300, 400, time.Second)

	in, out, reqs, dur, cost, priced := r.Totals()
	if in != 1900 || out != 3600 {
		t.Errorf("totals tokens = %d/%d, want 1900/3600", in, out)
	}
	if reqs != 4 || dur != 4*time.Second {
		t.Errorf("totals reqs/dur = %d/%v", reqs, dur)
	}
	if !priced || cost <= 0 {
		t.Errorf("cost should be fully priced and positive: %v priced=%v", cost, priced)
	}

	// Per-model, sorted by token count (opus first).
	byModel := r.ByModel()
	if len(byModel) != 3 || byModel[0].Model != "claude-opus-4-8" {
		t.Fatalf("by-model wrong: %+v", byModel)
	}
	if byModel[0].Input != 1500 || byModel[0].Output != 3000 || byModel[0].Requests != 2 {
		t.Errorf("opus entry aggregated wrong: %+v", byModel[0])
	}

	// Per-provider aggregation.
	byProv := r.ByProvider()
	if len(byProv) != 2 || byProv[0].Provider != "anthropic" {
		t.Fatalf("by-provider wrong: %+v", byProv)
	}
	if byProv[0].Input != 1600 { // 1000+500+100
		t.Errorf("anthropic provider input = %d, want 1600", byProv[0].Input)
	}
}

func TestRecorderUnpricedModelFlagged(t *testing.T) {
	r := NewRecorder("")
	r.Record("glm", "glm-4.6", 1000, 1000, time.Second) // no built-in price
	_, _, _, _, _, priced := r.Totals()
	if priced {
		t.Error("an unpriced model should mark totals as not fully priced")
	}
	e := r.ByModel()[0]
	if e.Priced || e.Cost != 0 {
		t.Errorf("unpriced entry should have Priced=false cost=0: %+v", e)
	}
}

func TestRecorderPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	r := NewRecorder(path)
	r.Record("anthropic", "claude-opus-4-8", 1000, 2000, time.Second)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("usage file not written: %v", err)
	}
	// A fresh recorder loads the persisted totals.
	r2 := NewRecorder(path)
	in, out, _, _, _, _ := r2.Totals()
	if in != 1000 || out != 2000 {
		t.Errorf("reloaded totals = %d/%d, want 1000/2000", in, out)
	}
	// Recording more accumulates on top of the loaded total.
	r2.Record("anthropic", "claude-opus-4-8", 1000, 0, time.Second)
	in, _, _, _, _, _ = r2.Totals()
	if in != 2000 {
		t.Errorf("accumulated input = %d, want 2000", in)
	}
}

func TestRecorderConcurrent(t *testing.T) {
	r := NewRecorder("")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Record("anthropic", "claude-opus-4-8", 100, 100, time.Millisecond)
		}()
	}
	wg.Wait()
	in, out, reqs, _, _, _ := r.Totals()
	if in != 2000 || out != 2000 || reqs != 20 {
		t.Errorf("concurrent totals = in %d out %d reqs %d, want 2000/2000/20", in, out, reqs)
	}
}

func TestRegisteredPriceAppliesRetroactively(t *testing.T) {
	// Record tokens for a model with no built-in price — cost is unknown.
	r := NewRecorder("")
	r.Record("custom", "my-model-v9", 1_000_000, 1_000_000, 0)
	if e := r.ByModel()[0]; e.Priced || e.Cost != 0 {
		t.Fatalf("unpriced before registration: %+v", e)
	}

	// Registering a price makes the ALREADY-recorded tokens show cost, since
	// cost is derived from tokens on read (not frozen at record time).
	RegisterPrices(map[string]Price{"my-model-v9": {InputPerM: 2, OutputPerM: 4}})
	defer delete(overrides, "my-model-v9")

	e := r.ByModel()[0]
	if !e.Priced || e.Cost != 6 { // 1M*2/1M + 1M*4/1M = 6
		t.Errorf("registered price not applied retroactively: %+v", e)
	}
	// Overrides beat the built-in table.
	RegisterPrices(map[string]Price{"claude-opus-4-8": {InputPerM: 1, OutputPerM: 1}})
	defer delete(overrides, "claude-opus-4-8")
	if p, _ := PriceFor("anthropic", "claude-opus-4-8"); p.InputPerM != 1 {
		t.Errorf("override did not beat built-in: %+v", p)
	}
}

func TestModelsDevProviderKeyedAndFallback(t *testing.T) {
	// Seed models.dev data: the same model priced differently per provider,
	// plus a mode-based fallback for when the provider is unknown.
	mdMu.Lock()
	md = mdData{
		ByProvider: map[string]map[string]Price{
			"deepseek": {"deepseek-v4-pro": {InputPerM: 0.435, OutputPerM: 0.87}},
			"gateway":  {"deepseek-v4-pro": {InputPerM: 1.74, OutputPerM: 3.48}},
		},
		Fallback: map[string]Price{"deepseek-v4-pro": {InputPerM: 0.435, OutputPerM: 0.87}},
	}
	mdMu.Unlock()
	defer func() {
		mdMu.Lock()
		md = mdData{ByProvider: map[string]map[string]Price{}, Fallback: map[string]Price{}}
		mdMu.Unlock()
	}()

	// Exact provider match uses that provider's price (not a gateway's).
	if p, ok := PriceFor("deepseek", "deepseek-v4-pro"); !ok || p.InputPerM != 0.435 {
		t.Errorf("deepseek price = %+v ok=%v, want 0.435", p, ok)
	}
	if p, _ := PriceFor("gateway", "deepseek-v4-pro"); p.InputPerM != 1.74 {
		t.Errorf("gateway price = %+v, want 1.74", p)
	}
	// Unknown provider → most-common (fallback) price, not a random gateway.
	if p, ok := PriceFor("some-unknown-provider", "deepseek-v4-pro"); !ok || p.InputPerM != 0.435 {
		t.Errorf("fallback price = %+v ok=%v, want 0.435", p, ok)
	}
}

func TestNilRecorderSafe(t *testing.T) {
	var r *Recorder
	r.Record("x", "y", 1, 1, 0) // must not panic
	if _, _, _, _, _, p := r.Totals(); !p {
		t.Error("nil recorder Totals should report fully-priced empty")
	}
	if r.ByModel() != nil || r.ByProvider() != nil {
		t.Error("nil recorder lists should be nil")
	}
}
