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

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/llm"
	"github.com/pawagents/pawagents/internal/provider"
)

// newTestProvider builds a provider pointed at a test server.
func newTestProvider(t *testing.T, handler http.Handler) (*Provider, *httptest.Server) {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	transport, err := provider.NewTransport(&config.Provider{
		Type:      config.ProviderTypeOpenAICompat,
		BaseURL:   server.URL + "/v1",
		APIKey:    "sk-test",
		Timeout:   config.Duration(5 * time.Second),
		ExtraBody: map[string]any{"gateway_flag": "on"},
	})
	if err != nil {
		t.Fatalf("NewTransport() = %v", err)
	}

	instance, err := New(context.Background(), provider.Options{
		Name:      "gateway",
		Config:    &config.Provider{Type: config.ProviderTypeOpenAICompat, BaseURL: server.URL + "/v1"},
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	openai, ok := instance.(*Provider)
	if !ok {
		t.Fatalf("New() returned %T", instance)
	}
	return openai, server
}

func testRequest() *llm.GenerateRequest {
	return &llm.GenerateRequest{
		Model:    "qwen3-coder",
		Messages: []llm.Message{llm.NewUserMessage("review main.go")},
	}
}

func TestProviderMetadata(t *testing.T) {
	instance, _ := newTestProvider(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	if instance.Name() != "gateway" {
		t.Fatalf("Name() = %q", instance.Name())
	}
	if instance.Type() != config.ProviderTypeOpenAICompat {
		t.Fatalf("Type() = %q", instance.Type())
	}

	info := instance.Info()
	if info.Name != "gateway" || info.Type != config.ProviderTypeOpenAICompat {
		t.Fatalf("Info() = %+v", info)
	}
	if info.Credential == "" || strings.Contains(info.Credential, "sk-test") {
		t.Fatalf("Credential = %q, want a description, never the secret", info.Credential)
	}
	if err := instance.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
}

func TestRequestMapping(t *testing.T) {
	var received map[string]any
	var path string
	var authHeader string

	instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		authHeader = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &received); err != nil {
			t.Errorf("cannot decode the request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"qwen3-coder","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))

	call, err := llm.NewToolCall("call_1", "repo.read", map[string]any{"path": "main.go"})
	if err != nil {
		t.Fatalf("NewToolCall() = %v", err)
	}
	schema := llm.ObjectSchema(map[string]llm.JSONSchema{"summary": llm.StringSchema()}, "summary")
	temperature := 0.2

	request := &llm.GenerateRequest{
		Model: "qwen3-coder",
		Messages: []llm.Message{
			llm.NewSystemMessage("you review code"),
			llm.NewUserMessage("review main.go"),
			llm.NewAssistantToolCallMessage(call),
			llm.NewToolResultMessage(llm.ToolResult{ToolCallID: "call_1", Name: "repo.read", Content: "package main", IsError: true}),
		},
		Tools: []llm.ToolDefinition{{
			Name:        "repo.read",
			Description: "Read a file.",
			InputSchema: llm.ObjectSchema(map[string]llm.JSONSchema{"path": llm.StringSchema()}, "path"),
			Strict:      true,
		}},
		ToolChoice:         llm.ToolChoice{Mode: llm.ToolChoiceAuto},
		MaxTokens:          1024,
		Temperature:        &temperature,
		Stop:               []string{"\n\n"},
		ResponseSchema:     &schema,
		ResponseSchemaName: "review_result",
		Extra:              map[string]any{"top_k": 40},
	}

	stream, err := instance.Generate(context.Background(), request)
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	if _, err := llm.Collect(context.Background(), stream); err != nil {
		t.Fatalf("Collect() = %v", err)
	}

	if path != "/v1/chat/completions" {
		t.Fatalf("path = %q", path)
	}
	if authHeader != "Bearer sk-test" {
		t.Fatalf("Authorization = %q", authHeader)
	}
	if received["model"] != "qwen3-coder" {
		t.Fatalf("model = %v", received["model"])
	}
	if received["stream"] != true {
		t.Fatalf("stream = %v, want true", received["stream"])
	}
	if received["max_tokens"] != float64(1024) {
		t.Fatalf("max_tokens = %v", received["max_tokens"])
	}
	if received["top_k"] != float64(40) {
		t.Fatalf("top_k = %v, want the per-request extra to be merged", received["top_k"])
	}
	if received["gateway_flag"] != "on" {
		t.Fatalf("gateway_flag = %v, want the provider extra_body to be merged", received["gateway_flag"])
	}
	if _, ok := received["tool_choice"]; ok {
		t.Fatal("auto tool choice is the default and must be omitted")
	}

	streamOptions, ok := received["stream_options"].(map[string]any)
	if !ok || streamOptions["include_usage"] != true {
		t.Fatalf("stream_options = %v", received["stream_options"])
	}

	responseFormat, ok := received["response_format"].(map[string]any)
	if !ok || responseFormat["type"] != "json_schema" {
		t.Fatalf("response_format = %v", received["response_format"])
	}
	jsonSchema, ok := responseFormat["json_schema"].(map[string]any)
	if !ok || jsonSchema["name"] != "review_result" || jsonSchema["strict"] != true {
		t.Fatalf("json_schema = %v", jsonSchema)
	}

	tools, ok := received["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %v", received["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "function" {
		t.Fatalf("tool type = %v", tool["type"])
	}
	function := tool["function"].(map[string]any)
	if function["name"] != "repo.read" || function["strict"] != true {
		t.Fatalf("function = %v", function)
	}
	parameters, ok := function["parameters"].(map[string]any)
	if !ok || parameters["type"] != "object" {
		t.Fatalf("parameters = %v", function["parameters"])
	}

	messages := received["messages"].([]any)
	if len(messages) != 4 {
		t.Fatalf("messages = %v", messages)
	}
	if messages[0].(map[string]any)["role"] != "system" {
		t.Fatalf("messages[0] = %v", messages[0])
	}
	if messages[0].(map[string]any)["content"] != "you review code" {
		t.Fatalf("messages[0] content = %v", messages[0].(map[string]any)["content"])
	}
	if messages[1].(map[string]any)["content"] != "review main.go" {
		t.Fatalf("messages[1] content = %v", messages[1].(map[string]any)["content"])
	}

	assistant := messages[2].(map[string]any)
	if assistant["role"] != "assistant" {
		t.Fatalf("assistant = %v", assistant)
	}
	calls := assistant["tool_calls"].([]any)
	callObject := calls[0].(map[string]any)
	if callObject["id"] != "call_1" || callObject["type"] != "function" {
		t.Fatalf("tool call = %v", callObject)
	}
	callFunction := callObject["function"].(map[string]any)
	if callFunction["name"] != "repo.read" || callFunction["arguments"] != `{"path":"main.go"}` {
		t.Fatalf("tool call function = %v", callFunction)
	}

	toolMessage := messages[3].(map[string]any)
	if toolMessage["role"] != "tool" || toolMessage["tool_call_id"] != "call_1" {
		t.Fatalf("tool message = %v", toolMessage)
	}
	content, _ := toolMessage["content"].(string)
	if !strings.HasPrefix(content, "[tool error] ") {
		t.Fatalf("tool message content = %q, want a failure marker", content)
	}
}

func TestRequestMappingSendsImagesAsContentParts(t *testing.T) {
	var received map[string]any

	instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))

	request := &llm.GenerateRequest{
		Model: "vision-model",
		Messages: []llm.Message{{
			Role: llm.RoleUser,
			Content: []llm.ContentPart{
				llm.TextPart("what is this?"),
				llm.ImageURLPart("https://example.com/x.png"),
				llm.ImageDataPart("image/png", []byte{1, 2, 3}),
				llm.ThinkingPart("internal reasoning"),
			},
		}},
	}

	stream, err := instance.Generate(context.Background(), request)
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	if _, err := llm.Collect(context.Background(), stream); err != nil {
		t.Fatalf("Collect() = %v", err)
	}

	messages := received["messages"].([]any)
	parts, ok := messages[0].(map[string]any)["content"].([]any)
	if !ok {
		t.Fatalf("content = %v, want a content part array", messages[0].(map[string]any)["content"])
	}
	if len(parts) != 3 {
		t.Fatalf("parts = %v, want text, url image and inline image", parts)
	}
	if parts[0].(map[string]any)["type"] != "text" {
		t.Fatalf("parts[0] = %v", parts[0])
	}
	image := parts[1].(map[string]any)
	if image["type"] != "image_url" {
		t.Fatalf("parts[1] = %v", image)
	}
	if image["image_url"].(map[string]any)["url"] != "https://example.com/x.png" {
		t.Fatalf("image url = %v", image["image_url"])
	}
	inline := parts[2].(map[string]any)
	dataURL := inline["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(dataURL, "data:image/png;base64,") {
		t.Fatalf("inline image = %q, want a data URL", dataURL)
	}
	for _, part := range parts {
		if strings.Contains(part.(map[string]any)["type"].(string), "thinking") {
			t.Fatal("reasoning content must never be sent back to the provider")
		}
	}
}

func TestRequestMappingToolChoiceFunction(t *testing.T) {
	var received map[string]any

	instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))

	request := testRequest()
	request.Tools = []llm.ToolDefinition{{
		Name:        "repo.read",
		Description: "Read a file.",
		InputSchema: llm.ObjectSchema(nil),
	}}
	request.ToolChoice = llm.ToolChoice{Mode: llm.ToolChoiceFunction, Name: "repo.read"}

	stream, err := instance.Generate(context.Background(), request)
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	if _, err := llm.Collect(context.Background(), stream); err != nil {
		t.Fatalf("Collect() = %v", err)
	}

	choice, ok := received["tool_choice"].(map[string]any)
	if !ok || choice["type"] != "function" {
		t.Fatalf("tool_choice = %v", received["tool_choice"])
	}
	if choice["function"].(map[string]any)["name"] != "repo.read" {
		t.Fatalf("tool_choice = %v", choice)
	}
}

func TestGenerateRejectsInvalidRequest(t *testing.T) {
	instance, _ := newTestProvider(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the provider must not be called for an invalid request")
	}))

	if _, err := instance.Generate(context.Background(), nil); err == nil {
		t.Fatal("a nil request must be rejected")
	}
	if _, err := instance.Generate(context.Background(), &llm.GenerateRequest{}); err == nil {
		t.Fatal("a request without a model must be rejected")
	}
}
