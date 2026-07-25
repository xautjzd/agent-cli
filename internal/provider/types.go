// Package provider defines the abstraction over LLM chat-completion APIs.
//
// The package follows the Dependency Inversion Principle: the agent core
// depends only on the Provider interface declared here, never on a concrete
// HTTP client. New providers are added by implementing Provider and
// registering a factory (Open/Closed Principle).
package provider

import "encoding/json"

// Role constants for chat messages, matching the OpenAI wire format that
// both OpenAI and DeepSeek (and most compatible vendors) share.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Part is one segment of a multimodal message, following the OpenAI
// content-array format shared by vision-capable compatible providers.
type Part struct {
	Type string `json:"type"` // "text" or "image_url"
	Text string `json:"text,omitempty"`
	// ImageURL carries an image, typically as a data: URL with base64
	// content so no hosting is needed.
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// ImageURL wraps an image reference for a Part.
type ImageURL struct {
	URL string `json:"url"`
}

// Message is a single turn in a conversation.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// Parts, when non-empty, replaces Content on the wire with a
	// multimodal content array (text + images). Content is still kept as
	// the plain-text form for display and search.
	Parts     []Part     `json:"parts,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID links a RoleTool message back to the assistant tool call
	// it answers. Required by the API when Role == RoleTool.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// ReasoningContent carries chain-of-thought from reasoning models
	// (e.g. deepseek-v4-pro thinking mode). It is display-only: the agent must clear
	// it before appending the message to history, since providers reject
	// or ignore reasoning echoed back as context.
	ReasoningContent string `json:"reasoning_content,omitempty"`
	// ProviderState carries opaque provider-specific data that must be
	// echoed back verbatim on later requests — Anthropic thinking blocks
	// with their signatures, for example, which the API rejects if altered
	// or dropped. The agent never interprets it; only the provider that
	// produced it reads it back.
	ProviderState json.RawMessage `json:"provider_state,omitempty"`
}

// WireContent returns what the "content" field should carry on the wire: a
// parts array for multimodal messages, otherwise the plain string.
func (m Message) WireContent() any {
	if len(m.Parts) > 0 {
		return m.Parts
	}
	return m.Content
}

// ToolCall is a tool invocation requested by the model.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // always "function" today
	Function FunctionCall `json:"function"`
}

// FunctionCall carries the tool name and its JSON-encoded arguments.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolDef describes a tool the model is allowed to call. Parameters must be
// a valid JSON Schema object.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Request is a provider-agnostic chat completion request.
type Request struct {
	Model    string
	Messages []Message
	Tools    []ToolDef
	// MaxTokens caps the completion length; 0 means provider default.
	MaxTokens int
}

// Usage reports token consumption for a single completion.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	// CacheCreationTokens counts prompt tokens written to the provider's
	// prefix cache on this request (billed at a write premium). Anthropic
	// reports it as cache_creation_input_tokens; providers without prompt
	// caching leave it zero.
	CacheCreationTokens int `json:"cache_creation_tokens,omitempty"`
	// CacheReadTokens counts prompt tokens served from the prefix cache on
	// this request (billed at a large discount). Anthropic reports it as
	// cache_read_input_tokens. It is a subset of PromptTokens.
	CacheReadTokens int `json:"cache_read_tokens,omitempty"`
}

// Response is the assistant's reply to a Request. When ToolCalls is
// non-empty the caller is expected to execute them and continue the loop.
type Response struct {
	Message      Message
	FinishReason string
	Usage        Usage
}
