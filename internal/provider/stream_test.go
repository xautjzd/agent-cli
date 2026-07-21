package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sseServer replays the given SSE data payloads for any request.
func sseServer(t *testing.T, payloads []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), "test-key") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, p := range payloads {
			fmt.Fprintf(w, "data: %s\n\n", p)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

func TestChatStreamAssemblesResponse(t *testing.T) {
	srv := sseServer(t, []string{
		`{"choices":[{"delta":{"reasoning_content":"think "}}]}`,
		`{"choices":[{"delta":{"reasoning_content":"hard"}}]}`,
		`{"choices":[{"delta":{"content":"Hel"}}]}`,
		`{"choices":[{"delta":{"content":"lo"}}]}`,
		// Tool call arguments arrive split across chunks.
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"bash","arguments":"{\"comm"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"and\":\"ls\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":50,"completion_tokens":10,"total_tokens":60}}`,
	})
	defer srv.Close()

	p, err := New("custom", Config{APIKey: "test-key", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	streamer := p.(Streamer)

	var deltas []Delta
	resp, err := streamer.ChatStream(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(d Delta) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}

	// Deltas were forwarded live, in order.
	if len(deltas) != 4 {
		t.Fatalf("deltas = %+v", deltas)
	}
	if deltas[0].Reasoning != "think " || deltas[2].Content != "Hel" {
		t.Errorf("delta order wrong: %+v", deltas)
	}

	// The assembled response matches a blocking Chat result.
	if resp.Message.Content != "Hello" || resp.Message.ReasoningContent != "think hard" {
		t.Errorf("assembled message wrong: %+v", resp.Message)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", resp.Message.ToolCalls)
	}
	tc := resp.Message.ToolCalls[0]
	if tc.ID != "c1" || tc.Function.Name != "bash" || tc.Function.Arguments != `{"command":"ls"}` {
		t.Errorf("tool call not reassembled: %+v", tc)
	}
	if resp.FinishReason != "tool_calls" || resp.Usage.TotalTokens != 60 {
		t.Errorf("finish/usage wrong: %s %+v", resp.FinishReason, resp.Usage)
	}
}

func TestChatStreamAPIError(t *testing.T) {
	srv := sseServer(t, []string{`{"error":{"message":"model overloaded"}}`})
	defer srv.Close()
	p, _ := New("custom", Config{APIKey: "test-key", BaseURL: srv.URL})
	_, err := p.(Streamer).ChatStream(context.Background(), Request{Model: "m"}, nil)
	if err == nil || !strings.Contains(err.Error(), "model overloaded") {
		t.Errorf("expected API error, got %v", err)
	}
}

func TestChatStreamHTTPError(t *testing.T) {
	srv := sseServer(t, nil)
	defer srv.Close()
	p, _ := New("custom", Config{APIKey: "wrong", BaseURL: srv.URL})
	_, err := p.(Streamer).ChatStream(context.Background(), Request{Model: "m"}, nil)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("expected HTTP 401 error, got %v", err)
	}
}
