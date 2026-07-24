package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// captureServer records the JSON body of the request it receives and replies
// with a minimal successful completion, so tests can assert what was sent.
func captureServer(t *testing.T, body *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
}

func TestOpenAIReasoningEffortSent(t *testing.T) {
	var body map[string]any
	srv := captureServer(t, &body)
	defer srv.Close()

	p, err := New("custom", Config{APIKey: "test-key", BaseURL: srv.URL, Thinking: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Chat(context.Background(), Request{
		Model:    "gpt-5",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatal(err)
	}
	if body["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %v, want high", body["reasoning_effort"])
	}
}

func TestOpenAIReasoningEffortOmittedByDefault(t *testing.T) {
	// off and adaptive both leave reasoning_effort off the wire, so the
	// parameter never reaches models that would reject it.
	for _, level := range []string{"", "off", "adaptive"} {
		var body map[string]any
		srv := captureServer(t, &body)

		p, err := New("custom", Config{APIKey: "test-key", BaseURL: srv.URL, Thinking: level})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Chat(context.Background(), Request{
			Model:    "gpt-4o",
			Messages: []Message{{Role: RoleUser, Content: "hi"}},
		}); err != nil {
			t.Fatal(err)
		}
		if _, present := body["reasoning_effort"]; present {
			t.Errorf("level %q: reasoning_effort should be omitted, got %v", level, body["reasoning_effort"])
		}
		srv.Close()
	}
}
