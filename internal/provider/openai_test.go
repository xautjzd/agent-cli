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
	// adaptive leaves reasoning_effort off the wire, so the parameter never
	// reaches models that would reject it. ("off" is not in this list: it
	// sends an explicit disable switch — see TestOpenAIEffortOffDisablesThinking.)
	for _, level := range []string{"", "adaptive"} {
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

// Effort "off" must actually reach the endpoint. Omitting the parameter means
// "vendor default", and reasoning models default to thinking ON — the reported
// bug. Each family gets the switch its API documents, verified against the
// live DeepSeek and GLM endpoints; families without one send nothing, since an
// unknown field fails the whole request (DeepSeek 400s on reasoning_effort
// "none").
func TestOpenAIEffortOffDisablesThinking(t *testing.T) {
	cases := []struct {
		model  string
		assert func(t *testing.T, body map[string]any)
	}{
		{"deepseek-v4-flash", wantThinkingDisabled},
		{"glm-5.2", wantThinkingDisabled},
		{"deepseek/deepseek-chat", wantThinkingDisabled}, // openrouter-namespaced
		{"qwen3-max", func(t *testing.T, body map[string]any) {
			if v, ok := body["enable_thinking"]; !ok || v != false {
				t.Errorf("enable_thinking = %v (present=%v), want false", v, ok)
			}
		}},
		{"gpt-5.6-terra", func(t *testing.T, body map[string]any) {
			if body["reasoning_effort"] != "none" {
				t.Errorf("reasoning_effort = %v, want none", body["reasoning_effort"])
			}
		}},
		{"kimi-k3", func(t *testing.T, body map[string]any) {
			// No documented switch: send nothing rather than risk a 400.
			for _, k := range []string{"thinking", "enable_thinking", "reasoning_effort"} {
				if _, present := body[k]; present {
					t.Errorf("unknown family should send no switch, got %s = %v", k, body[k])
				}
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			var body map[string]any
			srv := captureServer(t, &body)
			defer srv.Close()

			p, err := New("custom", Config{APIKey: "test-key", BaseURL: srv.URL, Thinking: "off"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.Chat(context.Background(), Request{
				Model:    tc.model,
				Messages: []Message{{Role: RoleUser, Content: "hi"}},
			}); err != nil {
				t.Fatal(err)
			}
			tc.assert(t, body)
		})
	}
}

func wantThinkingDisabled(t *testing.T, body map[string]any) {
	t.Helper()
	th, ok := body["thinking"].(map[string]any)
	if !ok || th["type"] != "disabled" {
		t.Errorf("thinking = %v, want {\"type\": \"disabled\"}", body["thinking"])
	}
	if _, present := body["reasoning_effort"]; present {
		t.Errorf("reasoning_effort must stay off the wire for this family: %v", body["reasoning_effort"])
	}
}
