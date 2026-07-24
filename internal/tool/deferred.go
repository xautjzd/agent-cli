package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Activation is the per-session set of deferred tools the model has pulled
// into context via search_tools. It is shared between the meta-tool (which
// grows it) and the agent (which reads it to decide the advertised tool set).
// It is safe for concurrent use because tool calls in one turn may run in
// parallel.
type Activation struct {
	mu     sync.Mutex
	active map[string]bool
}

// Activate marks tools as loaded for the session.
func (a *Activation) Activate(names ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active == nil {
		a.active = map[string]bool{}
	}
	for _, n := range names {
		a.active[n] = true
	}
}

// IsActive reports whether a deferred tool has been loaded.
func (a *Activation) IsActive(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.active[name]
}

// SearchTools is the meta-tool that reveals deferred tools' full schemas on
// demand and activates them so the model may call them on the next turn. It
// mirrors Claude Code's deferred-tool loading and this environment's own
// ToolSearch: the system prompt lists deferred tools by name+description only,
// and the model fetches a schema here right before it needs to call the tool.
type SearchTools struct {
	Registry   *Registry
	Activation *Activation
}

func (t *SearchTools) Name() string { return "search_tools" }

func (t *SearchTools) Description() string {
	return "Load the full input schema of tools that are listed by name only " +
		"(e.g. MCP tools) so you can call them. Pass exact names, and/or a query " +
		"to search names and descriptions. The returned tools become callable on " +
		"your next turn. Call this before invoking any tool not already in your tool list."
}

func (t *SearchTools) Schema() json.RawMessage {
	return mustSchema(`{
		"type": "object",
		"properties": {
			"names": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Exact tool names to load, as listed in the deferred-tools catalog"
			},
			"query": {
				"type": "string",
				"description": "Keywords to match against deferred tool names and descriptions"
			}
		}
	}`)
}

// maxSearchResults bounds how many tools one query can reveal, so a broad query
// cannot dump every schema back into context and defeat the purpose.
const maxSearchResults = 10

func (t *SearchTools) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Names []string `json:"names"`
		Query string   `json:"query"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if len(args.Names) == 0 && strings.TrimSpace(args.Query) == "" {
		return "", fmt.Errorf("provide names, query, or both")
	}

	deferred := t.Registry.Deferred()
	byName := make(map[string]Tool, len(deferred))
	for _, d := range deferred {
		byName[d.Name()] = d
	}

	seen := map[string]bool{}
	var matched []Tool

	// Exact names first, so an explicit request is never crowded out.
	for _, n := range args.Names {
		if tool, ok := byName[n]; ok && !seen[n] {
			seen[n] = true
			matched = append(matched, tool)
		}
	}
	// Then keyword matches over the remaining deferred tools.
	if q := strings.ToLower(strings.TrimSpace(args.Query)); q != "" {
		for _, d := range deferred {
			if seen[d.Name()] || len(matched) >= maxSearchResults {
				continue
			}
			if strings.Contains(strings.ToLower(d.Name()), q) ||
				strings.Contains(strings.ToLower(d.Description()), q) {
				seen[d.Name()] = true
				matched = append(matched, d)
			}
		}
	}

	if len(matched) == 0 {
		return t.noMatch(args, deferred), nil
	}
	if len(matched) > maxSearchResults {
		matched = matched[:maxSearchResults]
	}

	names := make([]string, 0, len(matched))
	for _, m := range matched {
		names = append(names, m.Name())
	}
	if t.Activation != nil {
		t.Activation.Activate(names...)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Loaded %d tool(s); they are now available to call:\n\n", len(matched))
	for _, m := range matched {
		fmt.Fprintf(&sb, "### %s\n%s\n\nInput schema:\n%s\n\n",
			m.Name(), m.Description(), indentJSON(m.Schema()))
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// noMatch returns a helpful message listing available deferred tool names so
// the model can correct a typo rather than give up.
func (t *SearchTools) noMatch(args struct {
	Names []string `json:"names"`
	Query string   `json:"query"`
}, deferred []Tool) string {
	names := make([]string, 0, len(deferred))
	for _, d := range deferred {
		names = append(names, d.Name())
	}
	sort.Strings(names)
	return fmt.Sprintf("No deferred tools matched (names=%v query=%q). Available deferred tools: %s",
		args.Names, args.Query, strings.Join(names, ", "))
}

// indentJSON pretty-prints a schema for readability, falling back to the raw
// bytes if it is not valid JSON (it always is for built-in tools).
func indentJSON(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(out)
}
