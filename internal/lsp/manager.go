package lsp

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
)

// Manager routes files to language servers by extension and starts each server
// lazily on first use, reusing it afterwards. It is safe for concurrent use by
// parallel tool calls and subagents.
type Manager struct {
	root string
	defs []ServerDef

	mu      sync.Mutex
	clients map[string]*Client
	failed  map[string]error
}

// NewManager builds a manager rooted at the workspace directory. overrides are
// merged over the built-in server definitions by language name, so config can
// change a command, add arguments, or register a new language.
func NewManager(root string, overrides []ServerDef) *Manager {
	return &Manager{
		root:    root,
		defs:    mergeDefs(defaultServers, overrides),
		clients: map[string]*Client{},
		failed:  map[string]error{},
	}
}

// mergeDefs applies overrides onto defaults, matched by Lang: an override with
// the same Lang updates the default in place, inheriting any field it leaves
// empty (so setting just the command keeps the default's extensions); a new
// Lang is appended.
func mergeDefs(defaults, overrides []ServerDef) []ServerDef {
	out := append([]ServerDef(nil), defaults...)
	for _, o := range overrides {
		replaced := false
		for i := range out {
			if out[i].Lang == o.Lang {
				out[i] = inherit(o, out[i])
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, o)
		}
	}
	return out
}

// inherit fills empty fields of o from the base default it replaces.
func inherit(o, base ServerDef) ServerDef {
	if o.Command == "" {
		o.Command = base.Command
	}
	if o.Args == nil {
		o.Args = base.Args
	}
	if o.Env == nil {
		o.Env = base.Env
	}
	if o.Extensions == nil {
		o.Extensions = base.Extensions
	}
	return o
}

// defForPath finds the server definition handling a file's extension.
func (m *Manager) defForPath(path string) (ServerDef, bool) {
	ext := filepath.Ext(path)
	for _, d := range m.defs {
		if d.Disabled {
			continue
		}
		for _, e := range d.Extensions {
			if e == ext {
				return d, true
			}
		}
	}
	return ServerDef{}, false
}

// clientFor returns the (lazily started) server for a file, plus its
// definition. Startup failure is cached so a missing or broken server is not
// retried on every call.
func (m *Manager) clientFor(ctx context.Context, path string) (*Client, ServerDef, error) {
	def, ok := m.defForPath(path)
	if !ok {
		return nil, def, fmt.Errorf("no language server configured for %s files", filepath.Ext(path))
	}
	if !def.available() {
		return nil, def, fmt.Errorf("language server %q for %s files is not installed — install it and retry",
			def.Command, filepath.Ext(path))
	}

	m.mu.Lock()
	if c, ok := m.clients[def.Lang]; ok {
		m.mu.Unlock()
		return c, def, nil
	}
	if err, bad := m.failed[def.Lang]; bad {
		m.mu.Unlock()
		return nil, def, err
	}
	m.mu.Unlock()

	// Start outside the lock: the handshake (indexing) can be slow.
	c, err := startClient(ctx, def.Lang, m.root, def.Command, def.Args, def.Env)

	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.failed[def.Lang] = err
		return nil, def, err
	}
	if existing, ok := m.clients[def.Lang]; ok {
		// Lost a start race; keep the first winner.
		go c.Close()
		return existing, def, nil
	}
	m.clients[def.Lang] = c
	return c, def, nil
}

// Diagnostics returns the server's problems for a file.
func (m *Manager) Diagnostics(ctx context.Context, absPath, content string) ([]Diagnostic, error) {
	c, _, err := m.clientFor(ctx, absPath)
	if err != nil {
		return nil, err
	}
	return c.Diagnostics(ctx, pathToURI(absPath), languageID(filepath.Ext(absPath)), content)
}

// References returns all references to the symbol at pos.
func (m *Manager) References(ctx context.Context, absPath, content string, pos Position) ([]Location, error) {
	c, _, err := m.clientFor(ctx, absPath)
	if err != nil {
		return nil, err
	}
	return c.References(ctx, pathToURI(absPath), languageID(filepath.Ext(absPath)), content, pos)
}

// Definition returns the definition location(s) of the symbol at pos.
func (m *Manager) Definition(ctx context.Context, absPath, content string, pos Position) ([]Location, error) {
	c, _, err := m.clientFor(ctx, absPath)
	if err != nil {
		return nil, err
	}
	return c.Definition(ctx, pathToURI(absPath), languageID(filepath.Ext(absPath)), content, pos)
}

// Hover returns the hover text for the symbol at pos.
func (m *Manager) Hover(ctx context.Context, absPath, content string, pos Position) (string, error) {
	c, _, err := m.clientFor(ctx, absPath)
	if err != nil {
		return "", err
	}
	return c.Hover(ctx, pathToURI(absPath), languageID(filepath.Ext(absPath)), content, pos)
}

// ServerStatus summarizes one configured server for the /lsp listing.
type ServerStatus struct {
	Lang       string
	Command    string
	Extensions []string
	Available  bool
	Running    bool
	Disabled   bool
}

// Status reports each configured server's availability and whether it is
// running, for display.
func (m *Manager) Status() []ServerStatus {
	m.mu.Lock()
	running := map[string]bool{}
	for lang := range m.clients {
		running[lang] = true
	}
	m.mu.Unlock()

	out := make([]ServerStatus, len(m.defs))
	for i, d := range m.defs {
		out[i] = ServerStatus{
			Lang:       d.Lang,
			Command:    d.Command,
			Extensions: d.Extensions,
			Available:  d.available(),
			Running:    running[d.Lang],
			Disabled:   d.Disabled,
		}
	}
	return out
}

// Close shuts down every running server. Safe to call on a nil manager.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	clients := m.clients
	m.clients = map[string]*Client{}
	m.mu.Unlock()
	for _, c := range clients {
		c.Close()
	}
}
