package mcp

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/xautjzd/agent-cli/internal/config"
	"github.com/xautjzd/agent-cli/internal/log"
	"github.com/xautjzd/agent-cli/internal/tool"
)

// ServerStatus reports the outcome of connecting to one configured server,
// for display by /mcp and startup diagnostics.
type ServerStatus struct {
	Name      string
	Transport string
	Tools     []string // namespaced tool names contributed
	Err       error    // non-nil when the server failed to connect
}

// OK reports whether the server connected successfully.
func (s ServerStatus) OK() bool { return s.Err == nil }

// Manager owns the live MCP client connections for a session and is
// responsible for closing them.
type Manager struct {
	clients []*Client
	Status  []ServerStatus
}

// connectTimeout bounds the initialize+tools/list handshake per server so one
// unreachable endpoint cannot stall startup indefinitely.
const connectTimeout = 20 * time.Second

// Connect dials every enabled server in the config, registers its tools into
// the registry, and returns a Manager holding the connections. Servers are
// processed in name order for deterministic tool ordering. A server that fails
// to connect is recorded in Status but does not abort the others — a broken
// MCP entry must not take the whole agent down.
func Connect(ctx context.Context, servers map[string]config.MCPServerConfig, reg *tool.Registry) *Manager {
	m := &Manager{}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		cfg := servers[name]
		if cfg.Disabled {
			continue
		}
		log.Info("mcp", "connecting to %q via %s", name, cfg.Transport())
		st := ServerStatus{Name: name, Transport: cfg.Transport()}
		client, tools, err := dial(ctx, name, cfg)
		if err != nil {
			log.Warn("mcp", "server %q failed: %v", name, err)
			st.Err = err
			m.Status = append(m.Status, st)
			continue
		}
		m.clients = append(m.clients, client)
		log.Debug("mcp", "server %q connected, %d tools", name, len(tools))
		for _, info := range tools {
			a := &toolAdapter{client: client, info: info, name: ToolName(name, info.Name)}
			// MCP tools are loaded on demand: their full JSON Schema can be large
			// and numerous, so they are advertised by name only until the model
			// pulls one in via search_tools (see tool.Registry deferral).
			reg.RegisterDeferred(a)
			st.Tools = append(st.Tools, a.name)
		}
		m.Status = append(m.Status, st)
	}
	return m
}

// dial builds the transport for one server, performs the handshake, and lists
// its tools.
func dial(ctx context.Context, name string, cfg config.MCPServerConfig) (*Client, []ToolInfo, error) {
	dctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	var tr transport
	switch cfg.Transport() {
	case "stdio":
		if cfg.Command == "" {
			return nil, nil, fmt.Errorf("stdio server %q has no command", name)
		}
		st, err := newStdioTransport(dctx, cfg.Command, cfg.Args, cfg.Env)
		if err != nil {
			return nil, nil, err
		}
		tr = st
	case "http":
		if cfg.URL == "" {
			return nil, nil, fmt.Errorf("http server %q has no url", name)
		}
		tr = newHTTPTransport(cfg.URL, cfg.Headers, nil)
	default:
		return nil, nil, fmt.Errorf("server %q: cannot determine transport (set \"type\", \"command\", or \"url\")", name)
	}

	client := &Client{name: name, transport: tr}
	if err := client.initialize(dctx); err != nil {
		_ = tr.Close()
		return nil, nil, err
	}
	tools, err := client.ListTools(dctx)
	if err != nil {
		_ = tr.Close()
		return nil, nil, err
	}
	return client, tools, nil
}

// Close shuts down every live connection.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	for _, c := range m.clients {
		_ = c.Close()
	}
	m.clients = nil
}
