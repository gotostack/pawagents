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
	"testing"
)

func TestRoleValid(t *testing.T) {
	for _, role := range Roles() {
		if !role.Valid() {
			t.Fatalf("role %q should be valid", role)
		}
	}
	if Role("developer").Valid() {
		t.Fatal("an unknown role must not be valid")
	}
	if Role("").Valid() {
		t.Fatal("an empty role must not be valid")
	}
}

func TestMessageConstructors(t *testing.T) {
	system := NewSystemMessage("you review code")
	if system.Role != RoleSystem || system.Text() != "you review code" {
		t.Fatalf("system message = %+v", system)
	}

	user := NewUserMessage("review this")
	if user.Role != RoleUser || user.Text() != "review this" {
		t.Fatalf("user message = %+v", user)
	}

	assistant := NewAssistantMessage("found one issue")
	if assistant.Role != RoleAssistant || !assistant.HasContent() {
		t.Fatalf("assistant message = %+v", assistant)
	}

	calls, err := NewToolCall("call_1", "repo.read", map[string]any{"path": "main.go"})
	if err != nil {
		t.Fatalf("NewToolCall() returned %v", err)
	}
	if calls.Arguments != `{"path":"main.go"}` {
		t.Fatalf("arguments = %q", calls.Arguments)
	}

	toolMessage := NewAssistantToolCallMessage(calls)
	if !toolMessage.HasToolCalls() || toolMessage.HasContent() {
		t.Fatalf("tool call message = %+v", toolMessage)
	}
	if err := toolMessage.Validate(); err != nil {
		t.Fatalf("Validate() returned %v", err)
	}

	resultMessage := NewToolResultMessage(ToolResult{
		ToolCallID: "call_1",
		Name:       "repo.read",
		Content:    "package main",
	})
	if err := resultMessage.Validate(); err != nil {
		t.Fatalf("Validate() returned %v", err)
	}
	if resultMessage.Text() != "" {
		t.Fatalf("a tool message carries its payload in ToolResult, got text %q", resultMessage.Text())
	}
}

func TestNewToolCallRejectsUnencodableArguments(t *testing.T) {
	_, err := NewToolCall("call_1", "repo.read", func() {})
	if err == nil {
		t.Fatal("NewToolCall should reject a value that cannot be marshalled")
	}
}

func TestToolCallArgumentsJSON(t *testing.T) {
	call := ToolCall{ID: "c1", Name: "repo.read", Arguments: `{"path":"main.go","max_bytes":10}`}

	var decoded struct {
		Path     string `json:"path"`
		MaxBytes int    `json:"max_bytes"`
	}
	if err := call.ArgumentsJSON(&decoded); err != nil {
		t.Fatalf("ArgumentsJSON() returned %v", err)
	}
	if decoded.Path != "main.go" || decoded.MaxBytes != 10 {
		t.Fatalf("decoded = %+v", decoded)
	}

	// A tool that takes no argument must not fail on an empty payload. The
	// decode target is fresh because encoding/json merges into existing maps.
	empty := ToolCall{ID: "c2", Name: "git.status"}
	target := map[string]any{}
	if err := empty.ArgumentsJSON(&target); err != nil {
		t.Fatalf("ArgumentsJSON() returned %v for an empty payload", err)
	}
	if len(target) != 0 {
		t.Fatalf("target = %v, want an empty object", target)
	}

	malformed := ToolCall{ID: "c3", Name: "repo.read", Arguments: "{not json"}
	if err := malformed.ArgumentsJSON(&decoded); err == nil {
		t.Fatal("ArgumentsJSON should reject malformed JSON")
	}
}

func TestMessageValidate(t *testing.T) {
	tests := []struct {
		name    string
		message Message
		wantErr bool
		want    string
	}{
		{
			name:    "valid user message",
			message: NewUserMessage("hello"),
		},
		{
			name:    "valid tool result",
			message: NewToolResultMessage(ToolResult{ToolCallID: "c1", Name: "repo.read", Content: "x"}),
		},
		{
			name:    "unsupported role",
			message: Message{Role: "developer", Content: []ContentPart{TextPart("hi")}},
			wantErr: true,
			want:    "unsupported role",
		},
		{
			name:    "tool result on a user message",
			message: Message{Role: RoleUser, ToolResult: &ToolResult{ToolCallID: "c1"}},
			wantErr: true,
			want:    "must be carried by a",
		},
		{
			name:    "tool message without a result",
			message: Message{Role: RoleTool},
			wantErr: true,
			want:    "must carry a tool result",
		},
		{
			name: "tool message requesting tool calls",
			message: Message{
				Role:       RoleTool,
				ToolResult: &ToolResult{ToolCallID: "c1"},
				ToolCalls:  []ToolCall{{ID: "c2", Name: "repo.read"}},
			},
			wantErr: true,
			want:    "must not request tool calls",
		},
		{
			name:    "tool result without an identifier",
			message: Message{Role: RoleTool, ToolResult: &ToolResult{Name: "repo.read"}},
			wantErr: true,
			want:    "missing the tool call id",
		},
		{
			name:    "tool call without a name",
			message: NewAssistantToolCallMessage(ToolCall{ID: "c1"}),
			wantErr: true,
			want:    "missing the tool name",
		},
		{
			name:    "tool call with non object arguments",
			message: NewAssistantToolCallMessage(ToolCall{ID: "c1", Name: "repo.read", Arguments: "[1,2]"}),
			wantErr: true,
			want:    "not a JSON object",
		},
		{
			name:    "empty content part kind",
			message: Message{Role: RoleUser, Content: []ContentPart{{Text: "x"}}},
			wantErr: true,
			want:    "missing its kind",
		},
		{
			name:    "unsupported content kind",
			message: Message{Role: RoleUser, Content: []ContentPart{{Kind: "audio"}}},
			wantErr: true,
			want:    "unsupported content kind",
		},
		{
			name:    "image part without payload",
			message: Message{Role: RoleUser, Content: []ContentPart{{Kind: ContentImage}}},
			wantErr: true,
			want:    "missing the image payload",
		},
		{
			name:    "image part with data and URL",
			message: Message{Role: RoleUser, Content: []ContentPart{ImageURLPart("https://x/y.png")}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.message.Validate()
			if test.wantErr {
				if err == nil {
					t.Fatal("Validate() = nil, want an error")
				}
				if !strings.Contains(err.Error(), test.want) {
					t.Fatalf("Validate() = %q, want it to contain %q", err.Error(), test.want)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() = %v", err)
			}
		})
	}
}

func TestImagePartValidation(t *testing.T) {
	if err := ImageDataPart("image/png", []byte{1, 2, 3}).Validate(); err != nil {
		t.Fatalf("inline image should be valid: %v", err)
	}
	if err := ImageDataPart("", []byte{1}).Validate(); err == nil {
		t.Fatal("inline data without a MIME type must be rejected")
	}

	both := ContentPart{Kind: ContentImage, Image: &ImagePart{
		MIMEType: "image/png",
		Data:     []byte{1},
		URL:      "https://example.com/x.png",
	}}
	if err := both.Validate(); err == nil {
		t.Fatal("an image with both data and URL must be rejected")
	}
}

func TestMessageTextSkipsImagesAndResults(t *testing.T) {
	message := Message{
		Role: RoleAssistant,
		Content: []ContentPart{
			TextPart("first "),
			ThinkingPart("thinking "),
			ImageURLPart("https://example.com/x.png"),
			TextPart("second"),
		},
	}
	if got := message.Text(); got != "first thinking second" {
		t.Fatalf("Text() = %q", got)
	}
}

func TestMessageCloneIsDeep(t *testing.T) {
	original := Message{
		Role:      RoleUser,
		Content:   []ContentPart{ImageDataPart("image/png", []byte{1, 2})},
		ToolCalls: []ToolCall{{ID: "c1", Name: "repo.read", Arguments: "{}"}},
		ToolResult: &ToolResult{
			ToolCallID: "c1",
			Metadata:   map[string]any{"truncated": true},
		},
	}

	clone := original.Clone()

	original.Role = RoleTool
	if clone.Role != RoleUser {
		t.Fatalf("clone role = %q, want the value captured at clone time", clone.Role)
	}

	clone.Content[0].Image.Data[0] = 99
	if original.Content[0].Image.Data[0] != 1 {
		t.Fatal("image data is shared between the original and the clone")
	}

	clone.ToolCalls[0].Name = "git.diff"
	if original.ToolCalls[0].Name != "repo.read" {
		t.Fatal("tool calls are shared between the original and the clone")
	}

	clone.ToolResult.Metadata["truncated"] = false
	if original.ToolResult.Metadata["truncated"] != true {
		t.Fatal("tool result metadata is shared between the original and the clone")
	}
}

func TestValidateConversation(t *testing.T) {
	if err := ValidateConversation(nil); err == nil {
		t.Fatal("an empty conversation must be rejected")
	} else if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error = %q", err.Error())
	}

	conversation := []Message{
		NewSystemMessage("instructions"),
		NewUserMessage("task"),
		{},
	}
	err := ValidateConversation(conversation)
	if err == nil {
		t.Fatal("an invalid message must fail the conversation")
	}
	if !strings.Contains(err.Error(), "messages[2]") {
		t.Fatalf("error = %q, want the offending index", err.Error())
	}

	if err := ValidateConversation([]Message{NewSystemMessage("s"), NewUserMessage("u")}); err != nil {
		t.Fatalf("valid conversation returned %v", err)
	}
}

func TestMessageIsEmpty(t *testing.T) {
	if !(Message{Role: RoleAssistant}).IsEmpty() {
		t.Fatal("an assistant message without content must be empty")
	}
	if NewAssistantMessage("x").IsEmpty() {
		t.Fatal("an assistant message with text must not be empty")
	}
}
