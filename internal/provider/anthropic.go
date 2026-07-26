package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/xautjzd/agent-cli/internal/log"
)

// This file is the Anthropic Messages API adapter. It is the only place in
// the codebase that references Anthropic SDK types: the agent, tools and UI
// continue to depend solely on the Provider/Streamer interfaces (DIP), and
// the adapter's single responsibility is translating between this project's
// provider-agnostic types and the Anthropic wire format (SRP).
//
// The Anthropic API differs structurally from the OpenAI-compatible format
// used by the other providers, so the translation is non-trivial:
//
//   - The system prompt is a top-level field, not a message with a role.
//   - Assistant tool calls are `tool_use` content blocks whose input is a
//     JSON object; internally arguments are a JSON string.
//   - Tool results are `tool_result` blocks inside a *user* message, and all
//     results for one assistant turn must share a single message.
//   - Images are base64 blocks with an explicit media type rather than
//     data URLs.
//   - Thinking blocks carry signatures and must be echoed back unchanged.
//   - max_tokens is mandatory.

// Default model when the configuration does not name one.
const defaultAnthropicModel = "claude-opus-4-8"

// Anthropic requires an explicit max_tokens. These defaults keep blocking
// requests inside SDK HTTP timeouts while giving streamed requests room.
const (
	defaultAnthropicMaxTokens       = 16000
	defaultAnthropicStreamMaxTokens = 64000
)

// answerReserveTokens is the headroom kept for the visible answer above a
// thinking budget, so the budget stays comfortably below max_tokens (Anthropic
// rejects a budget that is not strictly below max_tokens). minThinkingBudget is
// Anthropic's floor: a budget below it cannot enable thinking.
const (
	answerReserveTokens = 4096
	minThinkingBudget   = 1024
)

func init() {
	Register("anthropic", func(cfg Config) (Provider, error) {
		return NewAnthropic("anthropic", cfg)
	})
}

// NewAnthropic builds a Messages API provider under an arbitrary display
// name. Any Anthropic-compatible endpoint works by setting BaseURL — several
// vendors (Zhipu GLM, DashScope, Moonshot, DeepSeek) expose one so that
// Claude Code–style clients can talk to their models.
func NewAnthropic(name string, cfg Config) (Provider, error) {
	// Unlike the OpenAI-compatible vendors, an unset key is not an error
	// here: the Anthropic SDK also resolves credentials from
	// ANTHROPIC_API_KEY and from an `ant auth login` profile on disk, so an
	// empty key is left to the SDK's own resolution chain.
	var opts []option.RequestOption
	if cfg.APIKey != "" {
		// Anthropic itself authenticates with x-api-key; most compatible
		// gateways expect Authorization: Bearer instead.
		if strings.EqualFold(cfg.Auth, AuthBearer) {
			opts = append(opts, option.WithAuthToken(cfg.APIKey))
		} else {
			opts = append(opts, option.WithAPIKey(cfg.APIKey))
		}
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	client := anthropic.NewClient(opts...)
	return &anthropicProvider{
		name:     name,
		client:   &client,
		thinking: cfg.Thinking,
		// Prompt caching is on unless explicitly disabled, so real Anthropic
		// requests cache by default while a compatible gateway that rejects
		// the cache_control field can opt out.
		cache: !strings.EqualFold(cfg.PromptCache, "off"),
	}, nil
}

// anthropicProvider implements Provider and Streamer over the official
// Anthropic Go SDK.
type anthropicProvider struct {
	// name is the display name — "anthropic", or a config profile name
	// when pointed at an Anthropic-compatible third-party endpoint.
	name   string
	client *anthropic.Client
	// thinking selects extended-thinking behavior: "off" disables it,
	// anything else uses adaptive thinking with summarized display so the
	// reasoning surfaces through the normal thinking UI.
	thinking string
	// cache enables prompt-cache breakpoints (cache_control) on the stable
	// request prefix — tools, the system prompt and recent turns.
	cache bool
}

func (p *anthropicProvider) Name() string { return p.name }

// Chat performs one blocking Messages API round-trip.
func (p *anthropicProvider) Chat(ctx context.Context, req Request) (*Response, error) {
	log.Debug("provider", "%s Chat: model=%s, %d messages, %d tools", p.name, req.Model, len(req.Messages), len(req.Tools))
	start := time.Now()
	params, err := p.buildParams(req, defaultAnthropicMaxTokens)
	if err != nil {
		return nil, err
	}
	msg, err := p.client.Messages.New(ctx, *params)
	if err != nil {
		log.Warn("provider", "%s Chat: API error: %v", p.name, err)
		return nil, p.wrapError(err)
	}
	log.Debug("provider", "%s Chat: done in %s", p.name, time.Since(start).Round(time.Millisecond))
	return fromAnthropicMessage(msg)
}

// ChatStream implements Streamer. The SDK's Accumulate helper rebuilds the
// complete message from the event stream, so the assembled Response is
// identical to the blocking path while text and thinking fragments are
// forwarded live.
func (p *anthropicProvider) ChatStream(ctx context.Context, req Request, onDelta func(Delta)) (*Response, error) {
	log.Debug("provider", "%s ChatStream: model=%s, %d messages, %d tools", p.name, req.Model, len(req.Messages), len(req.Tools))
	start := time.Now()
	params, err := p.buildParams(req, defaultAnthropicStreamMaxTokens)
	if err != nil {
		return nil, err
	}
	stream := p.client.Messages.NewStreaming(ctx, *params)
	var acc anthropic.Message
	for stream.Next() {
		event := stream.Current()
		if err := acc.Accumulate(event); err != nil {
			return nil, fmt.Errorf("anthropic: accumulate stream: %w", err)
		}
		if event.Type != "content_block_delta" || onDelta == nil {
			continue
		}
		// Text and thinking arrive as separate delta kinds; tool-call
		// argument fragments (partial_json) are assembled by Accumulate
		// and are not surfaced as display deltas.
		if t := event.Delta.Text; t != "" {
			onDelta(Delta{Content: t})
		}
		if t := event.Delta.Thinking; t != "" {
			onDelta(Delta{Reasoning: t})
		}
	}
	if err := stream.Err(); err != nil {
		// On a user interrupt (cancelled context) hand back the partial message
		// assembled so far, so the caller can preserve what already streamed.
		if ctx.Err() != nil {
			if resp, ferr := fromAnthropicMessage(&acc); ferr == nil {
				return resp, ctx.Err()
			}
		}
		log.Warn("provider", "%s ChatStream: stream error: %v", p.name, err)
		return nil, p.wrapError(err)
	}
	// A stream that yielded nothing at all means the endpoint answered
	// without server-sent events — common on third-party Anthropic-
	// compatible gateways that only implement the blocking API. Report it
	// instead of returning a silently empty answer.
	if acc.StopReason == "" && len(acc.Content) == 0 {
		log.Warn("provider", "%s ChatStream: no stream events", p.name)
		return nil, fmt.Errorf("%s: endpoint returned no stream events "+
			"(it may not support streaming; check its base_url and API compatibility)", p.name)
	}
	log.Debug("provider", "%s ChatStream: done in %s", p.name, time.Since(start).Round(time.Millisecond))
	return fromAnthropicMessage(&acc)
}

// buildParams translates a provider-agnostic Request into Anthropic
// parameters.
//
// Key flow: the system prompt is lifted out of the message list into the
// top-level system field, the remaining messages are converted block by
// block, and tools are re-shaped from the OpenAI function form into
// Anthropic's flat {name, description, input_schema}.
func (p *anthropicProvider) buildParams(req Request, defaultMaxTokens int) (*anthropic.MessageNewParams, error) {
	model := req.Model
	if model == "" {
		model = defaultAnthropicModel
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}

	system, msgs, err := toAnthropicMessages(req.Messages)
	if err != nil {
		return nil, err
	}

	params := &anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: int64(maxTokens),
		Messages:  msgs,
	}
	if system != "" {
		params.System = []anthropic.TextBlockParam{{Text: system}}
	}
	// Thinking is driven by the effort level: a fixed budget for low/medium/
	// high, model-chosen budget for adaptive, and omitted for off. Summarized
	// display makes the reasoning visible to the existing thinking renderer.
	effort, _ := ParseEffort(p.thinking)
	switch {
	case effort == EffortOff:
		// Send an explicit disabled block rather than omitting the field.
		// Native Anthropic treats an absent field as off, but Anthropic-
		// compatible gateways (e.g. GLM/Zhipu) default thinking ON when it is
		// omitted, so "off" only takes effect when disabled is stated outright.
		params.Thinking = anthropic.ThinkingConfigParamUnion{
			OfDisabled: &anthropic.ThinkingConfigDisabledParam{},
		}
	case effort.budgetTokens() > 0:
		budget := effort.budgetTokens()
		// Anthropic requires budget_tokens < max_tokens with room for the
		// answer. Rather than raise max_tokens — which can trip the SDK's
		// "streaming required" guard on the blocking path — cap the budget to
		// the available headroom. The streaming path (large max_tokens) keeps
		// the full budget; internal blocking calls get a smaller one.
		if ceiling := int(params.MaxTokens) - answerReserveTokens; budget > ceiling {
			budget = ceiling
		}
		// Below Anthropic's 1024 minimum there is no room to think at all.
		if budget >= minThinkingBudget {
			params.Thinking = anthropic.ThinkingConfigParamOfEnabled(int64(budget))
		}
	default: // adaptive
		params.Thinking = anthropic.ThinkingConfigParamUnion{
			OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{
				Display: anthropic.ThinkingConfigAdaptiveDisplaySummarized,
			},
		}
	}
	for _, t := range req.Tools {
		tool, err := toAnthropicTool(t)
		if err != nil {
			return nil, err
		}
		params.Tools = append(params.Tools, tool)
	}
	if p.cache {
		applyCacheControl(params)
	}
	return params, nil
}

// applyCacheControl marks the stable prefix of a request with cache_control
// breakpoints so Anthropic serves it from its prompt cache on later turns.
//
// A breakpoint caches every block before and including it, and the API allows
// at most four. They are placed, in prefix order, on:
//
//   - the last tool definition — caches the whole (stable) tool block;
//   - the last system-prompt block — caches tools + the large system prompt,
//     which is the dominant and guaranteed cross-turn hit;
//   - the last block of each of the two most recent messages — extends the
//     cached prefix over the growing conversation so each new turn re-reads
//     the previous one instead of reprocessing it.
func applyCacheControl(params *anthropic.MessageNewParams) {
	// The TTL is set explicitly to the default 5 minutes: an empty
	// CacheControlEphemeralParam is treated as a zero value and dropped by
	// the SDK's `omitzero` marshaling, so it must carry a field to appear on
	// the wire.
	ephemeral := anthropic.CacheControlEphemeralParam{TTL: anthropic.CacheControlEphemeralTTLTTL5m}

	if n := len(params.Tools); n > 0 {
		if cc := params.Tools[n-1].GetCacheControl(); cc != nil {
			*cc = ephemeral
		}
	}
	if n := len(params.System); n > 0 {
		params.System[n-1].CacheControl = ephemeral
	}

	// The two most recent messages that carry at least one block. Walking
	// from the end keeps the earlier of the two roughly where the previous
	// turn's write landed, turning it into a read on the next request.
	marked := 0
	for i := len(params.Messages) - 1; i >= 0 && marked < 2; i-- {
		blocks := params.Messages[i].Content
		if len(blocks) == 0 {
			continue
		}
		if cc := blocks[len(blocks)-1].GetCacheControl(); cc != nil {
			*cc = ephemeral
			marked++
		}
	}
}

// toAnthropicTool converts one tool definition. The internal schema is raw
// JSON Schema; Anthropic wants properties and required split out, so the
// schema is decoded and re-mapped, with any extra keywords preserved.
func toAnthropicTool(t ToolDef) (anthropic.ToolUnionParam, error) {
	var schema struct {
		Properties any            `json:"properties"`
		Required   []string       `json:"required"`
		Extra      map[string]any `json:"-"`
	}
	if len(t.Parameters) > 0 {
		if err := json.Unmarshal(t.Parameters, &schema); err != nil {
			return anthropic.ToolUnionParam{}, fmt.Errorf("tool %s: invalid input schema: %w", t.Name, err)
		}
	}
	tool := anthropic.ToolParam{
		Name: t.Name,
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: schema.Properties,
			Required:   schema.Required,
		},
	}
	if t.Description != "" {
		tool.Description = anthropic.String(t.Description)
	}
	return anthropic.ToolUnionParam{OfTool: &tool}, nil
}

// toAnthropicMessages splits the conversation into the top-level system
// prompt and the Anthropic message list.
//
// Key flow: tool results are buffered and flushed as a single user message
// so that all results for one assistant turn travel together — splitting
// them across messages teaches the model to stop issuing parallel tool
// calls.
func toAnthropicMessages(msgs []Message) (system string, out []anthropic.MessageParam, err error) {
	var systemParts []string
	var pendingResults []anthropic.ContentBlockParamUnion

	flushResults := func() {
		if len(pendingResults) > 0 {
			out = append(out, anthropic.NewUserMessage(pendingResults...))
			pendingResults = nil
		}
	}

	for _, m := range msgs {
		switch m.Role {
		case RoleSystem:
			systemParts = append(systemParts, m.Content)

		case RoleTool:
			// Accumulated until the next non-tool message.
			pendingResults = append(pendingResults,
				anthropic.NewToolResultBlock(m.ToolCallID, m.Content, isErrorResult(m.Content)))

		case RoleUser:
			flushResults()
			blocks, err := userBlocks(m)
			if err != nil {
				return "", nil, err
			}
			if len(blocks) > 0 {
				out = append(out, anthropic.NewUserMessage(blocks...))
			}

		case RoleAssistant:
			flushResults()
			blocks := assistantBlocks(m)
			if len(blocks) > 0 {
				out = append(out, anthropic.NewAssistantMessage(blocks...))
			}
		}
	}
	flushResults()
	return strings.Join(systemParts, "\n\n"), out, nil
}

// isErrorResult marks tool results the registry rendered as failures, so
// Claude sees them as errors rather than as ordinary output.
func isErrorResult(content string) bool {
	return strings.HasPrefix(content, "Error:")
}

// userBlocks converts a user message, translating any multimodal image
// parts from data URLs into Anthropic base64 image blocks.
func userBlocks(m Message) ([]anthropic.ContentBlockParamUnion, error) {
	if len(m.Parts) == 0 {
		if m.Content == "" {
			return nil, nil
		}
		return []anthropic.ContentBlockParamUnion{anthropic.NewTextBlock(m.Content)}, nil
	}

	var blocks []anthropic.ContentBlockParamUnion
	for _, part := range m.Parts {
		switch part.Type {
		case "text":
			if part.Text != "" {
				blocks = append(blocks, anthropic.NewTextBlock(part.Text))
			}
		case "image_url":
			if part.ImageURL == nil {
				continue
			}
			mediaType, data, err := decodeDataURL(part.ImageURL.URL)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, anthropic.NewImageBlockBase64(mediaType, data))
		}
	}
	return blocks, nil
}

// decodeDataURL splits a "data:<media-type>;base64,<data>" URL. Anthropic
// takes the media type and payload as separate fields, and only supports
// base64 sources for inline images.
func decodeDataURL(url string) (mediaType, data string, err error) {
	const prefix = "data:"
	if !strings.HasPrefix(url, prefix) {
		return "", "", fmt.Errorf("anthropic: image must be a base64 data URL, got %.32q", url)
	}
	meta, payload, found := strings.Cut(strings.TrimPrefix(url, prefix), ",")
	if !found {
		return "", "", fmt.Errorf("anthropic: malformed image data URL")
	}
	mediaType, encoding, _ := strings.Cut(meta, ";")
	if encoding != "base64" {
		return "", "", fmt.Errorf("anthropic: image data URL must be base64-encoded")
	}
	if _, err := base64.StdEncoding.DecodeString(payload); err != nil {
		return "", "", fmt.Errorf("anthropic: image data is not valid base64: %w", err)
	}
	return mediaType, payload, nil
}

// assistantState is the opaque round-trip payload stored on assistant
// messages. Thinking blocks must be replayed with their signature intact,
// and tool inputs must be replayed as JSON objects, so the exact blocks are
// recorded rather than reconstructed from the flattened text form.
type assistantState struct {
	Blocks []assistantBlock `json:"blocks"`
}

type assistantBlock struct {
	Type      string          `json:"type"` // text | thinking | tool_use
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
}

// assistantBlocks rebuilds an assistant turn. Turns this provider produced
// are replayed verbatim from their recorded state; turns inherited from
// another provider (after a mid-session switch) are reconstructed from the
// portable text and tool-call fields.
func assistantBlocks(m Message) []anthropic.ContentBlockParamUnion {
	if len(m.ProviderState) > 0 {
		var state assistantState
		if err := json.Unmarshal(m.ProviderState, &state); err == nil {
			var blocks []anthropic.ContentBlockParamUnion
			for _, b := range state.Blocks {
				switch b.Type {
				case "text":
					if b.Text != "" {
						blocks = append(blocks, anthropic.NewTextBlock(b.Text))
					}
				case "thinking":
					blocks = append(blocks, anthropic.NewThinkingBlock(b.Signature, b.Thinking))
				case "tool_use":
					input := json.RawMessage("{}")
					if len(b.Input) > 0 {
						input = b.Input
					}
					blocks = append(blocks, anthropic.NewToolUseBlock(b.ID, input, b.Name))
				}
			}
			if len(blocks) > 0 {
				return blocks
			}
		}
	}

	var blocks []anthropic.ContentBlockParamUnion
	if m.Content != "" {
		blocks = append(blocks, anthropic.NewTextBlock(m.Content))
	}
	for _, call := range m.ToolCalls {
		args := json.RawMessage(call.Function.Arguments)
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		blocks = append(blocks, anthropic.NewToolUseBlock(call.ID, args, call.Function.Name))
	}
	return blocks
}

// fromAnthropicMessage normalizes a Messages API reply into the internal
// Response shape: text and thinking are flattened into their respective
// fields, tool_use blocks become ToolCalls with JSON-string arguments, and
// the exact blocks are preserved for faithful replay.
func fromAnthropicMessage(msg *anthropic.Message) (*Response, error) {
	if msg == nil {
		return nil, fmt.Errorf("anthropic: empty response")
	}
	var text, thinking strings.Builder
	var calls []ToolCall
	state := assistantState{}

	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
			state.Blocks = append(state.Blocks, assistantBlock{Type: "text", Text: block.Text})
		case "thinking":
			thinking.WriteString(block.Thinking)
			state.Blocks = append(state.Blocks, assistantBlock{
				Type: "thinking", Thinking: block.Thinking, Signature: block.Signature,
			})
		case "tool_use":
			// Internally tool arguments are a JSON string, matching the
			// OpenAI-compatible shape the tool registry already parses.
			args := string(block.Input)
			if args == "" {
				args = "{}"
			}
			calls = append(calls, ToolCall{
				ID:       block.ID,
				Type:     "function",
				Function: FunctionCall{Name: block.Name, Arguments: args},
			})
			state.Blocks = append(state.Blocks, assistantBlock{
				Type: "tool_use", ID: block.ID, Name: block.Name, Input: block.Input,
			})
		}
	}

	out := Message{
		Role:             RoleAssistant,
		Content:          text.String(),
		ReasoningContent: thinking.String(),
		ToolCalls:        calls,
	}
	if len(state.Blocks) > 0 {
		if encoded, err := json.Marshal(state); err == nil {
			out.ProviderState = encoded
		}
	}

	// Anthropic reports input_tokens as the uncached portion only, keeping
	// cache reads and writes in separate fields. Fold them back so
	// PromptTokens is the full input context (as the other providers report
	// it) and the cache breakdown stays available on the side.
	inputTokens := int(msg.Usage.InputTokens + msg.Usage.CacheReadInputTokens + msg.Usage.CacheCreationInputTokens)
	outputTokens := int(msg.Usage.OutputTokens)

	return &Response{
		Message:      out,
		FinishReason: string(msg.StopReason),
		Usage: Usage{
			PromptTokens:     inputTokens,
			CompletionTokens: outputTokens,
			TotalTokens:      inputTokens + outputTokens,
			// Reads are billed at a discount and writes at a premium; both
			// are a subset of PromptTokens.
			CacheCreationTokens: int(msg.Usage.CacheCreationInputTokens),
			CacheReadTokens:     int(msg.Usage.CacheReadInputTokens),
		},
	}, nil
}

// anthropicError unwraps SDK errors into messages consistent with the other
// providers, surfacing the HTTP status when the API rejected the request.
func (p *anthropicProvider) wrapError(err error) error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		return fmt.Errorf("%s: HTTP %d: %s", p.name, apiErr.StatusCode, apiErr.Error())
	}
	return fmt.Errorf("%s request failed: %w", p.name, err)
}
