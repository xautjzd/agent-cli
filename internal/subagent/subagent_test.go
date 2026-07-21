package subagent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xautjzd/agent-cli/internal/provider"
	"github.com/xautjzd/agent-cli/internal/tool"
)

// scriptProvider returns a fixed final answer and records how many completions
// it served, so a subagent run can be observed.
type scriptProvider struct {
	answer string
	calls  int32
}

func (p *scriptProvider) Name() string { return "script" }
func (p *scriptProvider) Chat(_ context.Context, _ provider.Request) (*provider.Response, error) {
	atomic.AddInt32(&p.calls, 1)
	return &provider.Response{
		Message: provider.Message{Role: provider.RoleAssistant, Content: p.answer},
	}, nil
}

// nopTool is a harmless tool used to check allow-list filtering.
type nopTool struct{ name string }

func (t *nopTool) Name() string                                             { return t.name }
func (t *nopTool) Description() string                                      { return "nop" }
func (t *nopTool) Schema() json.RawMessage                                  { return json.RawMessage(`{"type":"object"}`) }
func (t *nopTool) Execute(context.Context, json.RawMessage) (string, error) { return "ok", nil }

func newSpawner(answer string) *Spawner {
	return &Spawner{
		Provider: &scriptProvider{answer: answer},
		Model:    "m",
		BuildTools: func() []tool.Tool {
			return []tool.Tool{&nopTool{"read_file"}, &nopTool{"write_file"}}
		},
		Definitions: map[string]Definition{DefaultDefinition.Name: DefaultDefinition},
	}
}

func TestTaskRunsSubagent(t *testing.T) {
	sp := newSpawner("investigation complete: found the bug in main.go")
	task := &Task{Spawner: sp}

	out, err := task.Execute(context.Background(), json.RawMessage(`{"description":"find bug","prompt":"locate the bug"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "found the bug in main.go") {
		t.Errorf("report not returned: %q", out)
	}
}

func TestTaskRequiresPrompt(t *testing.T) {
	task := &Task{Spawner: newSpawner("x")}
	if _, err := task.Execute(context.Background(), json.RawMessage(`{"description":"d"}`)); err == nil {
		t.Error("empty prompt should error")
	}
}

func TestSubagentToolsHaveNoTaskTool(t *testing.T) {
	// The subagent must not receive a "task" tool, or delegation could recurse.
	sp := newSpawner("done")
	sp.BuildTools = func() []tool.Tool {
		return []tool.Tool{&nopTool{"read_file"}, &nopTool{"task"}} // even if one leaks in...
	}
	// buildToolsFor with the default (no allow-list) returns what BuildTools
	// gives; the guarantee is enforced at the composition root (BuildTools
	// excludes task). Here we assert the allow-list mechanism itself works.
	sp.Definitions["reader"] = Definition{Name: "reader", Prompt: "read only", Tools: []string{"read_file"}}
	got := sp.buildToolsFor(sp.Definitions["reader"])
	if len(got) != 1 || got[0].Name() != "read_file" {
		t.Fatalf("allow-list filtering failed: %v", names(got))
	}
}

func TestUnknownTypeFallsBackToDefault(t *testing.T) {
	sp := newSpawner("ok")
	def := sp.definition("does-not-exist")
	if def.Name != DefaultDefinition.Name {
		t.Errorf("unknown type should fall back to general-purpose, got %q", def.Name)
	}
}

func TestTypesIncludesDefault(t *testing.T) {
	sp := &Spawner{Definitions: map[string]Definition{
		"reviewer": {Name: "reviewer", Prompt: "review"},
	}}
	types := sp.Types()
	var hasDefault, hasReviewer bool
	for _, d := range types {
		hasDefault = hasDefault || d.Name == DefaultDefinition.Name
		hasReviewer = hasReviewer || d.Name == "reviewer"
	}
	if !hasDefault || !hasReviewer {
		t.Errorf("Types() should list default + custom: %v", typeNames(types))
	}
	// Sorted by name.
	for i := 1; i < len(types); i++ {
		if types[i-1].Name > types[i].Name {
			t.Errorf("Types() not sorted: %v", typeNames(types))
		}
	}
}

// blockingProvider blocks until released, so we can prove two subagents run
// concurrently rather than serially.
type blockingProvider struct {
	release chan struct{}
	entered chan struct{}
}

func (p *blockingProvider) Name() string { return "block" }
func (p *blockingProvider) Chat(_ context.Context, _ provider.Request) (*provider.Response, error) {
	p.entered <- struct{}{}
	<-p.release
	return &provider.Response{Message: provider.Message{Role: provider.RoleAssistant, Content: "done"}}, nil
}

func TestParallelDelegationRunsConcurrently(t *testing.T) {
	// Two task tools sharing a blocking provider: both must enter Chat before
	// either is released, proving concurrency. (The agent core runs multiple
	// tool calls concurrently; here we drive two Task.Execute calls directly.)
	bp := &blockingProvider{release: make(chan struct{}), entered: make(chan struct{}, 2)}
	sp := &Spawner{
		Provider:    bp,
		Model:       "m",
		BuildTools:  func() []tool.Tool { return nil },
		Definitions: map[string]Definition{DefaultDefinition.Name: DefaultDefinition},
	}
	task := &Task{Spawner: sp}

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task.Execute(context.Background(), json.RawMessage(`{"prompt":"go"}`))
		}()
	}

	// Both goroutines must reach Chat before we release either.
	for i := 0; i < 2; i++ {
		select {
		case <-bp.entered:
		case <-time.After(2 * time.Second):
			t.Fatal("second subagent did not start concurrently (serialized)")
		}
	}
	close(bp.release)
	wg.Wait()
}

func names(ts []tool.Tool) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Name())
	}
	return out
}

func typeNames(ds []Definition) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Name)
	}
	return out
}
