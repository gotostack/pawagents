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

package anthropic

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/llm"
)

// Wire types for the Anthropic Messages API.
//
// Content is a list of typed blocks rather than a string, tools are declared
// with an input schema instead of a function object, and the system prompt is a
// top level field instead of a message. The translation below is the only place
// that knows any of this.

// messagesRequest is the body of POST /v1/messages.
type messagesRequest struct {
	Model     string        `json:"model"`
	System    string        `json:"system,omitempty"`
	Messages  []wireMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens"`

	Tools         []wireTool      `json:"tools,omitempty"`
	ToolChoice    *wireToolChoice `json:"tool_choice,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
	Metadata      map[string]any  `json:"metadata,omitempty"`

	// Extra carries the provider extra_body and the per-request extras. It is
	// merged into the encoded object instead of being a JSON field, so a user
	// can reach any vendor specific knob without a code change.
	Extra map[string]any `json:"-"`
}

// wireMessage is one turn of the conversation.
type wireMessage struct {
	Role    string      `json:"role"`
	Content []wireBlock `json:"content"`
}

// wireBlock is one content block. A single struct covers every block type the
// runtime uses; the fields that do not apply are omitted.
type wireBlock struct {
	Type string `json:"type"`

	// Text is set for text blocks.
	Text string `json:"text,omitempty"`
	// Thinking is set for extended thinking blocks.
	Thinking string `json:"thinking,omitempty"`
	// Source is set for image blocks.
	Source *wireImageSource `json:"source,omitempty"`

	// ID, Name and Input describe a tool_use block.
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// ToolUseID, Content and IsError describe a tool_result block, which is
	// sent back inside a user message.
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

// wireImageSource is an image payload, either inline or referenced.
type wireImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

// wireTool declares a tool the model may call.
type wireTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

// wireToolChoice constrains tool usage.
type wireToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

// messagesResponse is a complete (non streaming) answer.
type messagesResponse struct {
	ID         string      `json:"id"`
	Type       string      `json:"type"`
	Role       string      `json:"role"`
	Model      string      `json:"model"`
	Content    []wireBlock `json:"content"`
	StopReason string      `json:"stop_reason"`
	Usage      *wireUsage  `json:"usage"`
	Error      *wireError  `json:"error"`
}

// wireUsage is the token accounting of a request.
type wireUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// wireError is the error object of a stream event.
type wireError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// modelListing is the answer of GET /v1/models.
type modelListing struct {
	Data []struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"data"`
	HasMore bool `json:"has_more"`
}

// toUsage converts the provider token accounting.
func toUsage(usage *wireUsage) llm.Usage {
	if usage == nil {
		return llm.Usage{}
	}

	converted := llm.Usage{
		InputTokens:      usage.InputTokens,
		OutputTokens:     usage.OutputTokens,
		CacheReadTokens:  usage.CacheReadInputTokens,
		CacheWriteTokens: usage.CacheCreationInputTokens,
	}
	converted.TotalTokens = converted.InputTokens + converted.OutputTokens
	return converted
}

// splitSystem separates the system prompt from the conversation.
//
// The Messages API takes the system prompt as a top level field, so it cannot
// stay a message. Several system messages are joined because that is what the
// provider does internally with its own block form.
func splitSystem(messages []llm.Message) (string, []llm.Message) {
	var system []string
	conversation := make([]llm.Message, 0, len(messages))

	for _, message := range messages {
		if message.Role == llm.RoleSystem {
			if text := strings.TrimSpace(message.Text()); text != "" {
				system = append(system, text)
			}
			continue
		}
		conversation = append(conversation, message)
	}

	return strings.Join(system, "\n\n"), conversation
}

// toWireMessages translates the conversation.
//
// Two rules of the API shape the result:
//
//   - a tool result is not a message of its own but a tool_result block inside
//     a user message, and several tool results answering one assistant turn
//     belong to the same user message, so consecutive results are buffered and
//     flushed together;
//   - a reasoning trace is not replayed, because an unsigned thinking block
//     cannot be sent back and inventing a signature would be worse than
//     dropping it.
func toWireMessages(conversation []llm.Message) ([]wireMessage, error) {
	out := make([]wireMessage, 0, len(conversation))
	var results []wireBlock

	flushResults := func() {
		if len(results) == 0 {
			return
		}
		out = append(out, wireMessage{Role: "user", Content: results})
		results = nil
	}

	for _, message := range conversation {
		switch message.Role {
		case llm.RoleTool:
			block, err := toToolResultBlock(message)
			if err != nil {
				return nil, err
			}
			if block != nil {
				results = append(results, *block)
			}
			continue
		case llm.RoleUser, llm.RoleAssistant:
			flushResults()
		default:
			return nil, apperrors.New(apperrors.KindInvalidArgument, "provider.anthropic",
				"the Messages API cannot carry a %q message", message.Role)
		}

		blocks, err := toContentBlocks(message)
		if err != nil {
			return nil, err
		}
		if len(blocks) == 0 {
			// An empty content list is rejected by the API, so a message that
			// carries nothing to say is dropped instead.
			continue
		}

		role := "user"
		if message.Role == llm.RoleAssistant {
			role = "assistant"
		}
		out = append(out, wireMessage{Role: role, Content: blocks})
	}

	flushResults()

	if len(out) == 0 {
		return nil, apperrors.New(apperrors.KindInvalidArgument, "provider.anthropic",
			"the conversation carries nothing to send")
	}
	return out, nil
}

// toContentBlocks translates one message into content blocks.
func toContentBlocks(message llm.Message) ([]wireBlock, error) {
	blocks := make([]wireBlock, 0, len(message.Content)+len(message.ToolCalls))

	for _, part := range message.Content {
		switch part.Kind {
		case llm.ContentText:
			if strings.TrimSpace(part.Text) == "" {
				continue
			}
			blocks = append(blocks, wireBlock{Type: "text", Text: part.Text})
		case llm.ContentThinking:
			// Not replayed: see toWireMessages.
		case llm.ContentImage:
			if part.Image == nil {
				continue
			}
			block, err := toImageBlock(part.Image)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		default:
			return nil, apperrors.New(apperrors.KindInvalidArgument, "provider.anthropic",
				"unsupported content kind %q", part.Kind)
		}
	}

	for _, call := range message.ToolCalls {
		arguments := strings.TrimSpace(call.Arguments)
		if arguments == "" {
			arguments = "{}"
		}
		blocks = append(blocks, wireBlock{
			Type:  "tool_use",
			ID:    call.ID,
			Name:  call.Name,
			Input: json.RawMessage(arguments),
		})
	}

	return blocks, nil
}

// toToolResultBlock translates a tool result message into one block.
func toToolResultBlock(message llm.Message) (*wireBlock, error) {
	result := message.ToolResult
	if result == nil {
		return nil, apperrors.New(apperrors.KindInvalidArgument, "provider.anthropic",
			"a tool message must carry a tool result")
	}
	if strings.TrimSpace(result.ToolCallID) == "" {
		return nil, apperrors.New(apperrors.KindInvalidArgument, "provider.anthropic",
			"a tool result must reference the tool call it answers")
	}

	content := result.Content
	if strings.TrimSpace(content) == "" {
		content = "(the tool returned no output)"
	}

	return &wireBlock{
		Type:      "tool_result",
		ToolUseID: result.ToolCallID,
		Content:   content,
		IsError:   result.IsError,
	}, nil
}

// toImageBlock translates an image part.
func toImageBlock(image *llm.ImagePart) (wireBlock, error) {
	if err := image.Validate(); err != nil {
		return wireBlock{}, err
	}

	if len(image.Data) > 0 {
		return wireBlock{
			Type: "image",
			Source: &wireImageSource{
				Type:      "base64",
				MediaType: image.MIMEType,
				Data:      base64.StdEncoding.EncodeToString(image.Data),
			},
		}, nil
	}

	return wireBlock{
		Type:   "image",
		Source: &wireImageSource{Type: "url", URL: image.URL},
	}, nil
}

// toWireTools translates the tool definitions.
func toWireTools(definitions []llm.ToolDefinition) []wireTool {
	out := make([]wireTool, 0, len(definitions))
	for _, definition := range definitions {
		out = append(out, wireTool{
			Name:        definition.Name,
			Description: definition.Description,
			InputSchema: map[string]any(definition.InputSchema),
		})
	}
	return out
}

// toToolChoice translates the tool selection policy.
//
// The API has no "none": the way to forbid tool use is to send no tools at all,
// which is why the caller drops the tool list in that case.
func toToolChoice(choice llm.ToolChoice) *wireToolChoice {
	switch choice.Mode {
	case llm.ToolChoiceAuto, "":
		return &wireToolChoice{Type: "auto"}
	case llm.ToolChoiceRequired:
		return &wireToolChoice{Type: "any"}
	case llm.ToolChoiceFunction:
		return &wireToolChoice{Type: "tool", Name: choice.Name}
	default:
		return &wireToolChoice{Type: "auto"}
	}
}

// mapStopReason translates the provider stop reason.
//
// An unknown reason maps to "stop": Anthropic adds reasons over time, and
// treating a new one as a protocol failure would break a successful generation.
func mapStopReason(reason string) llm.FinishReason {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "end_turn", "stop_sequence", "pause_turn", "":
		return llm.FinishReasonStop
	case "max_tokens":
		return llm.FinishReasonLength
	case "tool_use":
		return llm.FinishReasonToolCalls
	case "refusal":
		return llm.FinishReasonContentFilter
	case "error":
		return llm.FinishReasonError
	default:
		return llm.FinishReasonStop
	}
}
