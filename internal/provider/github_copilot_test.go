package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
)

func TestCopilotPromptSeparatesCurrentTurnAndContext(t *testing.T) {
	prompt, system, err := copilotPrompt([]Message{
		{Role: RoleSystem, Content: "be careful"},
		{Role: RoleUser, Content: "first"},
		{Role: RoleAssistant, Content: "answer"},
		{Role: RoleUser, Content: "second"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "Current user message:\nsecond") || !strings.Contains(prompt, "user: first") || !strings.Contains(system, "be careful") || strings.Contains(system, "user: first") {
		t.Fatalf("prompt=%q system=%q", prompt, system)
	}
}

func TestCopilotToolsUseHostExecutor(t *testing.T) {
	var call ToolCall
	tools, names, err := copilotTools(context.Background(), Request{
		Tools: []ToolDef{{Name: "read_file", Parameters: json.RawMessage(`{"type":"object"}`)}},
		ExecuteTool: func(_ context.Context, got ToolCall) (string, bool) {
			call = got
			return "safe result", true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := tools[0].Handler(copilot.ToolInvocation{ToolCallID: "call-1", Arguments: map[string]any{"path": "README.md"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "read_file" || !tools[0].SkipPermission || call.ID != "call-1" || result.TextResultForLLM != "safe result" {
		t.Fatalf("names=%v call=%+v result=%+v", names, call, result)
	}
}

func TestCopilotPromptRejectsImages(t *testing.T) {
	_, _, err := copilotPrompt([]Message{{Role: RoleUser, Parts: []Part{{Type: "image_url"}}}})
	if err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("err=%v", err)
	}
}

func TestCopilotToolsRequireHostExecutor(t *testing.T) {
	_, _, err := copilotTools(context.Background(), Request{Tools: []ToolDef{{Name: "bash", Parameters: json.RawMessage(`{"type":"object"}`)}}})
	if err == nil || !strings.Contains(err.Error(), "host tool executor") {
		t.Fatalf("err=%v", err)
	}
}

func TestCopilotRuntimeStartErrorExplainsLegacyCLI(t *testing.T) {
	err := copilotRuntimeStartError(errors.New("CLI process exited: exit status 1\nstderr: error: unknown option '--headless'"))
	if !strings.Contains(err.Error(), "Copilot CLI is too old") || !strings.Contains(err.Error(), "copilot update") {
		t.Fatalf("err=%v", err)
	}
}

func TestCopilotRuntimeStartErrorPreservesOtherFailures(t *testing.T) {
	err := copilotRuntimeStartError(errors.New("permission denied"))
	if !strings.Contains(err.Error(), "start GitHub Copilot runtime: permission denied") || strings.Contains(err.Error(), "copilot update") {
		t.Fatalf("err=%v", err)
	}
}
