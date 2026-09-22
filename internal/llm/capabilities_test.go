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
	"errors"
	"strings"
	"testing"

	"github.com/pawagents/pawagents/internal/apperrors"
)

func TestRequirementsFrom(t *testing.T) {
	request := &GenerateRequest{
		Model: "qwen3-coder",
		Messages: []Message{
			NewSystemMessage("instructions"),
			{Role: RoleUser, Content: []ContentPart{TextPart("what is in this screenshot?"), ImageURLPart("https://x/y.png")}},
		},
		Tools:     []ToolDefinition{{Name: "repo.read"}},
		MaxTokens: 2048,
		ResponseSchema: func() *JSONSchema {
			schema := ObjectSchema(nil)
			return &schema
		}(),
	}

	requirements := RequirementsFrom(request)
	if !requirements.Tools {
		t.Fatal("offered tools must be required")
	}
	if requirements.ParallelTools {
		t.Fatal("parallel tools must not be required implicitly")
	}
	if !requirements.StructuredOutput {
		t.Fatal("a response schema must require structured output")
	}
	if !requirements.Vision {
		t.Fatal("an image part must require vision")
	}
	if !requirements.SystemMessage {
		t.Fatal("a system message must require system message support")
	}
	if requirements.MaxOutputTokens != 2048 {
		t.Fatalf("max output tokens = %d", requirements.MaxOutputTokens)
	}

	if got := RequirementsFrom(nil); got != (Requirements{}) {
		t.Fatalf("RequirementsFrom(nil) = %+v", got)
	}
}

func TestCapabilitiesMissing(t *testing.T) {
	full := ModelCapabilities{
		ToolCalling:      true,
		ParallelTools:    true,
		Streaming:        true,
		StructuredOutput: true,
		Vision:           true,
		SystemMessage:    true,
		MaxContextTokens: 32768,
		MaxOutputTokens:  4096,
	}

	if missing := full.Missing(Requirements{}); len(missing) != 0 {
		t.Fatalf("missing = %v, want none", missing)
	}
	if !full.Supports(Requirements{Tools: true, StructuredOutput: true, Vision: true}) {
		t.Fatal("a fully capable model must satisfy the requirement set")
	}

	limited := ModelCapabilities{Streaming: true, SystemMessage: true, MaxContextTokens: 8192}
	missing := limited.Missing(Requirements{
		Tools:            true,
		ParallelTools:    true,
		StructuredOutput: true,
		Vision:           true,
		MinContextTokens: 20000,
	})
	want := []string{"tool_calling", "parallel_tools", "structured_output", "vision", "max_context_tokens>=20000"}
	if strings.Join(missing, ",") != strings.Join(want, ",") {
		t.Fatalf("missing = %v, want %v", missing, want)
	}

	// An unknown context window (zero) must not be reported as missing: the
	// budget manager uses a conservative default instead.
	unknown := ModelCapabilities{ToolCalling: true}
	if missing := unknown.Missing(Requirements{MinContextTokens: 100000}); len(missing) != 0 {
		t.Fatalf("missing = %v, want none", missing)
	}

	smallOutput := ModelCapabilities{MaxOutputTokens: 512}
	if got := smallOutput.Missing(Requirements{MaxOutputTokens: 4096}); len(got) != 1 {
		t.Fatalf("missing = %v", got)
	}
}

func TestCapabilitiesValidate(t *testing.T) {
	capabilities := ModelCapabilities{Streaming: true, MaxContextTokens: 8192}

	if err := capabilities.Validate(Requirements{Streaming: true}, "qwen3-coder"); err != nil {
		t.Fatalf("a satisfied requirement must validate: %v", err)
	}

	err := capabilities.Validate(Requirements{Tools: true, StructuredOutput: true}, "qwen3-coder")
	if err == nil {
		t.Fatal("an unmet requirement must produce a capability error")
	}
	if !apperrors.IsKind(err, apperrors.KindCapability) {
		t.Fatalf("kind = %q, want %q", apperrors.KindOf(err), apperrors.KindCapability)
	}

	message := err.Error()
	for _, want := range []string{"qwen3-coder", "tool_calling", "structured_output", "output_mode: text"} {
		if !strings.Contains(message, want) {
			t.Fatalf("message = %q, want it to contain %q", message, want)
		}
	}

	var classified *apperrors.Error
	if !errors.As(err, &classified) {
		t.Fatal("the error must be an *apperrors.Error")
	}
	missing, ok := classified.Details["missing"].([]string)
	if !ok || len(missing) != 2 {
		t.Fatalf("details = %#v", classified.Details)
	}

	// The model label defaults to a readable phrase when the name is empty.
	unnamed := capabilities.Validate(Requirements{Tools: true}, "  ")
	if !strings.Contains(unnamed.Error(), "the selected model") {
		t.Fatalf("message = %q", unnamed.Error())
	}
}

func TestModelCapabilitiesJSONShape(t *testing.T) {
	capabilities := ModelCapabilities{ToolCalling: true, MaxContextTokens: 32768}
	encoded, err := json.Marshal(capabilities)
	if err != nil {
		t.Fatalf("marshal returned %v", err)
	}
	for _, want := range []string{`"tool_calling":true`, `"max_context_tokens":32768`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("encoded = %s, want it to contain %s", encoded, want)
		}
	}
}
