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
	"math"
	"strings"
	"testing"
)

func float64Ptr(value float64) *float64 { return &value }

func TestGenerateRequestValidate(t *testing.T) {
	base := func() *GenerateRequest {
		return &GenerateRequest{
			Model:    "qwen3-coder",
			Messages: []Message{NewSystemMessage("instructions"), NewUserMessage("task")},
		}
	}

	if err := base().Validate(); err != nil {
		t.Fatalf("a minimal request must be valid: %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(*GenerateRequest)
		wantErr string
	}{
		{
			name:    "missing model",
			mutate:  func(r *GenerateRequest) { r.Model = " " },
			wantErr: "model is required",
		},
		{
			name:    "empty conversation",
			mutate:  func(r *GenerateRequest) { r.Messages = nil },
			wantErr: "conversation is empty",
		},
		{
			name:    "invalid message",
			mutate:  func(r *GenerateRequest) { r.Messages = append(r.Messages, Message{Role: "developer"}) },
			wantErr: "unsupported role",
		},
		{
			name: "invalid tool",
			mutate: func(r *GenerateRequest) {
				r.Tools = []ToolDefinition{{Name: "read", Description: "x", InputSchema: ObjectSchema(nil)}}
			},
			wantErr: "must match",
		},
		{
			name: "tool choice without tools",
			mutate: func(r *GenerateRequest) {
				r.ToolChoice = ToolChoice{Mode: ToolChoiceRequired}
			},
			wantErr: "requires at least one tool",
		},
		{
			name:    "negative max tokens",
			mutate:  func(r *GenerateRequest) { r.MaxTokens = -1 },
			wantErr: "max_tokens must not be negative",
		},
		{
			name:    "temperature too high",
			mutate:  func(r *GenerateRequest) { r.Temperature = float64Ptr(3) },
			wantErr: "outside the allowed range",
		},
		{
			name:    "negative temperature",
			mutate:  func(r *GenerateRequest) { r.Temperature = float64Ptr(-1) },
			wantErr: "outside the allowed range",
		},
		{
			name:    "top_p out of range",
			mutate:  func(r *GenerateRequest) { r.TopP = float64Ptr(0) },
			wantErr: "top_p",
		},
		{
			name:    "empty stop sequence",
			mutate:  func(r *GenerateRequest) { r.Stop = []string{""} },
			wantErr: "stop[0] is empty",
		},
		{
			name: "response schema without a name",
			mutate: func(r *GenerateRequest) {
				schema := ObjectSchema(map[string]JSONSchema{"summary": StringSchema()})
				r.ResponseSchema = &schema
			},
			wantErr: "response_schema_name is required",
		},
		{
			name: "broken response schema",
			mutate: func(r *GenerateRequest) {
				schema := JSONSchema{"type": "date"}
				r.ResponseSchema = &schema
				r.ResponseSchemaName = "result"
			},
			wantErr: "unsupported schema type",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := base()
			test.mutate(request)
			err := request.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want %q", test.wantErr)
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Validate() = %q, want it to contain %q", err.Error(), test.wantErr)
			}
		})
	}
}

func TestGenerateRequestValidateNil(t *testing.T) {
	var request *GenerateRequest
	if err := request.Validate(); err == nil {
		t.Fatal("a nil request must be rejected")
	}
}

func TestGenerateRequestValidWithToolsAndSchema(t *testing.T) {
	schema := ObjectSchema(map[string]JSONSchema{"summary": StringSchema()}, "summary")
	request := &GenerateRequest{
		Model:    "qwen3-coder",
		Messages: []Message{NewUserMessage("review")},
		Tools: []ToolDefinition{{
			Name:        "repo.read",
			Description: "Read a file.",
			InputSchema: ObjectSchema(map[string]JSONSchema{"path": StringSchema()}, "path"),
		}},
		ToolChoice:         ToolChoice{Mode: ToolChoiceAuto},
		MaxTokens:          512,
		Temperature:        float64Ptr(0.2),
		TopP:               float64Ptr(1),
		ResponseSchema:     &schema,
		ResponseSchemaName: "review_result",
		Extra:              map[string]any{"reasoning": map[string]any{"effort": "low"}},
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	if !request.WantsTools() {
		t.Fatal("WantsTools() must report the offered tools")
	}
	if MaxTemperature != 2.0 {
		t.Fatalf("MaxTemperature = %v", MaxTemperature)
	}
}

func TestGenerateRequestCloneIsDeep(t *testing.T) {
	schema := ObjectSchema(nil)
	request := &GenerateRequest{
		Model:          "qwen3-coder",
		Messages:       []Message{NewUserMessage("task")},
		Tools:          []ToolDefinition{{Name: "repo.read"}},
		Stop:           []string{"STOP"},
		Temperature:    float64Ptr(0.5),
		ResponseSchema: &schema,
		Extra:          map[string]any{"nested": map[string]any{"a": 1}},
	}

	clone := request.Clone()
	clone.Messages[0].Content[0].Text = "changed"
	clone.Tools[0].Name = "git.diff"
	clone.Stop[0] = "END"
	*clone.Temperature = 1.5
	(*clone.ResponseSchema)["type"] = "array"
	clone.Extra["nested"].(map[string]any)["a"] = 2

	if request.Messages[0].Text() != "task" {
		t.Fatal("messages are shared between the request and its clone")
	}
	if request.Tools[0].Name != "repo.read" {
		t.Fatal("tools are shared between the request and its clone")
	}
	if request.Stop[0] != "STOP" {
		t.Fatal("stop sequences are shared between the request and its clone")
	}
	if *request.Temperature != 0.5 {
		t.Fatal("temperature is shared between the request and its clone")
	}
	if (*request.ResponseSchema)["type"] != "object" {
		t.Fatal("the response schema is shared between the request and its clone")
	}
	if request.Extra["nested"].(map[string]any)["a"] != 1 {
		t.Fatal("extra options are shared between the request and its clone")
	}

	var nilRequest *GenerateRequest
	if nilRequest.Clone() != nil {
		t.Fatal("cloning a nil request must return nil")
	}
}

func TestSplitSystem(t *testing.T) {
	system, rest := SplitSystem([]Message{
		NewSystemMessage("first"),
		NewSystemMessage("second"),
		NewUserMessage("task"),
		NewAssistantMessage("answer"),
	})

	if system != "first\n\nsecond" {
		t.Fatalf("system = %q", system)
	}
	if len(rest) != 2 {
		t.Fatalf("rest = %+v", rest)
	}
	if rest[0].Role != RoleUser || rest[1].Role != RoleAssistant {
		t.Fatalf("rest roles = %q, %q", rest[0].Role, rest[1].Role)
	}

	system, rest = SplitSystem([]Message{NewUserMessage("only user")})
	if system != "" {
		t.Fatalf("system = %q, want empty", system)
	}
	if len(rest) != 1 {
		t.Fatalf("rest = %+v", rest)
	}
}

func TestLastAssistantMessage(t *testing.T) {
	if _, ok := LastAssistantMessage(nil); ok {
		t.Fatal("an empty conversation has no assistant message")
	}
	messages := []Message{
		NewUserMessage("task"),
		NewAssistantMessage("first"),
		NewUserMessage("more"),
		NewAssistantMessage("second"),
	}
	message, ok := LastAssistantMessage(messages)
	if !ok || message.Text() != "second" {
		t.Fatalf("LastAssistantMessage() = %+v, %v", message, ok)
	}
}

func TestPendingToolCalls(t *testing.T) {
	callOne := ToolCall{ID: "c1", Name: "repo.read", Arguments: "{}"}
	callTwo := ToolCall{ID: "c2", Name: "git.diff", Arguments: "{}"}

	if got := PendingToolCalls([]Message{NewUserMessage("task")}); got != nil {
		t.Fatalf("PendingToolCalls() = %v, want nil when the model asked for nothing", got)
	}

	assistant := NewAssistantToolCallMessage(callOne, callTwo)

	unanswered := PendingToolCalls([]Message{NewUserMessage("task"), assistant})
	if len(unanswered) != 2 {
		t.Fatalf("pending = %+v, want both calls", unanswered)
	}

	partiallyAnswered := PendingToolCalls([]Message{
		NewUserMessage("task"),
		assistant,
		NewToolResultMessage(ToolResult{ToolCallID: "c1", Content: "ok"}),
	})
	if len(partiallyAnswered) != 1 || partiallyAnswered[0].ID != "c2" {
		t.Fatalf("pending = %+v, want only c2", partiallyAnswered)
	}

	fullyAnswered := PendingToolCalls([]Message{
		NewUserMessage("task"),
		assistant,
		NewToolResultMessage(ToolResult{ToolCallID: "c1", Content: "ok"}),
		NewToolResultMessage(ToolResult{ToolCallID: "c2", Content: "ok"}),
	})
	if fullyAnswered != nil {
		t.Fatalf("pending = %+v, want nil once every call is answered", fullyAnswered)
	}
}

func TestTemperatureBoundariesAreAccepted(t *testing.T) {
	for _, value := range []float64{0, 1, 2} {
		request := &GenerateRequest{
			Model:       "m",
			Messages:    []Message{NewUserMessage("t")},
			Temperature: float64Ptr(value),
		}
		if err := request.Validate(); err != nil {
			t.Fatalf("temperature %v should be accepted: %v", value, err)
		}
	}

	request := &GenerateRequest{
		Model:       "m",
		Messages:    []Message{NewUserMessage("t")},
		Temperature: float64Ptr(math.Nextafter(2, 3)),
	}
	if err := request.Validate(); err == nil {
		t.Fatal("a temperature above the maximum must be rejected")
	}
}
