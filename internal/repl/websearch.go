package repl

import (
	"fmt"
	"strings"

	"github.com/xautjzd/agent-cli/internal/webtool"
)

// switchSearchProvider points the web_search tool at another engine for the
// running session. The tool holds a switchable backend shared with every
// subagent's tool set, so one swap reaches all of them without a restart.
// It does not persist; callers decide whether to save.
func (r *Repl) switchSearchProvider(name string) error {
	canonical, ok := webtool.CanonicalProvider(name)
	if !ok {
		return fmt.Errorf("unknown search engine %q (choose one of %s)",
			name, strings.Join(webtool.Providers(), ", "))
	}

	// Credentials resolve per provider, so the field has to be set before
	// asking for them — and restored if the switch turns out to be a dead end.
	previous := r.Cfg.WebSearch.Provider
	r.Cfg.WebSearch.Provider = canonical
	creds := r.Cfg.WebSearchCredentials()

	// Switching to an API backend whose credentials are not reachable would
	// persist a config whose every search fails. Refuse it while the user can
	// still act on the reason.
	if err := webtool.CheckCredentials(canonical, creds); err != nil {
		r.Cfg.WebSearch.Provider = previous
		return err
	}

	// A session built without web tools (tests, or a stripped build) has
	// nothing live to swap; the saved value still applies on the next start.
	if r.Search != nil {
		r.Search.Set(webtool.NewSearcher(canonical, creds, nil))
	}
	return nil
}
