package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/xautjzd/agent-cli/internal/provider"
	"github.com/xautjzd/agent-cli/internal/tool"
)

// stubHooks is a scripted ToolHooks implementation.
type stubHooks struct {
	pre             HookOutcome
	post            HookOutcome
	sawPre, sawPost bool
}

func (h *stubHooks) PreToolUse(_ context.Context, _, _ string) HookOutcome {
	h.sawPre = true
	return h.pre
}
func (h *stubHooks) PostToolUse(_ context.Context, _, _, _ string, _ bool) HookOutcome {
	h.sawPost = true
	return h.post
}

func toolCallResponse(name string) provider.Response {
	return provider.Response{Message: provider.Message{
		Role: provider.RoleAssistant,
		ToolCalls: []provider.ToolCall{{
			ID: "c1", Type: "function",
			Function: provider.FunctionCall{Name: name, Arguments: `{}`},
		}},
	}}
}

func TestPreToolUseHookBlocks(t *testing.T) {
	echo := &echoTool{}
	fake := &fakeProvider{responses: []provider.Response{
		toolCallResponse("echo"),
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "done"}},
	}}
	ag := New(fake, "m", tool.NewRegistry(echo), "sys", nil, 5)
	ag.Hooks = &stubHooks{pre: HookOutcome{Block: true, Reason: "policy denies echo"}}

	if _, err := ag.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	// The tool must NOT have executed…
	if len(echo.calls) != 0 {
		t.Errorf("PreToolUse block should prevent execution, calls=%v", echo.calls)
	}
	// …and the block reason must reach the model as the tool result.
	msgs := fake.requests[len(fake.requests)-1].Messages
	last := msgs[len(msgs)-1]
	if last.Role != provider.RoleTool || !strings.Contains(last.Content, "policy denies echo") {
		t.Errorf("block reason not fed back: %+v", last)
	}
}

func TestPostToolUseHookAppendsContext(t *testing.T) {
	echo := &echoTool{}
	fake := &fakeProvider{responses: []provider.Response{
		toolCallResponse("echo"),
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "done"}},
	}}
	ag := New(fake, "m", tool.NewRegistry(echo), "sys", nil, 5)
	h := &stubHooks{post: HookOutcome{Context: "lint: no issues"}}
	ag.Hooks = h

	if _, err := ag.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if !h.sawPre || !h.sawPost {
		t.Error("both hooks should have fired")
	}
	// The tool ran and its result carries the appended context.
	msgs := fake.requests[len(fake.requests)-1].Messages
	last := msgs[len(msgs)-1]
	if !strings.Contains(last.Content, "echoed:") || !strings.Contains(last.Content, "lint: no issues") {
		t.Errorf("PostToolUse context not appended: %q", last.Content)
	}
}

func TestNoHooksLeavesResultUnchanged(t *testing.T) {
	echo := &echoTool{}
	fake := &fakeProvider{responses: []provider.Response{
		toolCallResponse("echo"),
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "done"}},
	}}
	ag := New(fake, "m", tool.NewRegistry(echo), "sys", nil, 5)
	// Hooks nil — must behave exactly as before.
	if _, err := ag.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if len(echo.calls) != 1 {
		t.Errorf("tool should have run once, calls=%v", echo.calls)
	}
}
