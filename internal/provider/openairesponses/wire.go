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

package openairesponses

import (
	"strings"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/llm"
	"github.com/pawagents/pawagents/internal/provider"
)

// Wire types for the OpenAI Responses API.
//
// The protocol differs from Chat Completions in three ways that shape this
// file: the system prompt is an "instructions" field, the conversation is a
// flat list of typed input items rather than messages, and a tool call is its
// own item instead of a field of an assistant message.

// responsesRequest is the body of POST /v1/responses.
type responsesRequest struct {
	Model        string `json:"model"`
	Instructions string `json:"instructions,omitempty"`
	Input        []any  `json:"input"`

	Tools      []wireTool `json:"tools,omitempty"`
	ToolChoice any        `json:"tool_choice,omitempty"`

	MaxOutputTokens int      `json:"max_output_tokens,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"top_p,omitempty"`
	Stream          bool     `json:"stream,omitempty"`

	// Store is always false: PawAgents is a read-only advisor, and persisting
	// a conversation in the vendor's account would be a side effect the user
	// never asked for. The session file is the record.
	Store bool `json:"store"`

	Text      *wireText      `json:"text,omitempty"`
	Reasoning *wireReasoning `json:"reasoning,omitempty"`

	// Extra carries the provider extra_body and the per-request extras. It is
	// merged into the encoded object instead of being a JSON field.
	Extra map[string]any `json:"-"`
}

// inputMessage is one turn written by the user or the model.
type inputMessage struct {
	Type    string         `json:"type"`
	Role    string         `json:"role"`
	Content []inputContent `json:"content"`
}

// inputContent is a text or image part of an input message.
type inputContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// functionCallItem is a tool call the model made in an earlier turn.
type functionCallItem struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// functionCallOutputItem is the result of a tool call, sent back as its own
// item.
type functionCallOutputItem struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

// wireTool declares a tool. Unlike Chat Completions the function fields are not
// nested inside a "function" object.
type wireTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Strict      bool           `json:"strict,omitempty"`
}

// wireText carries the output format.
type wireText struct {
	Format *wireFormat `json:"format,omitempty"`
}

// wireFormat asks for a schema constrained answer.
type wireFormat struct {
	Type   string         `json:"type"`
	Name   string         `json:"name,omitempty"`
	Schema map[string]any `json:"schema,omitempty"`
	Strict bool           `json:"strict,omitempty"`
}

// wireReasoning configures the reasoning effort and summary.
type wireReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// responsesResponse is a complete answer.
type responsesResponse struct {
	ID                string       `json:"id"`
	Object            string       `json:"object"`
	Status            string       `json:"status"`
	Model             string       `json:"model"`
	Output            []outputItem `json:"output"`
	Usage             *wireUsage   `json:"usage"`
	Error             *wireError   `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}

// outputItem is one item the model produced: a message, a function call or a
// reasoning trace.
type outputItem struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Status  string `json:"status"`
	Content []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
	Summary []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// wireUsage is the token accounting of a request.
type wireUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

// wireError is the error object of a failed response.
type wireError struct {
	Code    string `json:"code"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

// modelListing is the answer of GET /v1/models.
type modelListing struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// toUsage converts the provider token accounting.
func toUsage(usage *wireUsage) llm.Usage {
	if usage == nil {
		return llm.Usage{}
	}

	converted := llm.Usage{
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
		TotalTokens:  usage.TotalTokens,
	}
	if usage.InputTokensDetails != nil {
		converted.CacheReadTokens = usage.InputTokensDetails.CachedTokens
	}
	if usage.OutputTokensDetails != nil {
		converted.ReasoningTokens = usage.OutputTokensDetails.ReasoningTokens
	}
	if converted.TotalTokens == 0 {
		converted.TotalTokens = converted.InputTokens + converted.OutputTokens
	}
	return converted
}

// splitInstructions separates the system prompt from the conversation.
func splitInstructions(messages []llm.Message) (string, []llm.Message) {
	var instructions []string
	conversation := make([]llm.Message, 0, len(messages))

	for _, message := range messages {
		if message.Role == llm.RoleSystem {
			if text := strings.TrimSpace(message.Text()); text != "" {
				instructions = append(instructions, text)
			}
			continue
		}
		conversation = append(conversation, message)
	}

	return strings.Join(instructions, "\n\n"), conversation
}

// toInputItems translates the conversation into input items.
//
// The list is flat: a message is one item, a tool call of an earlier turn is a
// function_call item, and its result is a function_call_output item. Reasoning
// traces are not replayed, because the API rejects a summary whose opaque
// identifier the runtime does not keep.
func toInputItems(conversation []llm.Message) ([]any, error) {
	items := make([]any, 0, len(conversation))

	for _, message := range conversation {
		switch message.Role {
		case llm.RoleUser, llm.RoleAssistant:
			role := "user"
			if message.Role == llm.RoleAssistant {
				role = "assistant"
			}

			content, err := toInputContent(message, role)
			if err != nil {
				return nil, err
			}

			// Tool calls are separate items, so an assistant turn that
			// explains itself and then calls a tool becomes two items.
			if len(content) > 0 {
				items = append(items, inputMessage{
					Type:    "message",
					Role:    role,
					Content: content,
				})
			}

			for _, call := range message.ToolCalls {
				arguments := strings.TrimSpace(call.Arguments)
				if arguments == "" {
					arguments = "{}"
				}
				items = append(items, functionCallItem{
					Type:      "function_call",
					CallID:    call.ID,
					Name:      call.Name,
					Arguments: arguments,
				})
			}

		case llm.RoleTool:
			item, err := toFunctionCallOutput(message)
			if err != nil {
				return nil, err
			}
			items = append(items, item)

		default:
			return nil, apperrors.New(apperrors.KindInvalidArgument, "provider.openairesponses",
				"the Responses API cannot carry a %q message", message.Role)
		}
	}

	if len(items) == 0 {
		return nil, apperrors.New(apperrors.KindInvalidArgument, "provider.openairesponses",
			"the conversation carries nothing to send")
	}
	return items, nil
}

// toInputContent translates the visible parts of a message.
func toInputContent(message llm.Message, role string) ([]inputContent, error) {
	textType := "input_text"
	if role == "assistant" {
		// The API distinguishes text the model produced from text it received.
		textType = "output_text"
	}

	content := make([]inputContent, 0, len(message.Content))
	for _, part := range message.Content {
		switch part.Kind {
		case llm.ContentText:
			if strings.TrimSpace(part.Text) == "" {
				continue
			}
			content = append(content, inputContent{Type: textType, Text: part.Text})
		case llm.ContentThinking:
			// Not replayed: see toInputItems.
		case llm.ContentImage:
			if part.Image == nil {
				continue
			}
			if err := part.Image.Validate(); err != nil {
				return nil, err
			}
			url := part.Image.URL
			if len(part.Image.Data) > 0 {
				url = provider.DataURL(part.Image.MIMEType, part.Image.Data)
			}
			content = append(content, inputContent{
				Type:     "input_image",
				ImageURL: url,
				Detail:   part.Image.Detail,
			})
		default:
			return nil, apperrors.New(apperrors.KindInvalidArgument, "provider.openairesponses",
				"unsupported content kind %q", part.Kind)
		}
	}
	return content, nil
}

// toFunctionCallOutput translates a tool result into its own item.
func toFunctionCallOutput(message llm.Message) (functionCallOutputItem, error) {
	result := message.ToolResult
	if result == nil {
		return functionCallOutputItem{}, apperrors.New(apperrors.KindInvalidArgument,
			"provider.openairesponses", "a tool message must carry a tool result")
	}
	if strings.TrimSpace(result.ToolCallID) == "" {
		return functionCallOutputItem{}, apperrors.New(apperrors.KindInvalidArgument,
			"provider.openairesponses", "a tool result must reference the tool call it answers")
	}

	output := result.Content
	if strings.TrimSpace(output) == "" {
		output = "(the tool returned no output)"
	}
	if result.IsError {
		// The API has no error flag, so the failure has to be visible in the
		// text the model reads.
		output = "[tool error] " + output
	}

	return functionCallOutputItem{
		Type:   "function_call_output",
		CallID: result.ToolCallID,
		Output: output,
	}, nil
}

// toWireTools translates the tool definitions.
func toWireTools(definitions []llm.ToolDefinition) []wireTool {
	out := make([]wireTool, 0, len(definitions))
	for _, definition := range definitions {
		out = append(out, wireTool{
			Type:        "function",
			Name:        definition.Name,
			Description: definition.Description,
			Parameters:  map[string]any(definition.InputSchema),
		})
	}
	return out
}

// toToolChoice translates the tool selection policy.
//
// The API accepts the strings "auto", "none" and "required", or an object that
// names one function.
func toToolChoice(choice llm.ToolChoice) any {
	switch choice.Mode {
	case llm.ToolChoiceNone:
		return "none"
	case llm.ToolChoiceRequired:
		return "required"
	case llm.ToolChoiceFunction:
		return map[string]any{"type": "function", "name": choice.Name}
	case llm.ToolChoiceAuto, "":
		return "auto"
	default:
		return "auto"
	}
}

// textFormat builds the schema constrained output format.
//
// Strict mode is deliberately off: the API only accepts a strict schema that
// marks every property as required and forbids additional properties, which the
// finding envelope does not. Asking for a non strict schema still constrains
// the shape, and refusing to work with a schema we cannot mark strict would
// remove structured output for every caller.
func textFormat(name string, schema llm.JSONSchema) *wireText {
	return &wireText{Format: &wireFormat{
		Type:   "json_schema",
		Name:   name,
		Schema: map[string]any(schema),
		Strict: false,
	}}
}

// mapIncompleteReason translates the reason a response was cut short.
func mapIncompleteReason(reason string) llm.FinishReason {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "max_output_tokens":
		return llm.FinishReasonLength
	case "content_filter":
		return llm.FinishReasonContentFilter
	default:
		return llm.FinishReasonLength
	}
}

// normaliseArguments returns a tool argument string. The API sends an
// incomplete response as null when a call was cut off.
func normaliseArguments(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "null" {
		return "{}"
	}
	return trimmed
}
