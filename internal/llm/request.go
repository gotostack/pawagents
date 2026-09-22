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
	"strings"

	"github.com/pawagents/pawagents/internal/apperrors"
)

// MaxTemperature is the highest sampling temperature accepted by validation.
const MaxTemperature = 2.0

// GenerateRequest is the vendor neutral unit of work handed to a provider.
//
// Providers translate it into their own request shape. Anything a provider
// supports but the protocol does not model goes through Extra, which is merged
// into the request body verbatim.
type GenerateRequest struct {
	// Model is the concrete model name, for example "qwen3-coder". The alias
	// has already been resolved by the router.
	Model string
	// Messages is the conversation, including system messages.
	Messages []Message
	// Tools are the tools the model may call. An empty list means the model
	// answers from the conversation alone.
	Tools []ToolDefinition
	// ToolChoice constrains tool usage. The zero value means auto.
	ToolChoice ToolChoice
	// MaxTokens bounds the completion. Zero means the provider default.
	MaxTokens int
	// Temperature overrides the provider default when set.
	Temperature *float64
	// TopP is nucleus sampling, used by providers that support it.
	TopP *float64
	// Stop lists stop sequences.
	Stop []string
	// ResponseSchema asks for structured output. Providers that cannot honour
	// it must fail loudly with a CapabilityError rather than silently return
	// free text.
	ResponseSchema *JSONSchema
	// ResponseSchemaName labels the schema for providers that require a name.
	ResponseSchemaName string
	// Extra is merged into the provider request body.
	Extra map[string]any
}

// Validate checks the request is well formed before it reaches a provider.
//
// Validation happens here, once, instead of in every adapter: a provider then
// only has to translate, not to police its input.
func (r *GenerateRequest) Validate() error {
	if r == nil {
		return apperrors.New(apperrors.KindInvalidArgument, "llm.request",
			"the generation request is nil")
	}

	var problems []error

	if strings.TrimSpace(r.Model) == "" {
		problems = append(problems, apperrors.New(apperrors.KindInvalidArgument, "llm.request",
			"model is required"))
	}
	if err := ValidateConversation(r.Messages); err != nil {
		problems = append(problems, err)
	}
	if err := ValidateToolDefinitions(r.Tools); err != nil {
		problems = append(problems, err)
	}
	if err := r.ToolChoice.Validate(r.Tools); err != nil {
		problems = append(problems, err)
	}
	if r.MaxTokens < 0 {
		problems = append(problems, apperrors.New(apperrors.KindInvalidArgument, "llm.request",
			"max_tokens must not be negative"))
	}
	if r.Temperature != nil && (*r.Temperature < 0 || *r.Temperature > MaxTemperature) {
		problems = append(problems, apperrors.New(apperrors.KindInvalidArgument, "llm.request",
			"temperature %.2f is outside the allowed range [0, %.1f]", *r.Temperature, MaxTemperature))
	}
	if r.TopP != nil && (*r.TopP <= 0 || *r.TopP > 1) {
		problems = append(problems, apperrors.New(apperrors.KindInvalidArgument, "llm.request",
			"top_p %.2f is outside the allowed range (0, 1]", *r.TopP))
	}
	for index, stop := range r.Stop {
		if stop == "" {
			problems = append(problems, apperrors.New(apperrors.KindInvalidArgument, "llm.request",
				"stop[%d] is empty", index))
		}
	}
	if r.ResponseSchema != nil {
		if err := r.ResponseSchema.Validate(); err != nil {
			problems = append(problems, err)
		}
		if strings.TrimSpace(r.ResponseSchemaName) == "" {
			problems = append(problems, apperrors.New(apperrors.KindInvalidArgument, "llm.request",
				"response_schema_name is required when a response schema is set"))
		}
	}

	return apperrors.Multi(apperrors.KindInvalidArgument, "llm.request",
		"invalid generation request", problems...)
}

// Clone returns a deep copy so that the context manager can derive a
// compacted request without mutating the original.
func (r *GenerateRequest) Clone() *GenerateRequest {
	if r == nil {
		return nil
	}
	clone := *r

	clone.Messages = make([]Message, 0, len(r.Messages))
	for _, message := range r.Messages {
		clone.Messages = append(clone.Messages, message.Clone())
	}
	clone.Tools = append([]ToolDefinition(nil), r.Tools...)
	clone.Stop = append([]string(nil), r.Stop...)
	if r.Temperature != nil {
		value := *r.Temperature
		clone.Temperature = &value
	}
	if r.TopP != nil {
		value := *r.TopP
		clone.TopP = &value
	}
	if r.ResponseSchema != nil {
		schema := r.ResponseSchema.Clone()
		clone.ResponseSchema = &schema
	}
	if r.Extra != nil {
		clone.Extra = cloneJSONValue(r.Extra).(map[string]any)
	}
	return &clone
}

// WantsTools reports whether the request offers tools to the model.
func (r *GenerateRequest) WantsTools() bool { return r != nil && len(r.Tools) > 0 }

// SplitSystem separates the leading system messages from the rest of the
// conversation.
//
// Providers disagree on where the system prompt goes: some take a dedicated
// field, some accept it as a message. Adapters use this helper so the decision
// stays in one place.
func SplitSystem(messages []Message) (string, []Message) {
	var system []string
	rest := make([]Message, 0, len(messages))

	for _, message := range messages {
		if message.Role == RoleSystem {
			if text := message.Text(); text != "" {
				system = append(system, text)
			}
			continue
		}
		rest = append(rest, message)
	}

	return strings.Join(system, "\n\n"), rest
}

// LastAssistantMessage returns the most recent assistant message, which is
// what the agent loop inspects for tool calls.
func LastAssistantMessage(messages []Message) (Message, bool) {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == RoleAssistant {
			return messages[index], true
		}
	}
	return Message{}, false
}

// PendingToolCalls returns the tool calls of the last assistant message when no
// tool result has been produced for them yet. It is used by the agent loop to
// decide whether it must execute tools before calling the model again.
func PendingToolCalls(messages []Message) []ToolCall {
	assistant, ok := LastAssistantMessage(messages)
	if !ok || !assistant.HasToolCalls() {
		return nil
	}

	answered := make(map[string]bool, len(messages))
	for _, message := range messages {
		if message.ToolResult != nil {
			answered[message.ToolResult.ToolCallID] = true
		}
	}

	pending := make([]ToolCall, 0, len(assistant.ToolCalls))
	for _, call := range assistant.ToolCalls {
		if call.ID != "" && answered[call.ID] {
			continue
		}
		pending = append(pending, call)
	}
	if len(pending) == 0 {
		return nil
	}
	return pending
}
