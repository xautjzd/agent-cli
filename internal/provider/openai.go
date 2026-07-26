package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// openAICompatible implements Provider against the OpenAI Chat Completions
// wire format. OpenAI, DeepSeek, Moonshot, Qwen and many other vendors expose
// this exact protocol, so a single implementation parameterized by BaseURL
// covers all of them.
type openAICompatible struct {
	name    string
	apiKey  string
	baseURL string
	client  *http.Client
	// effort is the reasoning level. It is resolved per request rather than
	// per client because what a level means on the wire depends on the model
	// (see applyThinking).
	effort Effort
}

// Vendor defaults. Users can always override BaseURL to point at any
// OpenAI-compatible endpoint (e.g. a local vLLM or Ollama server).
const (
	openAIBaseURL   = "https://api.openai.com/v1"
	deepSeekBaseURL = "https://api.deepseek.com/v1"
)

func init() {
	Register("openai", func(cfg Config) (Provider, error) {
		return newOpenAICompatible("openai", openAIBaseURL, cfg)
	})
	Register("deepseek", func(cfg Config) (Provider, error) {
		return newOpenAICompatible("deepseek", deepSeekBaseURL, cfg)
	})
	// "custom" targets any OpenAI-compatible server; BaseURL is mandatory.
	Register("custom", func(cfg Config) (Provider, error) {
		if cfg.BaseURL == "" {
			return nil, fmt.Errorf("provider custom requires base_url")
		}
		return newOpenAICompatible("custom", "", cfg)
	})
}

// NewNamed builds an OpenAI-compatible provider under an arbitrary display
// name, used for named provider profiles from the config file (e.g.
// "ollama", "moonshot"). BaseURL is mandatory since no vendor default
// exists for an arbitrary name.
func NewNamed(name string, cfg Config) (Provider, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("provider profile %q requires base_url", name)
	}
	return newOpenAICompatible(name, "", cfg)
}

func newOpenAICompatible(name, defaultBase string, cfg Config) (Provider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("provider %s: api key is required", name)
	}
	base := cfg.BaseURL
	if base == "" {
		base = defaultBase
	}
	effort, _ := ParseEffort(cfg.Thinking)
	return &openAICompatible{
		name:    name,
		apiKey:  cfg.APIKey,
		baseURL: base,
		effort:  effort,
		// Agent turns can be slow on long completions; generous timeout.
		client: &http.Client{Timeout: 5 * time.Minute},
	}, nil
}

func (p *openAICompatible) Name() string { return p.name }

// Wire-format request/response structs, kept private to this file so the
// rest of the codebase never sees vendor JSON shapes (ISP: callers only see
// the small Provider interface).
type chatRequest struct {
	Model           string        `json:"model"`
	Messages        []wireMessage `json:"messages"`
	Tools           []wireTool    `json:"tools,omitempty"`
	MaxTokens       int           `json:"max_tokens,omitempty"`
	ReasoningEffort string        `json:"reasoning_effort,omitempty"`
	// Thinking and EnableThinking are the two shapes vendors use to turn
	// extended thinking off; at most one is ever set (see applyThinking).
	Thinking       *thinkingParam `json:"thinking,omitempty"`
	EnableThinking *bool          `json:"enable_thinking,omitempty"`
	Stream         bool           `json:"stream,omitempty"`
	StreamOptions  *streamOptions `json:"stream_options,omitempty"`
}

// streamOptions requests a final usage chunk on streamed completions
// (supported by OpenAI and DeepSeek alike).
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// wireMessage is the on-the-wire message shape: content is either a plain
// string or a multimodal parts array, decided by Message.WireContent.
type wireMessage struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

func toWireMessages(msgs []Message) []wireMessage {
	out := make([]wireMessage, len(msgs))
	for i, m := range msgs {
		out[i] = wireMessage{
			Role:       m.Role,
			Content:    m.WireContent(),
			ToolCalls:  m.ToolCalls,
			ToolCallID: m.ToolCallID,
		}
	}
	return out
}

type wireTool struct {
	Type     string  `json:"type"`
	Function ToolDef `json:"function"`
}

type chatResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// thinkingParam is the {"type": ...} switch DeepSeek, GLM and Kimi accept.
type thinkingParam struct {
	Type string `json:"type"`
}

// applyThinking encodes the effort level for one request. The model decides
// what is even expressible: whether thinking can be switched off, how that is
// spelled, and which strength levels the endpoint accepts (SupportedThinking).
//
// Two rules earn their keep here. "Off" must be stated outright — omitting the
// switch means "vendor default", and reasoning models default to thinking ON,
// which is why turning effort off used to do nothing. And a strength the model
// does not accept is clamped rather than sent: DeepSeek answers an unknown
// reasoning_effort with 400, so a level left over from another model must not
// reach the wire.
func applyThinking(wire *chatRequest, effort Effort, model string) {
	support := SupportedThinking(model)
	if !support.Thinks {
		return // nothing to control; keep the request clean
	}
	if effort == EffortOff {
		switch support.disable {
		case disableThinkingType:
			wire.Thinking = &thinkingParam{Type: "disabled"}
		case disableEnableThinking:
			off := false
			wire.EnableThinking = &off
		case disableEffortNone:
			wire.ReasoningEffort = "none"
		}
		// disableUnsupported (the model always thinks) and disableUnknown
		// (no documented switch) send nothing: a rejected request is worse
		// than an ignored preference.
		return
	}
	if effort == EffortAdaptive {
		return // no strength: the vendor default stands
	}
	level, ok := support.clamp(effort)
	if !ok {
		return // the model thinks, but has no strength control
	}
	wire.ReasoningEffort = level.reasoningEffort()
}

// Chat sends one completion request. Key flow: encode the provider-agnostic
// Request into OpenAI wire format, POST it, then normalize the first choice
// back into a Response the agent loop can act on.
func (p *openAICompatible) Chat(ctx context.Context, req Request) (*Response, error) {
	wire := chatRequest{
		Model:     req.Model,
		Messages:  toWireMessages(req.Messages),
		MaxTokens: req.MaxTokens,
	}
	applyThinking(&wire, p.effort, req.Model)
	for _, t := range req.Tools {
		wire.Tools = append(wire.Tools, wireTool{Type: "function", Function: t})
	}

	body, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	httpResp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s request failed: %w", p.name, err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var parsed chatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("%s: unexpected response (HTTP %d): %s",
			p.name, httpResp.StatusCode, truncate(string(respBody), 500))
	}
	if parsed.Error != nil {
		return nil, fmt.Errorf("%s API error: %s", p.name, parsed.Error.Message)
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d: %s", p.name, httpResp.StatusCode,
			truncate(string(respBody), 500))
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("%s: response contained no choices", p.name)
	}

	choice := parsed.Choices[0]
	return &Response{
		Message:      choice.Message,
		FinishReason: choice.FinishReason,
		Usage:        parsed.Usage,
	}, nil
}

// streamChunk is one SSE data payload of a streamed completion.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// ChatStream implements Streamer over the standard SSE protocol.
//
// Key flow: the request is the same as Chat plus stream flags; the response
// body is a sequence of "data: {json}" lines ending with "data: [DONE]".
// Text and reasoning fragments are forwarded to onDelta immediately; tool
// call fragments are accumulated by index (the arguments JSON arrives in
// pieces); the final usage chunk is captured. The assembled Response is
// indistinguishable from a blocking Chat result.
func (p *openAICompatible) ChatStream(ctx context.Context, req Request, onDelta func(Delta)) (*Response, error) {
	wire := chatRequest{
		Model:         req.Model,
		Messages:      toWireMessages(req.Messages),
		MaxTokens:     req.MaxTokens,
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
	}
	applyThinking(&wire, p.effort, req.Model)
	for _, t := range req.Tools {
		wire.Tools = append(wire.Tools, wireTool{Type: "function", Function: t})
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	httpResp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s request failed: %w", p.name, err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("%s: HTTP %d: %s", p.name, httpResp.StatusCode,
			truncate(string(respBody), 500))
	}

	var (
		content, reasoning strings.Builder
		calls              []ToolCall
		finish             string
		usage              Usage
	)
	scanner := bufio.NewScanner(httpResp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue // comments, event names, blank keep-alives
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return nil, fmt.Errorf("%s: bad stream chunk: %s", p.name, truncate(payload, 200))
		}
		if chunk.Error != nil {
			return nil, fmt.Errorf("%s API error: %s", p.name, chunk.Error.Message)
		}
		if chunk.Usage != nil {
			usage = *chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			continue // usage-only chunk
		}
		choice := chunk.Choices[0]
		if choice.FinishReason != "" {
			finish = choice.FinishReason
		}
		d := choice.Delta
		if d.Content != "" || d.ReasoningContent != "" {
			content.WriteString(d.Content)
			reasoning.WriteString(d.ReasoningContent)
			if onDelta != nil {
				onDelta(Delta{Content: d.Content, Reasoning: d.ReasoningContent})
			}
		}
		for _, tc := range d.ToolCalls {
			for len(calls) <= tc.Index {
				calls = append(calls, ToolCall{Type: "function"})
			}
			c := &calls[tc.Index]
			if tc.ID != "" {
				c.ID = tc.ID
			}
			if tc.Type != "" {
				c.Type = tc.Type
			}
			c.Function.Name += tc.Function.Name
			c.Function.Arguments += tc.Function.Arguments
		}
	}
	partial := &Response{
		Message: Message{
			Role:             RoleAssistant,
			Content:          content.String(),
			ReasoningContent: reasoning.String(),
			ToolCalls:        calls,
		},
		FinishReason: finish,
		Usage:        usage,
	}
	if err := scanner.Err(); err != nil {
		// A user interrupt cancels the context and aborts the read: hand back
		// the partial message so the caller can preserve what already streamed.
		if ctx.Err() != nil {
			return partial, ctx.Err()
		}
		return nil, fmt.Errorf("%s: stream read: %w", p.name, err)
	}
	// The scanner can also stop cleanly (no error) when the context is
	// cancelled between reads; surface that as an interrupt too.
	if ctx.Err() != nil {
		return partial, ctx.Err()
	}
	return partial, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
