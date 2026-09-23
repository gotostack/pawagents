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

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/llm"
	"github.com/pawagents/pawagents/internal/provider"
)

// requestRecorder captures what a fake endpoint was asked.
type requestRecorder struct {
	server   *httptest.Server
	requests []recordedRequest
}

type recordedRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Body    string
}

// newTestProvider points a provider at a fake Messages API.
func newTestProvider(t *testing.T, handler http.Handler, options ...func(*config.Provider)) (*Provider, *requestRecorder) {
	t.Helper()

	recorder := &requestRecorder{}
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		recorder.requests = append(recorder.requests, recordedRequest{
			Method:  r.Method,
			Path:    r.URL.Path,
			Headers: r.Header.Clone(),
			Body:    string(body),
		})
		handler.ServeHTTP(w, r)
	})

	recorder.server = httptest.NewServer(wrapped)
	t.Cleanup(recorder.server.Close)

	cfgProvider := &config.Provider{
		Type:    config.ProviderTypeAnthropic,
		BaseURL: recorder.server.URL,
		APIKey:  "sk-ant-test",
		Timeout: config.Duration(5 * time.Second),
	}
	for _, option := range options {
		option(cfgProvider)
	}

	transport, err := provider.NewTransport(cfgProvider)
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}

	instance, err := New(context.Background(), provider.Options{
		Name:      "cloud-anthropic",
		Config:    cfgProvider,
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return instance.(*Provider), recorder
}

// sse joins event payloads into an event stream.
func sse(payloads ...string) string {
	lines := make([]string, 0, len(payloads))
	for _, payload := range payloads {
		lines = append(lines, "event: message\ndata: "+payload)
	}
	return strings.Join(lines, "\n\n") + "\n\n"
}

// event renders one event payload.
func event(fields map[string]any) string {
	encoded, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// textStream is a complete streaming answer.
func textStream(text string) string {
	return sse(
		event(map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":    "msg_1",
				"model": "claude-sonnet-4",
				"role":  "assistant",
				"usage": map[string]any{
					"input_tokens":                120,
					"output_tokens":               1,
					"cache_read_input_tokens":     64,
					"cache_creation_input_tokens": 8,
				},
			},
		}),
		event(map[string]any{"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "text", "text": ""}}),
		event(map[string]any{"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": text}}),
		event(map[string]any{"type": "content_block_stop", "index": 0}),
		event(map[string]any{"type": "message_delta",
			"delta": map[string]any{"stop_reason": "end_turn"},
			"usage": map[string]any{"output_tokens": 42}}),
		event(map[string]any{"type": "message_stop"}),
	)
}

func TestGenerateStreamsATextAnswer(t *testing.T) {
	instance, recorder := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, textStream("The change looks correct."))
	}))

	stream, err := instance.Generate(context.Background(), &llm.GenerateRequest{
		Model:     "claude-sonnet-4",
		MaxTokens: 1024,
		Messages: []llm.Message{
			llm.NewSystemMessage("You review Go code."),
			llm.NewUserMessage("Review the change."),
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	response, err := llm.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if response.Text() != "The change looks correct." {
		t.Fatalf("text = %q", response.Text())
	}
	if response.FinishReason != llm.FinishReasonStop {
		t.Fatalf("finish reason = %q", response.FinishReason)
	}
	if response.Usage.InputTokens != 120 || response.Usage.OutputTokens != 42 {
		t.Fatalf("usage = %+v", response.Usage)
	}
	if response.Usage.CacheReadTokens != 64 || response.Usage.CacheWriteTokens != 8 {
		t.Fatalf("cache usage = %+v", response.Usage)
	}

	// The request must carry the protocol version, the credential and a system
	// prompt outside the conversation.
	if len(recorder.requests) != 1 {
		t.Fatalf("requests = %d", len(recorder.requests))
	}
	request := recorder.requests[0]
	if request.Method != http.MethodPost || request.Path != "/v1/messages" {
		t.Fatalf("request = %+v", request)
	}
	if request.Headers.Get("anthropic-version") != apiVersion {
		t.Fatalf("version header = %q", request.Headers.Get("anthropic-version"))
	}
	if request.Headers.Get("x-api-key") != "sk-ant-test" {
		t.Fatalf("credential header = %q", request.Headers.Get("x-api-key"))
	}

	var sent messagesRequest
	if err := json.Unmarshal([]byte(request.Body), &sent); err != nil {
		t.Fatalf("cannot decode the request: %v", err)
	}
	if sent.System != "You review Go code." {
		t.Fatalf("system = %q", sent.System)
	}
	if len(sent.Messages) != 1 || sent.Messages[0].Role != "user" {
		t.Fatalf("messages = %+v", sent.Messages)
	}
	if sent.MaxTokens != 1024 || !sent.Stream {
		t.Fatalf("request = %+v", sent)
	}
}

func TestGenerateStreamsAToolCall(t *testing.T) {
	instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse(
			event(map[string]any{"type": "message_start",
				"message": map[string]any{"model": "claude-sonnet-4",
					"usage": map[string]any{"input_tokens": 80, "output_tokens": 1}}}),
			event(map[string]any{"type": "content_block_start", "index": 0,
				"content_block": map[string]any{"type": "tool_use", "id": "toolu_1",
					"name": "repo.read", "input": map[string]any{}}}),
			event(map[string]any{"type": "content_block_delta", "index": 0,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": `{"path":`}}),
			event(map[string]any{"type": "content_block_delta", "index": 0,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": `"main.go"}`}}),
			event(map[string]any{"type": "content_block_stop", "index": 0}),
			event(map[string]any{"type": "message_delta",
				"delta": map[string]any{"stop_reason": "tool_use"},
				"usage": map[string]any{"output_tokens": 30}}),
			event(map[string]any{"type": "message_stop"}),
		))
	}))

	stream, err := instance.Generate(context.Background(), &llm.GenerateRequest{
		Model:    "claude-sonnet-4",
		Messages: []llm.Message{llm.NewUserMessage("Read the file.")},
		Tools: []llm.ToolDefinition{{
			Name:        "repo.read",
			Description: "Read a file",
			InputSchema: llm.ObjectSchema(map[string]llm.JSONSchema{"path": llm.StringSchema()}, "path"),
		}},
		ToolChoice: llm.ToolChoice{Mode: llm.ToolChoiceAuto},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	response, err := llm.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if !response.HasToolCalls() {
		t.Fatalf("the tool call was lost: %+v", response.Message)
	}
	if response.FinishReason != llm.FinishReasonToolCalls {
		t.Fatalf("finish reason = %q", response.FinishReason)
	}

	call := response.Message.ToolCalls[0]
	if call.ID != "toolu_1" || call.Name != "repo.read" {
		t.Fatalf("call = %+v", call)
	}
	if call.Arguments != `{"path":"main.go"}` {
		t.Fatalf("arguments = %q", call.Arguments)
	}
}

func TestGenerateStreamsReasoning(t *testing.T) {
	instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse(
			event(map[string]any{"type": "message_start",
				"message": map[string]any{"model": "claude-sonnet-4"}}),
			event(map[string]any{"type": "content_block_start", "index": 0,
				"content_block": map[string]any{"type": "thinking", "thinking": ""}}),
			event(map[string]any{"type": "content_block_delta", "index": 0,
				"delta": map[string]any{"type": "thinking_delta", "thinking": "let me check"}}),
			event(map[string]any{"type": "content_block_delta", "index": 0,
				"delta": map[string]any{"type": "signature_delta", "signature": "abc"}}),
			event(map[string]any{"type": "content_block_stop", "index": 0}),
			event(map[string]any{"type": "content_block_start", "index": 1,
				"content_block": map[string]any{"type": "text", "text": ""}}),
			event(map[string]any{"type": "content_block_delta", "index": 1,
				"delta": map[string]any{"type": "text_delta", "text": "It is fine."}}),
			event(map[string]any{"type": "message_delta",
				"delta": map[string]any{"stop_reason": "end_turn"}}),
			event(map[string]any{"type": "message_stop"}),
		))
	}))

	stream, err := instance.Generate(context.Background(), &llm.GenerateRequest{
		Model:    "claude-sonnet-4",
		Messages: []llm.Message{llm.NewUserMessage("Is it fine?")},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	response, err := llm.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if response.Text() != "It is fine." {
		t.Fatalf("text = %q", response.Text())
	}
	if response.Message.ReasoningText() != "let me check" {
		t.Fatalf("reasoning = %q", response.Message.ReasoningText())
	}
}

func TestGenerateSendsToolResultsInsideAUserMessage(t *testing.T) {
	instance, recorder := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, textStream("done"))
	}))

	stream, err := instance.Generate(context.Background(), &llm.GenerateRequest{
		Model: "claude-sonnet-4",
		Messages: []llm.Message{
			llm.NewSystemMessage("Review."),
			llm.NewUserMessage("Read both files."),
			llm.NewAssistantToolCallMessage(
				llm.ToolCall{ID: "toolu_1", Name: "repo.read", Arguments: `{"path":"a.go"}`},
				llm.ToolCall{ID: "toolu_2", Name: "repo.read", Arguments: `{"path":"b.go"}`},
			),
			llm.NewToolResultMessage(llm.ToolResult{ToolCallID: "toolu_1", Name: "repo.read", Content: "package a"}),
			llm.NewToolResultMessage(llm.ToolResult{ToolCallID: "toolu_2", Name: "repo.read", Content: "package b", IsError: true}),
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	_, _ = llm.Collect(context.Background(), stream)

	var sent messagesRequest
	if err := json.Unmarshal([]byte(recorder.requests[0].Body), &sent); err != nil {
		t.Fatalf("cannot decode the request: %v", err)
	}

	// user, assistant(tool_use x2), user(tool_result x2)
	if len(sent.Messages) != 3 {
		t.Fatalf("messages = %+v", sent.Messages)
	}
	if sent.Messages[1].Role != "assistant" || len(sent.Messages[1].Content) != 2 {
		t.Fatalf("assistant = %+v", sent.Messages[1])
	}
	if sent.Messages[1].Content[0].Type != "tool_use" || sent.Messages[1].Content[0].ID != "toolu_1" {
		t.Fatalf("tool_use = %+v", sent.Messages[1].Content[0])
	}
	if string(sent.Messages[1].Content[0].Input) != `{"path":"a.go"}` {
		t.Fatalf("input = %s", sent.Messages[1].Content[0].Input)
	}

	results := sent.Messages[2]
	if results.Role != "user" || len(results.Content) != 2 {
		t.Fatalf("tool results must share one user message: %+v", results)
	}
	if results.Content[0].Type != "tool_result" || results.Content[0].ToolUseID != "toolu_1" {
		t.Fatalf("tool_result = %+v", results.Content[0])
	}
	if results.Content[0].Content != "package a" {
		t.Fatalf("content = %q", results.Content[0].Content)
	}
	if !results.Content[1].IsError {
		t.Fatalf("a failed tool must be marked: %+v", results.Content[1])
	}
}

func TestBuildRequestRejectsASchema(t *testing.T) {
	instance, _ := newTestProvider(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	schema := llm.ObjectSchema(map[string]llm.JSONSchema{"summary": llm.StringSchema()}, "summary")
	_, err := instance.Generate(context.Background(), &llm.GenerateRequest{
		Model:              "claude-sonnet-4",
		Messages:           []llm.Message{llm.NewUserMessage("Review.")},
		ResponseSchema:     &schema,
		ResponseSchemaName: "result",
	})
	if err == nil {
		t.Fatalf("a schema must be refused instead of being silently dropped")
	}
	if kind := apperrors.KindOf(err); kind != apperrors.KindCapability {
		t.Fatalf("kind = %s, want %s", kind, apperrors.KindCapability)
	}
}

func TestBuildRequestDropsToolsWhenToolUseIsForbidden(t *testing.T) {
	instance, recorder := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, textStream("done"))
	}))

	stream, err := instance.Generate(context.Background(), &llm.GenerateRequest{
		Model:    "claude-sonnet-4",
		Messages: []llm.Message{llm.NewUserMessage("Answer without tools.")},
		Tools: []llm.ToolDefinition{{
			Name:        "repo.read",
			InputSchema: llm.ObjectSchema(map[string]llm.JSONSchema{"path": llm.StringSchema()}),
		}},
		ToolChoice: llm.ToolChoice{Mode: llm.ToolChoiceNone},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	_, _ = llm.Collect(context.Background(), stream)

	var sent messagesRequest
	if err := json.Unmarshal([]byte(recorder.requests[0].Body), &sent); err != nil {
		t.Fatalf("cannot decode the request: %v", err)
	}
	if len(sent.Tools) != 0 || sent.ToolChoice != nil {
		t.Fatalf("tools = %+v, choice = %+v", sent.Tools, sent.ToolChoice)
	}
}

func TestBuildRequestMergesExtraBody(t *testing.T) {
	instance, recorder := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, textStream("done"))
	}), func(cfg *config.Provider) {
		cfg.ExtraBody = map[string]any{"metadata": map[string]any{"user_id": "u1"}}
	})

	stream, err := instance.Generate(context.Background(), &llm.GenerateRequest{
		Model:    "claude-sonnet-4",
		Messages: []llm.Message{llm.NewUserMessage("Review.")},
		Extra:    map[string]any{"top_k": 5},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	_, _ = llm.Collect(context.Background(), stream)

	var sent map[string]any
	if err := json.Unmarshal([]byte(recorder.requests[0].Body), &sent); err != nil {
		t.Fatalf("cannot decode the request: %v", err)
	}
	if sent["top_k"] != float64(5) {
		t.Fatalf("the per-request extra was dropped: %+v", sent)
	}
	if _, ok := sent["metadata"]; !ok {
		t.Fatalf("the configured extra body was dropped: %+v", sent)
	}
}

func TestGenerateHandlesACompleteResponse(t *testing.T) {
	instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id": "msg_2",
			"model": "claude-sonnet-4",
			"content": [
				{"type": "text", "text": "Nothing to report."},
				{"type": "tool_use", "id": "toolu_9", "name": "git.status", "input": {}}
			],
			"stop_reason": "tool_use",
			"usage": {"input_tokens": 30, "output_tokens": 12}
		}`)
	}))

	stream, err := instance.Generate(context.Background(), &llm.GenerateRequest{
		Model:    "claude-sonnet-4",
		Messages: []llm.Message{llm.NewUserMessage("Status?")},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	response, err := llm.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if response.Text() != "Nothing to report." {
		t.Fatalf("text = %q", response.Text())
	}
	if !response.HasToolCalls() || response.Message.ToolCalls[0].ID != "toolu_9" {
		t.Fatalf("tool calls = %+v", response.Message.ToolCalls)
	}
	if response.Message.ToolCalls[0].Arguments != "{}" {
		t.Fatalf("arguments = %q", response.Message.ToolCalls[0].Arguments)
	}
	if response.FinishReason != llm.FinishReasonToolCalls {
		t.Fatalf("finish reason = %q", response.FinishReason)
	}
	if response.Usage.InputTokens != 30 || response.Usage.OutputTokens != 12 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}

func TestGenerateMapsHTTPFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   apperrors.Kind
	}{
		{
			name:   "bad credential",
			status: http.StatusUnauthorized,
			body:   `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`,
			want:   apperrors.KindAuthentication,
		},
		{
			name:   "missing model",
			status: http.StatusNotFound,
			body:   `{"type":"error","error":{"type":"not_found_error","message":"model not found"}}`,
			want:   apperrors.KindNotFound,
		},
		{
			name:   "overloaded",
			status: http.StatusTooManyRequests,
			body:   `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`,
			want:   apperrors.KindProvider,
		},
		{
			name:   "server error",
			status: http.StatusInternalServerError,
			body:   `{"type":"error","error":{"type":"api_error","message":"boom"}}`,
			want:   apperrors.KindProvider,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(testCase.status)
				_, _ = io.WriteString(w, testCase.body)
			}))

			_, err := instance.Generate(context.Background(), &llm.GenerateRequest{
				Model:    "claude-sonnet-4",
				Messages: []llm.Message{llm.NewUserMessage("Review.")},
			})
			if err == nil {
				t.Fatalf("expected a failure")
			}
			if kind := apperrors.KindOf(err); kind != testCase.want {
				t.Fatalf("kind = %s, want %s (%v)", kind, testCase.want, err)
			}
		})
	}
}

func TestGenerateReportsAStreamFailure(t *testing.T) {
	instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse(
			event(map[string]any{"type": "message_start",
				"message": map[string]any{"model": "claude-sonnet-4"}}),
			event(map[string]any{"type": "error",
				"error": map[string]any{"type": "overloaded_error", "message": "overloaded"}}),
		))
	}))

	stream, err := instance.Generate(context.Background(), &llm.GenerateRequest{
		Model:    "claude-sonnet-4",
		Messages: []llm.Message{llm.NewUserMessage("Review.")},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	_, err = llm.Collect(context.Background(), stream)
	if err == nil {
		t.Fatalf("a mid-stream error must surface")
	}
	if !strings.Contains(err.Error(), "overloaded") {
		t.Fatalf("error = %v", err)
	}
}

func TestCapabilitiesUseTheStaticTable(t *testing.T) {
	instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"data":[{"id":"claude-sonnet-4","display_name":"Claude Sonnet 4"}]}`)
			return
		}
		_, _ = io.WriteString(w, textStream("done"))
	}))

	capabilities, err := instance.Capabilities(context.Background(), "claude-sonnet-4")
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if !capabilities.ToolCalling || !capabilities.SystemMessage {
		t.Fatalf("capabilities = %+v", capabilities)
	}
	if capabilities.StructuredOutput {
		t.Fatalf("the Messages API cannot enforce a schema: %+v", capabilities)
	}

	// A model the listing does not mention is a warning, not a refusal: a
	// proxy may expose aliases that the listing does not enumerate.
	if _, err := instance.Capabilities(context.Background(), "claude-unknown"); err != nil {
		t.Fatalf("an unlisted model must not fail the capability check: %v", err)
	}
}

func TestCapabilitiesSurfaceAnAuthenticationFailure(t *testing.T) {
	instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid x-api-key"}}`)
	}))

	_, err := instance.Capabilities(context.Background(), "claude-sonnet-4")
	if err == nil {
		t.Fatalf("a rejected credential must not be hidden")
	}
	if kind := apperrors.KindOf(err); kind != apperrors.KindAuthentication {
		t.Fatalf("kind = %s", kind)
	}
}

func TestHealthUsesTheModelListing(t *testing.T) {
	healthy, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"claude-sonnet-4"}]}`)
	}))

	health, err := healthy.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !health.Reachable || !strings.Contains(health.Detail, "1 model") {
		t.Fatalf("health = %+v", health)
	}

	broken, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	health, err = broken.Health(context.Background())
	if err == nil || health.Reachable {
		t.Fatalf("health = %+v, err = %v", health, err)
	}
}

func TestListModelsReportsTheEndpointModels(t *testing.T) {
	instance, recorder := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"claude-sonnet-4"},{"id":"claude-opus-4"}]}`)
	}))

	models, err := instance.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 || models[0] != "claude-sonnet-4" {
		t.Fatalf("models = %v", models)
	}
	if recorder.requests[0].Path != "/v1/models" {
		t.Fatalf("path = %q", recorder.requests[0].Path)
	}
	if recorder.requests[0].Headers.Get("anthropic-version") != apiVersion {
		t.Fatalf("the version header is required on every call")
	}
}

func TestInfoDescribesTheEndpoint(t *testing.T) {
	instance, _ := newTestProvider(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	if instance.Name() != "cloud-anthropic" || instance.Type() != providerType {
		t.Fatalf("provider = %s/%s", instance.Name(), instance.Type())
	}
	info := instance.Info()
	if info.BaseURL == "" || info.Credential != "inline" {
		t.Fatalf("info = %+v", info)
	}
	if err := instance.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestToWireMessagesRejectsUnsupportedInput(t *testing.T) {
	_, err := toWireMessages([]llm.Message{
		llm.NewSystemMessage("system"),
	})
	if err == nil {
		t.Fatalf("a conversation with no user turn must be refused")
	}

	_, err = toWireMessages([]llm.Message{
		{Role: "developer", Content: []llm.ContentPart{llm.TextPart("hi")}},
	})
	if err == nil {
		t.Fatalf("an unknown role must be refused")
	}
}

func TestSplitSystemJoinsEverySystemMessage(t *testing.T) {
	system, conversation := splitSystem([]llm.Message{
		llm.NewSystemMessage("first"),
		llm.NewSystemMessage("second"),
		llm.NewUserMessage("task"),
	})
	if system != "first\n\nsecond" {
		t.Fatalf("system = %q", system)
	}
	if len(conversation) != 1 {
		t.Fatalf("conversation = %+v", conversation)
	}
}

func TestMapStopReasonCoversTheProtocol(t *testing.T) {
	cases := map[string]llm.FinishReason{
		"end_turn":      llm.FinishReasonStop,
		"stop_sequence": llm.FinishReasonStop,
		"pause_turn":    llm.FinishReasonStop,
		"max_tokens":    llm.FinishReasonLength,
		"tool_use":      llm.FinishReasonToolCalls,
		"refusal":       llm.FinishReasonContentFilter,
		"something_new": llm.FinishReasonStop,
		"":              llm.FinishReasonStop,
	}

	for reason, want := range cases {
		if got := mapStopReason(reason); got != want {
			t.Fatalf("mapStopReason(%q) = %q, want %q", reason, got, want)
		}
	}
}

func TestToToolChoiceMapsEveryMode(t *testing.T) {
	if choice := toToolChoice(llm.ToolChoice{Mode: llm.ToolChoiceAuto}); choice.Type != "auto" {
		t.Fatalf("auto = %+v", choice)
	}
	if choice := toToolChoice(llm.ToolChoice{Mode: llm.ToolChoiceRequired}); choice.Type != "any" {
		t.Fatalf("required = %+v", choice)
	}
	choice := toToolChoice(llm.ToolChoice{Mode: llm.ToolChoiceFunction, Name: "repo.read"})
	if choice.Type != "tool" || choice.Name != "repo.read" {
		t.Fatalf("function = %+v", choice)
	}
	if choice := toToolChoice(llm.ToolChoice{}); choice.Type != "auto" {
		t.Fatalf("zero value = %+v", choice)
	}
}
