// Copyright (c) 2026 PawAgents Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package openaicompat

// Wire types for the OpenAI Chat Completions protocol, which is also the
// de-facto format spoken by DeepSeek, DashScope, OpenRouter, LiteLLM, vLLM,
// corporate gateways and Ollama's compatibility endpoint.
//
// Every field is written by hand rather than generated so that the exact JSON
// name is visible next to the Go field. Providers in this family disagree
// about which optional fields they accept, which is why almost everything is
// omitempty: a field we do not need must not appear on the wire.

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`

	Tools      []chatTool `json:"tools,omitempty"`
	ToolChoice any        `json:"tool_choice,omitempty"`

	MaxTokens   int      `json:"max_tokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	Stop        []string `json:"stop,omitempty"`

	ResponseFormat *chatResponseFormat `json:"response_format,omitempty"`

	Stream        bool               `json:"stream,omitempty"`
	StreamOptions *chatStreamOptions `json:"stream_options,omitempty"`

	// Extra carries the provider extra_body and the per-request extras. It is
	// merged into the encoded object instead of being a JSON field, so a user
	// can reach any vendor specific knob without a code change.
	Extra map[string]any `json:"-"`
}

type chatMessage struct {
	Role string `json:"role"`
	// Content is either a string or a list of content parts, which is why it
	// is typed as any.
	Content    any            `json:"content,omitempty"`
	Name       string         `json:"name,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *chatImageURL `json:"image_url,omitempty"`
}

type chatImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Strict      bool           `json:"strict,omitempty"`
}

type chatToolCall struct {
	Index    *int             `json:"index,omitempty"`
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function chatFunctionCall `json:"function"`
}

type chatFunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type chatResponseFormat struct {
	Type       string          `json:"type"`
	JSONSchema *chatJSONSchema `json:"json_schema,omitempty"`
}

type chatJSONSchema struct {
	Name   string         `json:"name"`
	Schema map[string]any `json:"schema"`
	Strict bool           `json:"strict,omitempty"`
}

type chatStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// chatCompletion is the response of a non streaming request. The same shape is
// reused for streaming chunks, where only Choices[].Delta is populated.
type chatCompletion struct {
	ID      string       `json:"id"`
	Model   string       `json:"model"`
	Choices []chatChoice `json:"choices"`
	Usage   *chatUsage   `json:"usage"`
	Error   *chatError   `json:"error"`
}

type chatChoice struct {
	Index        int          `json:"index"`
	Message      *chatMessage `json:"message"`
	Delta        *chatDelta   `json:"delta"`
	FinishReason string       `json:"finish_reason"`
}

type chatDelta struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ReasoningContent is emitted by DeepSeek and several compatible servers.
	ReasoningContent string `json:"reasoning_content"`
	// Reasoning is the field name used by OpenRouter and some gateways.
	Reasoning string         `json:"reasoning"`
	ToolCalls []chatToolCall `json:"tool_calls"`
}

type chatUsage struct {
	PromptTokens            int                     `json:"prompt_tokens"`
	CompletionTokens        int                     `json:"completion_tokens"`
	TotalTokens             int                     `json:"total_tokens"`
	PromptTokensDetails     *chatPromptTokenDetails `json:"prompt_tokens_details"`
	CompletionTokensDetails *chatCompletionDetails  `json:"completion_tokens_details"`
}

type chatPromptTokenDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type chatCompletionDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

type chatError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
	Param   any    `json:"param"`
}

type chatModelList struct {
	Data []chatModel `json:"data"`
}

type chatModel struct {
	ID      string `json:"id"`
	OwnedBy string `json:"owned_by"`
	Created int64  `json:"created"`
}
