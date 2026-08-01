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

const (
	openAICodexBaseURL = "https://chatgpt.com/backend-api"
	maxCodexErrorBody  = 1 << 20
	maxCodexSSEEvent   = 4 << 20
)

var openAISubscriptionModels = []ModelInfo{
	{ID: "gpt-5.6-sol", Name: "Latest frontier agentic coding model."},
	{ID: "gpt-5.6-terra", Name: "Balanced agentic coding model for everyday work."},
	{ID: "gpt-5.6-luna", Name: "Fast and affordable agentic coding model."},
	{ID: "gpt-5.5", Name: "Frontier model for complex coding, research, and real-world work."},
	{ID: "gpt-5.4", Name: "Strong model for everyday coding."},
	{ID: "gpt-5.4-mini", Name: "Small, fast, and cost-efficient model for simpler coding tasks."},
}

// openAICodex implements the ChatGPT subscription Responses protocol. It is a
// separate wire adapter from openAICompatible even though both report the
// stable provider name "openai".
type openAICodex struct {
	auth    AuthSource
	baseURL string
	client  *http.Client
	effort  Effort
}

// NewOpenAICodex builds the OpenAI subscription provider. Authentication is
// resolved per request so rotated access tokens take effect immediately.
func NewOpenAICodex(source AuthSource, cfg Config) (Provider, error) {
	if source == nil {
		return nil, fmt.Errorf("OpenAI subscription auth source is required")
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base != "" && base != openAIBaseURL {
		return nil, fmt.Errorf("OpenAI subscription authentication cannot be used with a custom base URL")
	}
	base = openAICodexBaseURL
	effort, _ := ParseEffort(cfg.Thinking)
	return &openAICodex{
		auth:    source,
		baseURL: base,
		client:  &http.Client{Timeout: 5 * time.Minute},
		effort:  effort,
	}, nil
}

func (p *openAICodex) Name() string { return "openai" }

// Models returns the model IDs accepted by the ChatGPT subscription transport.
// The API-key transport intentionally continues to use the broader OpenAI API
// catalog because the two products have different model entitlements.
func (*openAICodex) Models(context.Context) ([]ModelInfo, error) {
	return append([]ModelInfo(nil), openAISubscriptionModels...), nil
}

func supportsOpenAISubscriptionModel(model string) bool {
	for _, available := range openAISubscriptionModels {
		if model == available.ID {
			return true
		}
	}
	return false
}

func openAISubscriptionModelIDs() []string {
	ids := make([]string, len(openAISubscriptionModels))
	for i, model := range openAISubscriptionModels {
		ids[i] = model.ID
	}
	return ids
}

type codexRequest struct {
	Model             string          `json:"model"`
	Store             bool            `json:"store"`
	Stream            bool            `json:"stream"`
	Instructions      string          `json:"instructions"`
	Input             []any           `json:"input"`
	Tools             []codexTool     `json:"tools,omitempty"`
	ToolChoice        string          `json:"tool_choice,omitempty"`
	ParallelToolCalls bool            `json:"parallel_tool_calls,omitempty"`
	Include           []string        `json:"include,omitempty"`
	Reasoning         *codexReasoning `json:"reasoning,omitempty"`
}

type codexReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type codexTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict"`
}

type codexState struct {
	Reasoning []json.RawMessage `json:"reasoning,omitempty"`
}

func buildCodexRequest(req Request, effort Effort) codexRequest {
	wire := codexRequest{
		Model:             req.Model,
		Store:             false,
		Stream:            true,
		ToolChoice:        "auto",
		ParallelToolCalls: true,
		Include:           []string{"reasoning.encrypted_content"},
	}
	var instructions []string
	for _, msg := range req.Messages {
		switch msg.Role {
		case RoleSystem:
			if msg.Content != "" {
				instructions = append(instructions, msg.Content)
			}
		case RoleUser:
			parts := make([]any, 0, max(1, len(msg.Parts)))
			if len(msg.Parts) == 0 {
				parts = append(parts, map[string]any{"type": "input_text", "text": msg.Content})
			} else {
				for _, part := range msg.Parts {
					switch part.Type {
					case "text":
						parts = append(parts, map[string]any{"type": "input_text", "text": part.Text})
					case "image_url":
						if part.ImageURL != nil {
							parts = append(parts, map[string]any{"type": "input_image", "detail": "auto", "image_url": part.ImageURL.URL})
						}
					}
				}
			}
			if len(parts) > 0 {
				wire.Input = append(wire.Input, map[string]any{"role": "user", "content": parts})
			}
		case RoleAssistant:
			var state codexState
			if len(msg.ProviderState) > 0 && json.Unmarshal(msg.ProviderState, &state) == nil {
				for _, item := range state.Reasoning {
					var raw any
					if json.Unmarshal(item, &raw) == nil {
						wire.Input = append(wire.Input, raw)
					}
				}
			}
			if msg.Content != "" {
				wire.Input = append(wire.Input, map[string]any{
					"type": "message", "role": "assistant", "status": "completed",
					"content": []any{map[string]any{"type": "output_text", "text": msg.Content, "annotations": []any{}}},
				})
			}
			for _, call := range msg.ToolCalls {
				wire.Input = append(wire.Input, map[string]any{
					"type": "function_call", "call_id": call.ID,
					"name": call.Function.Name, "arguments": call.Function.Arguments,
				})
			}
		case RoleTool:
			wire.Input = append(wire.Input, map[string]any{
				"type": "function_call_output", "call_id": msg.ToolCallID, "output": msg.Content,
			})
		}
	}
	wire.Instructions = strings.Join(instructions, "\n\n")
	if wire.Instructions == "" {
		wire.Instructions = "You are a helpful assistant."
	}
	for _, tool := range req.Tools {
		wire.Tools = append(wire.Tools, codexTool{Type: "function", Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters})
	}
	if effort != "" && effort != EffortAdaptive && effort != EffortOff {
		wire.Reasoning = &codexReasoning{Effort: effort.reasoningEffort(), Summary: "auto"}
	}
	return wire
}

func (p *openAICodex) endpoint() string {
	if strings.HasSuffix(p.baseURL, "/codex/responses") {
		return p.baseURL
	}
	if strings.HasSuffix(p.baseURL, "/codex") {
		return p.baseURL + "/responses"
	}
	return p.baseURL + "/codex/responses"
}

func (p *openAICodex) Chat(ctx context.Context, req Request) (*Response, error) {
	return p.ChatStream(ctx, req, func(Delta) {})
}

func (p *openAICodex) ChatStream(ctx context.Context, req Request, onDelta func(Delta)) (*Response, error) {
	resolved, err := p.auth.Auth(ctx)
	if err != nil {
		return nil, fmt.Errorf("OpenAI subscription authentication failed: %w", err)
	}
	if resolved.Token == "" || resolved.AccountID == "" {
		return nil, fmt.Errorf("OpenAI subscription authentication is incomplete; run auth login openai")
	}
	if !supportsOpenAISubscriptionModel(req.Model) {
		return nil, fmt.Errorf("OpenAI subscription model %q is not supported; choose one of: %s", req.Model, strings.Join(openAISubscriptionModelIDs(), ", "))
	}
	body, err := json.Marshal(buildCodexRequest(req, p.effort))
	if err != nil {
		return nil, fmt.Errorf("encode OpenAI Codex request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+resolved.Token)
	httpReq.Header.Set("ChatGPT-Account-Id", resolved.AccountID)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("OpenAI-Beta", "responses=experimental")
	httpReq.Header.Set("Originator", "agent-cli")
	httpReq.Header.Set("User-Agent", "agent-cli")

	httpResp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("OpenAI Codex request failed: %w", err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(httpResp.Body, maxCodexErrorBody))
		return nil, fmt.Errorf("OpenAI Codex request failed (HTTP %d): %s", httpResp.StatusCode, safeCodexError(data))
	}
	return parseCodexStream(httpResp.Body, onDelta)
}

func safeCodexError(data []byte) string {
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &payload) == nil {
		if payload.Error.Code != "" {
			return payload.Error.Code
		}
	}
	return "request rejected"
}

type codexEvent struct {
	Type        string          `json:"type"`
	Delta       string          `json:"delta"`
	Arguments   string          `json:"arguments"`
	OutputIndex int             `json:"output_index"`
	Item        json.RawMessage `json:"item"`
	Response    *struct {
		Status string `json:"status"`
		Error  *struct {
			Code string `json:"code"`
		} `json:"error"`
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
			Details      struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	} `json:"response"`
	Error *struct {
		Code string `json:"code"`
	} `json:"error"`
}

type codexOutputItem struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Summary   []struct {
		Text string `json:"text"`
	} `json:"summary"`
}

func parseCodexStream(body io.Reader, onDelta func(Delta)) (*Response, error) {
	response := &Response{Message: Message{Role: RoleAssistant}, FinishReason: "stop"}
	state := codexState{}
	calls := map[int]*ToolCall{}
	order := []int{}
	completed := false
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxCodexSSEEvent)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var event codexEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return nil, fmt.Errorf("OpenAI Codex stream contained invalid JSON")
		}
		switch event.Type {
		case "response.output_text.delta":
			response.Message.Content += event.Delta
			if onDelta != nil {
				onDelta(Delta{Content: event.Delta})
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			response.Message.ReasoningContent += event.Delta
			if onDelta != nil {
				onDelta(Delta{Reasoning: event.Delta})
			}
		case "response.output_item.added":
			var item codexOutputItem
			if json.Unmarshal(event.Item, &item) == nil && item.Type == "function_call" {
				call := &ToolCall{ID: item.CallID, Type: "function", Function: FunctionCall{Name: item.Name, Arguments: item.Arguments}}
				calls[event.OutputIndex] = call
				order = append(order, event.OutputIndex)
			}
		case "response.function_call_arguments.delta":
			if call := calls[event.OutputIndex]; call != nil {
				call.Function.Arguments += event.Delta
			}
		case "response.function_call_arguments.done":
			if call := calls[event.OutputIndex]; call != nil && event.Arguments != "" {
				call.Function.Arguments = event.Arguments
			}
		case "response.output_item.done":
			var item codexOutputItem
			if json.Unmarshal(event.Item, &item) != nil {
				return nil, fmt.Errorf("OpenAI Codex stream contained an invalid output item")
			}
			if item.Type == "reasoning" {
				state.Reasoning = append(state.Reasoning, append(json.RawMessage(nil), event.Item...))
				if response.Message.ReasoningContent == "" {
					for i, part := range item.Summary {
						if i > 0 {
							response.Message.ReasoningContent += "\n\n"
						}
						response.Message.ReasoningContent += part.Text
					}
				}
			} else if item.Type == "function_call" {
				call := calls[event.OutputIndex]
				if call == nil {
					call = &ToolCall{ID: item.CallID, Type: "function"}
					calls[event.OutputIndex] = call
					order = append(order, event.OutputIndex)
				}
				call.ID, call.Function.Name = item.CallID, item.Name
				if item.Arguments != "" {
					call.Function.Arguments = item.Arguments
				}
			}
		case "response.completed", "response.incomplete":
			if event.Response == nil {
				return nil, fmt.Errorf("OpenAI Codex stream ended without response metadata")
			}
			completed = true
			if event.Response.Status == "incomplete" || event.Type == "response.incomplete" {
				response.FinishReason = "length"
			}
			if event.Response.Usage != nil {
				response.Usage = Usage{
					PromptTokens: event.Response.Usage.InputTokens, CompletionTokens: event.Response.Usage.OutputTokens,
					TotalTokens: event.Response.Usage.TotalTokens, CacheReadTokens: event.Response.Usage.Details.CachedTokens,
				}
			}
		case "response.failed":
			code := "response_failed"
			if event.Response != nil && event.Response.Error != nil && event.Response.Error.Code != "" {
				code = event.Response.Error.Code
			}
			return nil, fmt.Errorf("OpenAI Codex response failed: %s", code)
		case "error":
			code := "stream_error"
			if event.Error != nil && event.Error.Code != "" {
				code = event.Error.Code
			}
			return nil, fmt.Errorf("OpenAI Codex stream error: %s", code)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read OpenAI Codex stream: %w", err)
	}
	if !completed {
		return nil, fmt.Errorf("OpenAI Codex stream ended before completion")
	}
	for _, index := range order {
		if call := calls[index]; call != nil {
			response.Message.ToolCalls = append(response.Message.ToolCalls, *call)
		}
	}
	if len(response.Message.ToolCalls) > 0 {
		response.FinishReason = "tool_calls"
	}
	if len(state.Reasoning) > 0 {
		response.Message.ProviderState, _ = json.Marshal(state)
	}
	return response, nil
}
