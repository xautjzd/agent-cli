package usage

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// models.dev is the primary price source: a community catalog of current model
// pricing (https://models.dev/api.json). Prices are loaded from a local cache
// instantly on startup and refreshed in the background when stale, so a session
// never blocks on the network and works offline from the last cache. When
// models.dev has no price for a model, PriceFor falls back to config-supplied
// prices and then the small built-in table.

const (
	modelsDevURL = "https://models.dev/api.json"
	modelsDevTTL = 24 * time.Hour
)

// The same model id appears in models.dev under many providers (first-party
// plus gateways/resellers) with different prices. Our usage is recorded per
// provider, so prices are keyed by provider first (byProvider) for an exact
// match; when the provider is unknown, fallback holds the *most common* price
// for that model id across providers — which is the first-party list price,
// not an inflated gateway rate.
type mdData struct {
	ByProvider map[string]map[string]Price `json:"by_provider"`
	Fallback   map[string]Price            `json:"fallback"`
}

var (
	mdMu sync.RWMutex
	md   = mdData{ByProvider: map[string]map[string]Price{}, Fallback: map[string]Price{}}
)

// InitModelsDev loads cached models.dev prices immediately and, when the cache
// is missing or older than the TTL, refreshes it in the background. cachePath
// is where the distilled price data is stored. It never blocks on the network.
func InitModelsDev(cachePath string) {
	if data, err := os.ReadFile(cachePath); err == nil {
		var d mdData
		if json.Unmarshal(data, &d) == nil && len(d.Fallback) > 0 {
			mdMu.Lock()
			md = d
			mdMu.Unlock()
		}
	}
	if cacheStale(cachePath, modelsDevTTL) {
		go refreshModelsDev(cachePath)
	}
}

// cacheStale reports whether the cache file is missing or older than ttl.
func cacheStale(path string, ttl time.Duration) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return true
	}
	return time.Since(fi.ModTime()) > ttl
}

// mdModel/mdProvider mirror the slice of models.dev's api.json we need.
type mdModel struct {
	Cost *struct {
		Input  float64 `json:"input"`
		Output float64 `json:"output"`
	} `json:"cost"`
}
type mdProvider struct {
	Models map[string]mdModel `json:"models"`
}

// refreshModelsDev fetches api.json, distills a model→price map, updates the
// in-memory prices, and rewrites the cache. Best-effort: any failure leaves the
// existing (cached) prices untouched.
func refreshModelsDev(cachePath string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsDevURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", "agent-cli")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}

	var catalog map[string]mdProvider
	if err := json.NewDecoder(resp.Body).Decode(&catalog); err != nil {
		return
	}

	byProvider := map[string]map[string]Price{}
	// counts[model][price] = how many providers charge that price → the mode
	// picks the first-party list price over scattered gateway rates.
	counts := map[string]map[Price]int{}
	for provID, prov := range catalog {
		for id, m := range prov.Models {
			if m.Cost == nil || (m.Cost.Input == 0 && m.Cost.Output == 0) {
				continue
			}
			p := Price{InputPerM: m.Cost.Input, OutputPerM: m.Cost.Output}
			if byProvider[provID] == nil {
				byProvider[provID] = map[string]Price{}
			}
			byProvider[provID][id] = p
			if counts[id] == nil {
				counts[id] = map[Price]int{}
			}
			counts[id][p]++
		}
	}
	fallback := map[string]Price{}
	for id, c := range counts {
		var best Price
		bestN := -1
		for p, n := range c {
			// Most common wins; ties break to the cheaper input.
			if n > bestN || (n == bestN && p.InputPerM < best.InputPerM) {
				best, bestN = p, n
			}
		}
		fallback[id] = best
	}
	if len(fallback) == 0 {
		return
	}

	d := mdData{ByProvider: byProvider, Fallback: fallback}
	mdMu.Lock()
	md = d
	mdMu.Unlock()

	if data, err := json.Marshal(d); err == nil {
		if os.MkdirAll(filepath.Dir(cachePath), 0o755) == nil {
			_ = os.WriteFile(cachePath, data, 0o644)
		}
	}
}

// modelsDevPrice looks up a model's price, preferring an exact provider match
// and falling back to the model's most-common (first-party) price.
func modelsDevPrice(provider, model string) (Price, bool) {
	mdMu.RLock()
	defer mdMu.RUnlock()
	if pm, ok := md.ByProvider[provider]; ok {
		if p, ok := lookup(pm, model); ok {
			return p, true
		}
	}
	return lookup(md.Fallback, model)
}
