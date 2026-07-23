package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// anthropicStub captures the request body and replays a canned reply, so
// the adapter's translation can be asserted in both directions.
type anthropicStub struct {
	body    map[string]any
	status  int
	reply   string
	stream  []string
	headers http.Header
}

func (s *anthropicStub) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s.headers = r.Header.Clone()
		if err := json.Unmarshal(raw, &s.body); err != nil {
			t.Errorf("request body not JSON: %v", err)
		}
		if s.status != 0 {
			w.WriteHeader(s.status)
			fmt.Fprint(w, s.reply)
			return
		}
		if len(s.stream) > 0 {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, ev := range s.stream {
				var probe struct {
					Type string `json:"type"`
				}
				json.Unmarshal([]byte(ev), &probe)
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", probe.Type, ev)
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, s.reply)
	}))
}

// newAnthropic builds the provider against the stub server.
func newAnthropic(t *testing.T, srv *httptest.Server, thinking string) Provider {
	t.Helper()
	p, err := New("anthropic", Config{APIKey: "test-key", BaseURL: srv.URL, Thinking: thinking})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// textReply is a minimal successful Messages API response.
const textReply = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-8",
 "content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn",
 "usage":{"input_tokens":10,"output_tokens":3}}`

func TestAnthropicRegisteredAndAuthenticated(t *testing.T) {
	// An empty key is delegated to the SDK's credential chain (env var or
	// an `ant auth login` profile) rather than rejected outright.
	if _, err := New("anthropic", Config{}); err != nil {
		t.Errorf("empty key should defer to SDK credential resolution: %v", err)
	}
	stub := &anthropicStub{reply: textReply}
	srv := stub.server(t)
	defer srv.Close()

	p := newAnthropic(t, srv, "")
	if p.Name() != "anthropic" {
		t.Errorf("Name = %q", p.Name())
	}
	if _, ok := p.(Streamer); !ok {
		t.Error("anthropic provider must implement Streamer")
	}

	if _, err := p.Chat(context.Background(), Request{
		Model:    "claude-opus-4-8",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatal(err)
	}
	// Anthropic authenticates with x-api-key, not a bearer token.
	if got := stub.headers.Get("X-Api-Key"); got != "test-key" {
		t.Errorf("x-api-key = %q", got)
	}
	if stub.headers.Get("Anthropic-Version") == "" {
		t.Error("anthropic-version header missing")
	}
}

func TestAnthropicSystemPromptAndMaxTokens(t *testing.T) {
	stub := &anthropicStub{reply: textReply}
	srv := stub.server(t)
	defer srv.Close()

	_, err := newAnthropic(t, srv, "").Chat(context.Background(), Request{
		Model: "claude-opus-4-8",
		Messages: []Message{
			{Role: RoleSystem, Content: "you are a coding agent"},
			{Role: RoleUser, Content: "hi"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The system prompt becomes a top-level field, never a message.
	system, ok := stub.body["system"].([]any)
	if !ok || len(system) != 1 {
		t.Fatalf("system field wrong: %#v", stub.body["system"])
	}
	if got := system[0].(map[string]any)["text"]; got != "you are a coding agent" {
		t.Errorf("system text = %v", got)
	}
	msgs := stub.body["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["role"] != "user" {
		t.Errorf("system leaked into messages: %#v", msgs)
	}
	// max_tokens is mandatory on Anthropic; a default must be supplied.
	if mt, _ := stub.body["max_tokens"].(float64); mt <= 0 {
		t.Errorf("max_tokens = %v, want a positive default", stub.body["max_tokens"])
	}
	// Adaptive thinking is requested by default, with a visible summary.
	thinking, ok := stub.body["thinking"].(map[string]any)
	if !ok || thinking["type"] != "adaptive" || thinking["display"] != "summarized" {
		t.Errorf("thinking config = %#v", stub.body["thinking"])
	}
}

func TestAnthropicThinkingOff(t *testing.T) {
	stub := &anthropicStub{reply: textReply}
	srv := stub.server(t)
	defer srv.Close()

	newAnthropic(t, srv, "off").Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if _, present := stub.body["thinking"]; present {
		t.Errorf("thinking should be omitted when off: %#v", stub.body["thinking"])
	}
	// The model default is applied when the request names none.
	if stub.body["model"] != defaultAnthropicModel {
		t.Errorf("model = %v, want %s", stub.body["model"], defaultAnthropicModel)
	}
}

func TestAnthropicToolDefinitionShape(t *testing.T) {
	stub := &anthropicStub{reply: textReply}
	srv := stub.server(t)
	defer srv.Close()

	newAnthropic(t, srv, "off").Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Tools: []ToolDef{{
			Name:        "bash",
			Description: "run a command",
			Parameters: json.RawMessage(`{"type":"object",
				"properties":{"command":{"type":"string"}},"required":["command"]}`),
		}},
	})

	tools := stub.body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %#v", tools)
	}
	tool := tools[0].(map[string]any)
	// Anthropic tools are flat: no nested "function" wrapper, and the
	// schema lives under input_schema.
	if _, nested := tool["function"]; nested {
		t.Error("tool must not use the OpenAI function wrapper")
	}
	if tool["name"] != "bash" || tool["description"] != "run a command" {
		t.Errorf("tool fields wrong: %#v", tool)
	}
	schema, ok := tool["input_schema"].(map[string]any)
	if !ok {
		t.Fatalf("input_schema missing: %#v", tool)
	}
	if schema["type"] != "object" {
		t.Errorf("schema type = %v", schema["type"])
	}
	props, _ := schema["properties"].(map[string]any)
	if _, has := props["command"]; !has {
		t.Errorf("schema properties = %#v", schema["properties"])
	}
	req, _ := schema["required"].([]any)
	if len(req) != 1 || req[0] != "command" {
		t.Errorf("schema required = %#v", schema["required"])
	}
}

// toolUseReply exercises thinking + tool_use in one assistant turn.
const toolUseReply = `{"id":"msg_2","type":"message","role":"assistant","model":"claude-opus-4-8",
 "content":[
   {"type":"thinking","thinking":"I should list files","signature":"sig-abc"},
   {"type":"text","text":"Listing now."},
   {"type":"tool_use","id":"toolu_1","name":"bash","input":{"command":"ls"}}],
 "stop_reason":"tool_use","usage":{"input_tokens":50,"output_tokens":20}}`

func TestAnthropicResponseNormalization(t *testing.T) {
	stub := &anthropicStub{reply: toolUseReply}
	srv := stub.server(t)
	defer srv.Close()

	resp, err := newAnthropic(t, srv, "").Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "list files"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "Listing now." {
		t.Errorf("content = %q", resp.Message.Content)
	}
	if resp.Message.ReasoningContent != "I should list files" {
		t.Errorf("thinking = %q", resp.Message.ReasoningContent)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", resp.Message.ToolCalls)
	}
	call := resp.Message.ToolCalls[0]
	// Anthropic returns tool input as a JSON object; internally it must be
	// the JSON *string* the tool registry parses.
	if call.ID != "toolu_1" || call.Function.Name != "bash" {
		t.Errorf("call = %+v", call)
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		t.Fatalf("arguments not a JSON string: %q", call.Function.Arguments)
	}
	if args["command"] != "ls" {
		t.Errorf("arguments = %q", call.Function.Arguments)
	}
	if resp.FinishReason != "tool_use" {
		t.Errorf("finish = %q", resp.FinishReason)
	}
	if resp.Usage.PromptTokens != 50 || resp.Usage.CompletionTokens != 20 || resp.Usage.TotalTokens != 70 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if len(resp.Message.ProviderState) == 0 {
		t.Error("provider state must be recorded for faithful replay")
	}
}

func TestAnthropicRoundTripsThinkingAndToolResults(t *testing.T) {
	// First call produces thinking + tool_use; the recorded state must be
	// replayed verbatim on the follow-up, with all tool results collapsed
	// into a single user message.
	stub := &anthropicStub{reply: toolUseReply}
	srv := stub.server(t)
	defer srv.Close()
	p := newAnthropic(t, srv, "")

	first, err := p.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "list files"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	stub.reply = textReply
	history := []Message{
		{Role: RoleUser, Content: "list files"},
		first.Message,
		{Role: RoleTool, ToolCallID: "toolu_1", Content: "file.txt"},
		{Role: RoleTool, ToolCallID: "toolu_2", Content: "Error: boom"},
	}
	if _, err := p.Chat(context.Background(), Request{Messages: history}); err != nil {
		t.Fatal(err)
	}

	msgs := stub.body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("expected user/assistant/user, got %d messages", len(msgs))
	}

	// Assistant turn replays thinking (with signature) and tool_use.
	assistant := msgs[1].(map[string]any)
	if assistant["role"] != "assistant" {
		t.Fatalf("second message role = %v", assistant["role"])
	}
	blocks := assistant["content"].([]any)
	kinds := map[string]map[string]any{}
	for _, b := range blocks {
		m := b.(map[string]any)
		kinds[m["type"].(string)] = m
	}
	think, ok := kinds["thinking"]
	if !ok {
		t.Fatal("thinking block was not replayed — the API rejects altered thinking")
	}
	if think["signature"] != "sig-abc" || think["thinking"] != "I should list files" {
		t.Errorf("thinking block altered: %#v", think)
	}
	use, ok := kinds["tool_use"]
	if !ok {
		t.Fatal("tool_use block missing")
	}
	// Input must go back as an object, not a string.
	if _, isObject := use["input"].(map[string]any); !isObject {
		t.Errorf("tool_use input must be an object, got %#v", use["input"])
	}

	// Both tool results share ONE user message; splitting them would train
	// the model out of parallel tool calls.
	results := msgs[2].(map[string]any)
	if results["role"] != "user" {
		t.Fatalf("tool results must be a user message, got %v", results["role"])
	}
	rblocks := results["content"].([]any)
	if len(rblocks) != 2 {
		t.Fatalf("expected 2 tool_result blocks in one message, got %d", len(rblocks))
	}
	r0 := rblocks[0].(map[string]any)
	if r0["type"] != "tool_result" || r0["tool_use_id"] != "toolu_1" {
		t.Errorf("tool_result shape wrong: %#v", r0)
	}
	// A failed tool result is flagged so Claude treats it as an error.
	r1 := rblocks[1].(map[string]any)
	if r1["is_error"] != true {
		t.Errorf("error result not flagged: %#v", r1)
	}
}

func TestAnthropicImageConversion(t *testing.T) {
	stub := &anthropicStub{reply: textReply}
	srv := stub.server(t)
	defer srv.Close()

	_, err := newAnthropic(t, srv, "off").Chat(context.Background(), Request{
		Messages: []Message{{
			Role:    RoleUser,
			Content: "what is this",
			Parts: []Part{
				{Type: "text", Text: "what is this"},
				{Type: "image_url", ImageURL: &ImageURL{URL: "data:image/png;base64,aGVsbG8="}},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	blocks := stub.body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %#v", blocks)
	}
	img := blocks[1].(map[string]any)
	if img["type"] != "image" {
		t.Fatalf("image block type = %v", img["type"])
	}
	// Anthropic takes media type and payload separately — not a data URL.
	src := img["source"].(map[string]any)
	if src["type"] != "base64" || src["media_type"] != "image/png" || src["data"] != "aGVsbG8=" {
		t.Errorf("image source = %#v", src)
	}

	// A non-data URL is rejected before the request is sent.
	_, err = newAnthropic(t, srv, "off").Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Parts: []Part{
			{Type: "image_url", ImageURL: &ImageURL{URL: "https://example.com/x.png"}},
		}}},
	})
	if err == nil || !strings.Contains(err.Error(), "base64 data URL") {
		t.Errorf("expected data-URL error, got %v", err)
	}
}

func TestAnthropicStreaming(t *testing.T) {
	stub := &anthropicStub{stream: []string{
		`{"type":"message_start","message":{"id":"msg_3","type":"message","role":"assistant","model":"claude-opus-4-8","content":[],"stop_reason":null,"usage":{"input_tokens":12,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"plan"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-x"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hel"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"lo"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_9","name":"bash","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"command\":"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"ls\"}"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":25}}`,
		`{"type":"message_stop"}`,
	}}
	srv := stub.server(t)
	defer srv.Close()

	var deltas []string
	resp, err := newAnthropic(t, srv, "").(Streamer).ChatStream(context.Background(),
		Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}},
		func(d Delta) {
			if d.Reasoning != "" {
				deltas = append(deltas, "think:"+d.Reasoning)
			}
			if d.Content != "" {
				deltas = append(deltas, "text:"+d.Content)
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	if stub.body["stream"] != true {
		t.Error("stream flag not set on the request")
	}

	want := []string{"think:plan", "text:Hel", "text:lo"}
	if len(deltas) != len(want) {
		t.Fatalf("deltas = %v", deltas)
	}
	for i := range want {
		if deltas[i] != want[i] {
			t.Errorf("delta[%d] = %q, want %q", i, deltas[i], want[i])
		}
	}

	// The assembled response matches the blocking shape.
	if resp.Message.Content != "Hello" || resp.Message.ReasoningContent != "plan" {
		t.Errorf("assembled message = %+v", resp.Message)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", resp.Message.ToolCalls)
	}
	// Tool arguments streamed as input_json_delta fragments are reassembled.
	if got := resp.Message.ToolCalls[0].Function.Arguments; !strings.Contains(got, `"ls"`) {
		t.Errorf("streamed tool arguments = %q", got)
	}
	if resp.FinishReason != "tool_use" {
		t.Errorf("finish = %q", resp.FinishReason)
	}
	if resp.Usage.PromptTokens != 12 || resp.Usage.CompletionTokens != 25 {
		t.Errorf("streamed usage = %+v", resp.Usage)
	}
}

// lastBlockCacheControl reports whether the final content block of the given
// message carries an ephemeral cache_control breakpoint.
func lastBlockCacheControl(msg map[string]any) bool {
	blocks, ok := msg["content"].([]any)
	if !ok || len(blocks) == 0 {
		return false
	}
	last, ok := blocks[len(blocks)-1].(map[string]any)
	if !ok {
		return false
	}
	cc, ok := last["cache_control"].(map[string]any)
	return ok && cc["type"] == "ephemeral"
}

func TestAnthropicPromptCaching(t *testing.T) {
	stub := &anthropicStub{reply: textReply}
	srv := stub.server(t)
	defer srv.Close()

	_, err := newAnthropic(t, srv, "off").Chat(context.Background(), Request{
		Messages: []Message{
			{Role: RoleSystem, Content: "you are a coding agent"},
			{Role: RoleUser, Content: "first"},
			{Role: RoleAssistant, Content: "ok"},
			{Role: RoleUser, Content: "second"},
		},
		Tools: []ToolDef{{
			Name:        "bash",
			Description: "run a command",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The last tool definition carries a cache breakpoint (caches all tools).
	tools := stub.body["tools"].([]any)
	tool := tools[len(tools)-1].(map[string]any)
	if cc, ok := tool["cache_control"].(map[string]any); !ok || cc["type"] != "ephemeral" {
		t.Errorf("last tool missing cache_control: %#v", tool["cache_control"])
	}

	// The last system block carries a breakpoint (caches tools + system).
	system := stub.body["system"].([]any)
	sysBlock := system[len(system)-1].(map[string]any)
	if cc, ok := sysBlock["cache_control"].(map[string]any); !ok || cc["type"] != "ephemeral" {
		t.Errorf("last system block missing cache_control: %#v", sysBlock["cache_control"])
	}

	// The two most recent messages are marked so the growing conversation is
	// re-read from cache on the next turn.
	msgs := stub.body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if !lastBlockCacheControl(msgs[2].(map[string]any)) {
		t.Error("last message not marked for caching")
	}
	if !lastBlockCacheControl(msgs[1].(map[string]any)) {
		t.Error("second-to-last message not marked for caching")
	}
	// Only the two most recent are marked; older turns are covered by the
	// prefix and need no breakpoint of their own.
	if lastBlockCacheControl(msgs[0].(map[string]any)) {
		t.Error("oldest message should not carry its own breakpoint")
	}
}

func TestAnthropicPromptCachingDisabled(t *testing.T) {
	stub := &anthropicStub{reply: textReply}
	srv := stub.server(t)
	defer srv.Close()

	p, err := NewAnthropic("gw", Config{APIKey: "k", BaseURL: srv.URL, PromptCache: "off"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Chat(context.Background(), Request{
		Messages: []Message{
			{Role: RoleSystem, Content: "sys"},
			{Role: RoleUser, Content: "hi"},
		},
		Tools: []ToolDef{{Name: "bash", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tool := stub.body["tools"].([]any)[0].(map[string]any)
	if _, present := tool["cache_control"]; present {
		t.Error("cache_control must be absent when prompt caching is off")
	}
	if lastBlockCacheControl(stub.body["messages"].([]any)[0].(map[string]any)) {
		t.Error("messages must not be marked when prompt caching is off")
	}
}

func TestAnthropicCacheUsageReported(t *testing.T) {
	// Anthropic reports the uncached input separately from cache reads and
	// writes; PromptTokens must fold them back into the full input count.
	const cachedReply = `{"id":"m","type":"message","role":"assistant","model":"claude-opus-4-8",
	 "content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn",
	 "usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":100,"cache_read_input_tokens":900}}`
	stub := &anthropicStub{reply: cachedReply}
	srv := stub.server(t)
	defer srv.Close()

	resp, err := newAnthropic(t, srv, "off").Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.PromptTokens != 1010 {
		t.Errorf("PromptTokens = %d, want 1010 (10 + 100 + 900)", resp.Usage.PromptTokens)
	}
	if resp.Usage.CacheCreationTokens != 100 || resp.Usage.CacheReadTokens != 900 {
		t.Errorf("cache tokens = create %d read %d", resp.Usage.CacheCreationTokens, resp.Usage.CacheReadTokens)
	}
	if resp.Usage.TotalTokens != 1015 {
		t.Errorf("TotalTokens = %d, want 1015", resp.Usage.TotalTokens)
	}
}

func TestAnthropicCompatibleGatewayNaming(t *testing.T) {
	// A third-party Anthropic-compatible endpoint is addressed through a
	// named profile; the provider reports that name in output and errors.
	stub := &anthropicStub{reply: textReply}
	srv := stub.server(t)
	defer srv.Close()

	p, err := NewProfile("glm", FormatAnthropic, Config{
		APIKey: "glm-key", BaseURL: srv.URL, Model: "glm-4.6",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "glm" {
		t.Errorf("Name = %q, want the profile name", p.Name())
	}
	if _, err := p.Chat(context.Background(), Request{
		Model: "glm-4.6", Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatal(err)
	}
	if stub.body["model"] != "glm-4.6" {
		t.Errorf("model = %v", stub.body["model"])
	}

	// A profile still requires an endpoint, and a typo'd format is
	// reported as such rather than as a missing base_url.
	if _, err := NewProfile("glm", FormatAnthropic, Config{APIKey: "k"}); err == nil {
		t.Error("missing base_url should be rejected")
	}
	_, err = NewProfile("glm", "anthorpic", Config{APIKey: "k"})
	if err == nil || !strings.Contains(err.Error(), "unknown format") {
		t.Errorf("typo'd format should be reported: %v", err)
	}
}

func TestAnthropicAuthStyles(t *testing.T) {
	// Anthropic itself authenticates with x-api-key; most compatible
	// gateways issue tokens for Authorization: Bearer instead.
	cases := []struct {
		auth       string
		wantAPIKey string
		wantBearer string
	}{
		{"", "tok", ""},
		{AuthAPIKey, "tok", ""},
		{AuthBearer, "", "Bearer tok"},
	}
	for _, c := range cases {
		stub := &anthropicStub{reply: textReply}
		srv := stub.server(t)

		p, err := NewAnthropic("gw", Config{APIKey: "tok", BaseURL: srv.URL, Auth: c.auth})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Chat(context.Background(), Request{
			Messages: []Message{{Role: RoleUser, Content: "hi"}},
		}); err != nil {
			t.Fatal(err)
		}
		if got := stub.headers.Get("X-Api-Key"); got != c.wantAPIKey {
			t.Errorf("auth=%q: x-api-key = %q, want %q", c.auth, got, c.wantAPIKey)
		}
		if got := stub.headers.Get("Authorization"); got != c.wantBearer {
			t.Errorf("auth=%q: authorization = %q, want %q", c.auth, got, c.wantBearer)
		}
		srv.Close()
	}
}

func TestAnthropicNonStreamingGateway(t *testing.T) {
	// Some compatible gateways implement only the blocking API. A stream
	// request that yields no events must report that, not an empty answer.
	stub := &anthropicStub{reply: textReply} // plain JSON, no SSE
	srv := stub.server(t)
	defer srv.Close()

	_, err := newAnthropic(t, srv, "off").(Streamer).ChatStream(context.Background(),
		Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}}, nil)
	if err == nil {
		t.Fatal("expected an error for a non-SSE response")
	}
	if !strings.Contains(err.Error(), "no stream events") {
		t.Errorf("error should explain the streaming mismatch: %v", err)
	}
}

func TestAnthropicAPIError(t *testing.T) {
	stub := &anthropicStub{
		status: http.StatusBadRequest,
		reply:  `{"type":"error","error":{"type":"invalid_request_error","message":"bad model"}}`,
	}
	srv := stub.server(t)
	defer srv.Close()

	_, err := newAnthropic(t, srv, "off").Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "anthropic") || !strings.Contains(err.Error(), "400") {
		t.Errorf("error should name the provider and status: %v", err)
	}
}

func TestAnthropicCrossProviderHistory(t *testing.T) {
	// History inherited from an OpenAI-compatible provider has no recorded
	// block state; it must still be replayable.
	stub := &anthropicStub{reply: textReply}
	srv := stub.server(t)
	defer srv.Close()

	_, err := newAnthropic(t, srv, "off").Chat(context.Background(), Request{
		Messages: []Message{
			{Role: RoleUser, Content: "run ls"},
			{Role: RoleAssistant, Content: "sure", ToolCalls: []ToolCall{{
				ID: "call_1", Type: "function",
				Function: FunctionCall{Name: "bash", Arguments: `{"command":"ls"}`},
			}}},
			{Role: RoleTool, ToolCallID: "call_1", Content: "out"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	blocks := stub.body["messages"].([]any)[1].(map[string]any)["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("reconstructed assistant blocks = %#v", blocks)
	}
	use := blocks[1].(map[string]any)
	if use["type"] != "tool_use" || use["id"] != "call_1" {
		t.Errorf("tool_use reconstruction wrong: %#v", use)
	}
	if _, isObject := use["input"].(map[string]any); !isObject {
		t.Errorf("arguments string must become an object: %#v", use["input"])
	}
}
