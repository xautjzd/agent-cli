package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type staticAuthSource struct {
	auth RequestAuth
	err  error
}

func (s staticAuthSource) Auth(context.Context) (RequestAuth, error) { return s.auth, s.err }

func TestOpenAICodexStreamsTextReasoningToolsAndUsage(t *testing.T) {
	var request codexRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/codex/responses" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer access-secret" || r.Header.Get("ChatGPT-Account-Id") != "acct-1" {
			t.Fatalf("bad auth headers")
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		events := []any{
			map[string]any{"type": "response.reasoning_summary_text.delta", "delta": "checking"},
			map[string]any{"type": "response.output_text.delta", "delta": "hello"},
			map[string]any{"type": "response.output_item.added", "output_index": 2, "item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "read_file", "arguments": ""}},
			map[string]any{"type": "response.function_call_arguments.delta", "output_index": 2, "delta": "{\"path\":"},
			map[string]any{"type": "response.function_call_arguments.done", "output_index": 2, "arguments": "{\"path\":\"README.md\"}"},
			map[string]any{"type": "response.output_item.done", "output_index": 1, "item": map[string]any{"id": "rs_1", "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "checking"}}, "encrypted_content": "opaque"}},
			map[string]any{"type": "response.output_item.done", "output_index": 2, "item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "read_file", "arguments": "{\"path\":\"README.md\"}"}},
			map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "usage": map[string]any{"input_tokens": 100, "output_tokens": 20, "total_tokens": 120, "input_tokens_details": map[string]any{"cached_tokens": 40}}}},
		}
		for _, event := range events {
			data, _ := json.Marshal(event)
			_, _ = w.Write([]byte("data: " + string(data) + "\n\n"))
		}
	}))
	defer server.Close()

	p, err := NewOpenAICodex(staticAuthSource{auth: RequestAuth{Token: "access-secret", AccountID: "acct-1"}}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	p.(*openAICodex).baseURL = server.URL
	p.(*openAICodex).client = server.Client()
	streamer := p.(Streamer)
	var deltas []Delta
	resp, err := streamer.ChatStream(context.Background(), Request{
		Model: "gpt-test",
		Messages: []Message{
			{Role: RoleSystem, Content: "system"},
			{Role: RoleUser, Content: "question"},
		},
		Tools: []ToolDef{{Name: "read_file", Description: "read", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}, func(delta Delta) { deltas = append(deltas, delta) })
	if err != nil {
		t.Fatal(err)
	}
	if request.Instructions != "system" || len(request.Input) != 1 || len(request.Tools) != 1 {
		t.Fatalf("wire request = %+v", request)
	}
	if resp.Message.Content != "hello" || resp.Message.ReasoningContent != "checking" || len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("response = %+v", resp)
	}
	if resp.Message.ToolCalls[0].Function.Arguments != `{"path":"README.md"}` || resp.FinishReason != "tool_calls" {
		t.Fatalf("tool response = %+v", resp)
	}
	if resp.Usage.PromptTokens != 100 || resp.Usage.CacheReadTokens != 40 || len(resp.Message.ProviderState) == 0 || len(deltas) != 2 {
		t.Fatalf("usage/state/deltas = %+v / %s / %+v", resp.Usage, resp.Message.ProviderState, deltas)
	}
}

func TestBuildCodexRequestReplaysToolResultsAndOpaqueReasoning(t *testing.T) {
	reasoning := json.RawMessage(`{"id":"rs_1","type":"reasoning","encrypted_content":"opaque"}`)
	state, _ := json.Marshal(codexState{Reasoning: []json.RawMessage{reasoning}})
	wire := buildCodexRequest(Request{Model: "gpt-test", Messages: []Message{
		{Role: RoleAssistant, Content: "calling", ProviderState: state, ToolCalls: []ToolCall{{ID: "call_1", Type: "function", Function: FunctionCall{Name: "read", Arguments: `{}`}}}},
		{Role: RoleTool, ToolCallID: "call_1", Content: "result"},
	}}, EffortAdaptive)
	data, _ := json.Marshal(wire.Input)
	text := string(data)
	for _, want := range []string{`"encrypted_content":"opaque"`, `"type":"function_call"`, `"type":"function_call_output"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("input missing %s: %s", want, text)
		}
	}
}

func TestOpenAICodexAuthAndHTTPFailuresAreSafe(t *testing.T) {
	p, err := NewOpenAICodex(staticAuthSource{err: errors.New("refresh failed")}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Chat(context.Background(), Request{}); err == nil || !strings.Contains(err.Error(), "refresh failed") {
		t.Fatalf("auth error = %v", err)
	}

	secret := "must-not-leak"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"` + secret + `","code":"unauthorized"}}`))
	}))
	defer server.Close()
	p, err = NewOpenAICodex(staticAuthSource{auth: RequestAuth{Token: secret, AccountID: "acct"}}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	p.(*openAICodex).baseURL = server.URL
	p.(*openAICodex).client = server.Client()
	_, err = p.Chat(context.Background(), Request{})
	if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("unsafe HTTP error = %v", err)
	}
}

func TestParseCodexStreamRequiresTerminalEvent(t *testing.T) {
	_, err := parseCodexStream(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"), func(Delta) {})
	if err == nil || !strings.Contains(err.Error(), "before completion") {
		t.Fatalf("stream error = %v", err)
	}
}

func TestOpenAICodexRejectsCustomBaseURLAndAllowsNilStreamCallback(t *testing.T) {
	auth := staticAuthSource{auth: RequestAuth{Token: "token", AccountID: "account"}}
	if _, err := NewOpenAICodex(auth, Config{BaseURL: "https://gateway.example/v1"}); err == nil {
		t.Fatal("subscription transport accepted a custom base URL")
	}

	stream := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"ok"}`,
		`data: {"type":"response.completed","response":{"status":"completed"}}`,
	}, "\n\n")
	response, err := parseCodexStream(strings.NewReader(stream), nil)
	if err != nil || response.Message.Content != "ok" {
		t.Fatalf("parseCodexStream = %#v, %v", response, err)
	}
}
