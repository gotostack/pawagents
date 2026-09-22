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
	"fmt"
	"strings"

	"github.com/pawagents/pawagents/internal/apperrors"
)

// ModelCapabilities describes what a concrete model can do.
//
// Capabilities are never assumed. Local models vary enormously — the same
// Ollama server can host a model with tool calling and one without — so the
// agent loop validates the requirement list before the first provider call and
// refuses to run instead of degrading silently.
type ModelCapabilities struct {
	// ToolCalling reports native function calling support.
	ToolCalling bool `json:"tool_calling"`
	// ParallelTools reports support for several tool calls in one turn.
	ParallelTools bool `json:"parallel_tools"`
	// Streaming reports incremental output.
	Streaming bool `json:"streaming"`
	// StructuredOutput reports a JSON schema constrained response mode.
	StructuredOutput bool `json:"structured_output"`
	// Vision reports image input.
	Vision bool `json:"vision"`
	// Reasoning reports a separate reasoning channel.
	Reasoning bool `json:"reasoning"`
	// SystemMessage reports whether a system role is accepted. Providers such
	// as some Ollama templates require the system prompt to be folded into the
	// first user message instead.
	SystemMessage bool `json:"system_message"`
	// MaxContextTokens is the usable context window.
	MaxContextTokens int `json:"max_context_tokens"`
	// MaxOutputTokens is the largest completion the model accepts.
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`
}

// Requirements is the capability set a task needs.
type Requirements struct {
	// Tools is set when the agent granted at least one tool.
	Tools bool
	// ParallelTools is set when the agent may call several tools at once.
	ParallelTools bool
	// Streaming is set when the caller cannot wait for the full answer.
	Streaming bool
	// StructuredOutput is set when the agent requested a structured result.
	StructuredOutput bool
	// Vision is set when the conversation contains images.
	Vision bool
	// SystemMessage is set when the agent profile has instructions.
	SystemMessage bool
	// MinContextTokens is the smallest usable context window.
	MinContextTokens int
	// MaxOutputTokens is the largest completion the request needs.
	MaxOutputTokens int
}

// RequirementsFrom derives the requirements of a generation request.
//
// ParallelTools is deliberately left unset: a model that calls tools one at a
// time still completes the task, so parallel support is a scheduling
// preference rather than a hard requirement. The orchestrator sets it when it
// explicitly wants concurrent tool execution.
func RequirementsFrom(request *GenerateRequest) Requirements {
	if request == nil {
		return Requirements{}
	}
	requirements := Requirements{
		Tools:            len(request.Tools) > 0,
		StructuredOutput: request.ResponseSchema != nil,
		MaxOutputTokens:  request.MaxTokens,
	}
	for _, message := range request.Messages {
		if message.Role == RoleSystem {
			requirements.SystemMessage = true
		}
		for _, part := range message.Content {
			if part.Kind == ContentImage {
				requirements.Vision = true
			}
		}
	}
	return requirements
}

// Missing returns the human-readable names of the capabilities a requirement
// asks for and the model does not provide.
func (c ModelCapabilities) Missing(required Requirements) []string {
	var missing []string

	if required.Tools && !c.ToolCalling {
		missing = append(missing, "tool_calling")
	}
	if required.ParallelTools && !c.ParallelTools {
		missing = append(missing, "parallel_tools")
	}
	if required.Streaming && !c.Streaming {
		missing = append(missing, "streaming")
	}
	if required.StructuredOutput && !c.StructuredOutput {
		missing = append(missing, "structured_output")
	}
	if required.Vision && !c.Vision {
		missing = append(missing, "vision")
	}
	if required.SystemMessage && !c.SystemMessage {
		missing = append(missing, "system_message")
	}
	if required.MinContextTokens > 0 && c.MaxContextTokens > 0 && c.MaxContextTokens < required.MinContextTokens {
		missing = append(missing, fmt.Sprintf("max_context_tokens>=%d", required.MinContextTokens))
	}
	if required.MaxOutputTokens > 0 && c.MaxOutputTokens > 0 && c.MaxOutputTokens < required.MaxOutputTokens {
		missing = append(missing, fmt.Sprintf("max_output_tokens>=%d", required.MaxOutputTokens))
	}

	return missing
}

// Supports reports whether every requirement is satisfied.
func (c ModelCapabilities) Supports(required Requirements) bool {
	return len(c.Missing(required)) == 0
}

// Validate returns a CapabilityError naming every unmet requirement.
//
// The message is built for a human who has to fix it, so it names the model,
// the capabilities that are missing and the configuration knob that usually
// resolves the problem.
func (c ModelCapabilities) Validate(required Requirements, model string) error {
	missing := c.Missing(required)
	if len(missing) == 0 {
		return nil
	}

	label := model
	if strings.TrimSpace(label) == "" {
		label = "the selected model"
	}

	details := "the model does not support: " + strings.Join(missing, ", ")
	if containsString(missing, "tool_calling") {
		details += "; grant the agent tools the model supports, or pick a model with tool calling"
	}
	if containsString(missing, "structured_output") {
		details += "; set output_mode: text for this agent, or pick a model with structured output"
	}

	return apperrors.New(apperrors.KindCapability, "llm.capabilities",
		"%s cannot run this task: %s", label, details).
		WithDetails(map[string]any{
			"model":   label,
			"missing": missing,
		})
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
