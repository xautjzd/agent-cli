package agent

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xautjzd/agent-cli/internal/provider"
	"github.com/xautjzd/agent-cli/internal/tool"
)

// gateProvider emits a fixed set of tool calls on the first completion, then a
// final answer on the second.
type gateProvider struct {
	calls []provider.ToolCall
	turn  int
}

func (p *gateProvider) Name() string { return "gate" }
func (p *gateProvider) Chat(_ context.Context, _ provider.Request) (*provider.Response, error) {
	p.turn++
	if p.turn == 1 {
		return &provider.Response{Message: provider.Message{Role: provider.RoleAssistant, ToolCalls: p.calls}}, nil
	}
	return &provider.Response{Message: provider.Message{Role: provider.RoleAssistant, Content: "final"}}, nil
}

// barrierTool blocks until a shared counter reaches the expected concurrency,
// so the test can prove multiple tool calls execute at the same time.
type barrierTool struct {
	name    string
	running *int32
	peak    *int32
	release chan struct{}
}

func (b *barrierTool) Name() string            { return b.name }
func (b *barrierTool) Description() string     { return "barrier" }
func (b *barrierTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (b *barrierTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	n := atomic.AddInt32(b.running, 1)
	for {
		peak := atomic.LoadInt32(b.peak)
		if n <= peak || atomic.CompareAndSwapInt32(b.peak, peak, n) {
			break
		}
	}
	<-b.release
	atomic.AddInt32(b.running, -1)
	return "done:" + b.name, nil
}

func TestParallelToolExecution(t *testing.T) {
	var running, peak int32
	release := make(chan struct{})
	reg := tool.NewRegistry(
		&barrierTool{"a", &running, &peak, release},
		&barrierTool{"b", &running, &peak, release},
		&barrierTool{"c", &running, &peak, release},
	)
	calls := []provider.ToolCall{
		{ID: "1", Function: provider.FunctionCall{Name: "a", Arguments: "{}"}},
		{ID: "2", Function: provider.FunctionCall{Name: "b", Arguments: "{}"}},
		{ID: "3", Function: provider.FunctionCall{Name: "c", Arguments: "{}"}},
	}
	ag := New(&gateProvider{calls: calls}, "m", reg, "sys", nil, 5)

	// Release the barrier once all three tools are inside Execute.
	go func() {
		deadline := time.After(2 * time.Second)
		for {
			if atomic.LoadInt32(&peak) >= 3 {
				close(release)
				return
			}
			select {
			case <-deadline:
				close(release) // avoid a hang; the assertion below will fail
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()

	if _, err := ag.Run(context.Background(), "do all three"); err != nil {
		t.Fatal(err)
	}
	if peak < 3 {
		t.Errorf("tools did not run concurrently: peak parallelism = %d, want 3", peak)
	}

	// Results must be recorded in call order despite concurrent completion.
	msgs := ag.History()
	var toolMsgs []provider.Message
	for _, m := range msgs {
		if m.Role == provider.RoleTool {
			toolMsgs = append(toolMsgs, m)
		}
	}
	if len(toolMsgs) != 3 {
		t.Fatalf("expected 3 tool results, got %d", len(toolMsgs))
	}
	wantOrder := []string{"1", "2", "3"}
	for i, m := range toolMsgs {
		if m.ToolCallID != wantOrder[i] {
			t.Errorf("tool result %d has id %q, want %q (order not preserved)", i, m.ToolCallID, wantOrder[i])
		}
	}
}

// countingGate records every gate consultation to prove gating is serialized
// (called once per call) even under concurrent execution.
type countingGate struct {
	mu    sync.Mutex
	names []string
}

func (g *countingGate) BeforeToolCall(name, _ string) (bool, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.names = append(g.names, name)
	return true, ""
}

func TestParallelGatingHappensPerCall(t *testing.T) {
	release := make(chan struct{})
	close(release) // don't block; we only care about gating here
	var running, peak int32
	reg := tool.NewRegistry(
		&barrierTool{"a", &running, &peak, release},
		&barrierTool{"b", &running, &peak, release},
	)
	calls := []provider.ToolCall{
		{ID: "1", Function: provider.FunctionCall{Name: "a", Arguments: "{}"}},
		{ID: "2", Function: provider.FunctionCall{Name: "b", Arguments: "{}"}},
	}
	gate := &countingGate{}
	ag := New(&gateProvider{calls: calls}, "m", reg, "sys", nil, 5)
	ag.Gate = gate
	if _, err := ag.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if len(gate.names) != 2 {
		t.Errorf("gate should be consulted once per call, got %v", gate.names)
	}
}
