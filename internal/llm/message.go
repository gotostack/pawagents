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

package llm

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pawagents/pawagents/internal/apperrors"
)

// Role identifies who produced a message.
type Role string

// Message roles understood by every provider adapter.
const (
	// RoleSystem carries the agent profile instructions.
	RoleSystem Role = "system"
	// RoleUser carries the delegated task and tool results the model asked for.
	RoleUser Role = "user"
	// RoleAssistant carries model output, including requested tool calls.
	RoleAssistant Role = "assistant"
	// RoleTool carries the result of a tool call.
	RoleTool Role = "tool"
)

// Roles returns every valid role.
func Roles() []Role { return []Role{RoleSystem, RoleUser, RoleAssistant, RoleTool} }

// Valid reports whether the role is one of the known roles.
func (r Role) Valid() bool {
	switch r {
	case RoleSystem, RoleUser, RoleAssistant, RoleTool:
		return true
	default:
		return false
	}
}

// ContentKind distinguishes the parts a message can be made of.
type ContentKind string

// Content kinds.
const (
	// ContentText is plain text.
	ContentText ContentKind = "text"
	// ContentImage is an inline or referenced image.
	ContentImage ContentKind = "image"
	// ContentThinking is a reasoning trace some providers expose separately
	// from the answer. It is never sent back to a provider as user content.
	ContentThinking ContentKind = "thinking"
)

// ImagePart describes an image attached to a message.
type ImagePart struct {
	// MIMEType is required for inline data, for example "image/png".
	MIMEType string
	// Data holds inline bytes. Mutually exclusive with URL.
	Data []byte
	// URL references an image the provider must fetch.
	URL string
	// Detail is an optional provider hint: "low", "high" or "auto".
	Detail string
}

// Validate checks that the image is addressable.
func (p ImagePart) Validate() error {
	switch {
	case len(p.Data) == 0 && p.URL == "":
		return apperrors.New(apperrors.KindInvalidArgument, "llm.image",
			"image part needs either inline data or a URL")
	case len(p.Data) > 0 && p.URL != "":
		return apperrors.New(apperrors.KindInvalidArgument, "llm.image",
			"image part cannot have both inline data and a URL")
	case len(p.Data) > 0 && strings.TrimSpace(p.MIMEType) == "":
		return apperrors.New(apperrors.KindInvalidArgument, "llm.image",
			"inline image data requires a MIME type")
	default:
		return nil
	}
}

// ContentPart is one piece of a message.
type ContentPart struct {
	Kind  ContentKind
	Text  string
	Image *ImagePart
}

// TextPart builds a text content part.
func TextPart(text string) ContentPart {
	return ContentPart{Kind: ContentText, Text: text}
}

// ThinkingPart builds a reasoning content part.
func ThinkingPart(text string) ContentPart {
	return ContentPart{Kind: ContentThinking, Text: text}
}

// ImageDataPart builds an image part from inline bytes.
func ImageDataPart(mimeType string, data []byte) ContentPart {
	return ContentPart{Kind: ContentImage, Image: &ImagePart{MIMEType: mimeType, Data: data}}
}

// ImageURLPart builds an image part from a URL.
func ImageURLPart(url string) ContentPart {
	return ContentPart{Kind: ContentImage, Image: &ImagePart{URL: url}}
}

// Validate checks the part is well formed.
func (p ContentPart) Validate() error {
	switch p.Kind {
	case ContentText, ContentThinking:
		return nil
	case ContentImage:
		if p.Image == nil {
			return apperrors.New(apperrors.KindInvalidArgument, "llm.content",
				"image content part is missing the image payload")
		}
		return p.Image.Validate()
	case "":
		return apperrors.New(apperrors.KindInvalidArgument, "llm.content",
			"content part is missing its kind")
	default:
		return apperrors.New(apperrors.KindInvalidArgument, "llm.content",
			"unsupported content kind %q", p.Kind)
	}
}

// ToolCall is a model request to invoke one tool.
type ToolCall struct {
	// ID is the provider supplied identifier. Tool results reference it.
	ID string
	// Name is the tool name, for example "repo.search".
	Name string
	// Arguments is the raw JSON object produced by the model. Parsing is
	// deferred to the tool runtime so that a malformed argument blob is
	// reported as a tool error instead of aborting the request.
	Arguments string
}

// NewToolCall builds a tool call from a value that will be marshalled to JSON.
func NewToolCall(id, name string, arguments any) (ToolCall, error) {
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return ToolCall{}, apperrors.Wrap(apperrors.KindInvalidArgument, "llm.toolcall",
			"cannot encode the arguments of tool call %q", err, name)
	}
	return ToolCall{ID: id, Name: name, Arguments: string(encoded)}, nil
}

// IsZero reports whether the call is unset.
func (c ToolCall) IsZero() bool {
	return c.ID == "" && c.Name == "" && c.Arguments == ""
}

// ArgumentsJSON decodes Arguments into v. An empty argument string decodes as
// an empty object so that a no-argument tool keeps working.
func (c ToolCall) ArgumentsJSON(v any) error {
	raw := strings.TrimSpace(c.Arguments)
	if raw == "" {
		raw = "{}"
	}
	if err := json.Unmarshal([]byte(raw), v); err != nil {
		return apperrors.Wrap(apperrors.KindTool, "llm.toolcall",
			"tool call %q has invalid JSON arguments", err, c.Name)
	}
	return nil
}

// Validate checks that the call can be dispatched and its result matched.
func (c ToolCall) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return apperrors.New(apperrors.KindInvalidArgument, "llm.toolcall",
			"tool call is missing the tool name")
	}
	raw := strings.TrimSpace(c.Arguments)
	if raw == "" {
		return nil
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return apperrors.Wrap(apperrors.KindInvalidArgument, "llm.toolcall",
			"tool call %q arguments are not a JSON object", err, c.Name)
	}
	return nil
}

// ToolResult is the outcome of a ToolCall, sent back to the model.
type ToolResult struct {
	// ToolCallID must match the ToolCall.ID it answers.
	ToolCallID string
	// Name is the tool name, kept for providers that require it.
	Name string
	// Content is the textual payload given to the model.
	Content string
	// IsError marks a failed call. The model is told the tool failed instead
	// of being handed a plausible looking success.
	IsError bool
	// Truncated marks a result the runtime cut to fit the output budget. The
	// model sees the truncation notice inside Content; this flag exists so
	// that telemetry and the session record can report it precisely.
	Truncated bool
	// Metadata carries structured extras such as truncation flags. It is not
	// sent to the model.
	Metadata map[string]any
}

// Validate checks that the result can be matched to a call.
func (r ToolResult) Validate() error {
	if strings.TrimSpace(r.ToolCallID) == "" {
		return apperrors.New(apperrors.KindInvalidArgument, "llm.toolresult",
			"tool result is missing the tool call id")
	}
	return nil
}

// Message is one turn of the conversation.
//
// A message carries content, tool calls, or a tool result; an assistant
// message may carry content and tool calls at the same time, which is what
// most providers emit when the model both explains and acts.
type Message struct {
	Role       Role
	Content    []ContentPart
	ToolCalls  []ToolCall
	ToolResult *ToolResult
	// Name is an optional author label used by a few providers for multi
	// participant conversations.
	Name string
}

// NewSystemMessage builds a system message.
func NewSystemMessage(text string) Message {
	return Message{Role: RoleSystem, Content: []ContentPart{TextPart(text)}}
}

// NewUserMessage builds a user message.
func NewUserMessage(text string) Message {
	return Message{Role: RoleUser, Content: []ContentPart{TextPart(text)}}
}

// NewAssistantMessage builds an assistant message with text content.
func NewAssistantMessage(text string) Message {
	return Message{Role: RoleAssistant, Content: []ContentPart{TextPart(text)}}
}

// NewAssistantToolCallMessage builds an assistant message that only requests
// tool calls.
func NewAssistantToolCallMessage(calls ...ToolCall) Message {
	return Message{Role: RoleAssistant, ToolCalls: calls}
}

// NewToolResultMessage builds the message that returns a tool result.
func NewToolResultMessage(result ToolResult) Message {
	clone := result
	return Message{Role: RoleTool, ToolResult: &clone}
}

// Text concatenates the visible text content parts. Reasoning traces, tool
// results and images are excluded: Text is what a user or a host agent reads,
// and a reasoning trace is not part of the answer.
func (m Message) Text() string {
	var b strings.Builder
	for _, part := range m.Content {
		if part.Kind == ContentText {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

// ReasoningText concatenates the reasoning content parts. Providers that
// expose a separate reasoning channel use it to keep the chain of thought out
// of the answer.
func (m Message) ReasoningText() string {
	var b strings.Builder
	for _, part := range m.Content {
		if part.Kind == ContentThinking {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

// HasToolCalls reports whether the message requests tool calls.
func (m Message) HasToolCalls() bool { return len(m.ToolCalls) > 0 }

// HasContent reports whether the message carries any content part.
func (m Message) HasContent() bool { return len(m.Content) > 0 }

// IsEmpty reports whether the message carries nothing at all.
func (m Message) IsEmpty() bool {
	return !m.HasContent() && !m.HasToolCalls() && m.ToolResult == nil
}

// Validate checks the message satisfies the invariants every provider relies
// on. It is called by the provider adapters before a request is sent, so that
// a malformed conversation surfaces as an InvalidArgumentError instead of a
// confusing vendor error.
func (m Message) Validate() error {
	var problems []error

	if !m.Role.Valid() {
		problems = append(problems, fmt.Errorf("unsupported role %q", m.Role))
	}
	for index, part := range m.Content {
		if err := part.Validate(); err != nil {
			problems = append(problems, fmt.Errorf("content[%d]: %w", index, err))
		}
	}
	for index, call := range m.ToolCalls {
		if err := call.Validate(); err != nil {
			problems = append(problems, fmt.Errorf("tool_calls[%d]: %w", index, err))
		}
	}
	if m.ToolResult != nil {
		if err := m.ToolResult.Validate(); err != nil {
			problems = append(problems, err)
		}
		if m.Role != RoleTool {
			problems = append(problems, fmt.Errorf(
				"a tool result must be carried by a %q message, not %q", RoleTool, m.Role))
		}
	}
	if m.Role == RoleTool && m.ToolResult == nil {
		problems = append(problems, fmt.Errorf("a %q message must carry a tool result", RoleTool))
	}
	if m.Role == RoleTool && m.HasToolCalls() {
		problems = append(problems, fmt.Errorf("a %q message must not request tool calls", RoleTool))
	}

	return apperrors.Multi(apperrors.KindInvalidArgument, "llm.message",
		"invalid message", problems...)
}

// Clone returns a deep copy so that a caller can append to a conversation
// without mutating shared history.
func (m Message) Clone() Message {
	clone := m
	clone.Content = append([]ContentPart(nil), m.Content...)
	for i := range clone.Content {
		if m.Content[i].Image == nil {
			continue
		}
		image := *m.Content[i].Image
		image.Data = append([]byte(nil), m.Content[i].Image.Data...)
		clone.Content[i].Image = &image
	}
	clone.ToolCalls = append([]ToolCall(nil), m.ToolCalls...)
	if m.ToolResult != nil {
		result := *m.ToolResult
		if m.ToolResult.Metadata != nil {
			result.Metadata = make(map[string]any, len(m.ToolResult.Metadata))
			for key, value := range m.ToolResult.Metadata {
				result.Metadata[key] = value
			}
		}
		clone.ToolResult = &result
	}
	return clone
}

// ValidateConversation checks a whole message list, which is what providers
// send. It reports the index of every offending message.
func ValidateConversation(messages []Message) error {
	if len(messages) == 0 {
		return apperrors.New(apperrors.KindInvalidArgument, "llm.conversation",
			"the conversation is empty")
	}
	var problems []error
	for index, message := range messages {
		if err := message.Validate(); err != nil {
			problems = append(problems, fmt.Errorf("messages[%d]: %w", index, err))
		}
	}
	return apperrors.Multi(apperrors.KindInvalidArgument, "llm.conversation",
		"invalid conversation", problems...)
}
