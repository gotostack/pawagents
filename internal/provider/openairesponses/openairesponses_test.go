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

// newTestProvider points a provider at a fake Responses API.
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
		Type:    config.ProviderTypeOpenAIResponses,
		BaseURL: recorder.server.URL + "/v1",
		APIKey:  "sk-test",
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
		Name:      "cloud-openai",
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
		lines = append(lines, "data: "+payload)
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
		event(map[string]any{"type": "response.created",
			"response": map[string]any{"id": "resp_1", "model": "gpt-5", "status": "in_progress"}}),
		event(map[string]any{"type": "response.output_item.added", "output_index": 0,
			"item": map[string]any{"type": "message", "role": "assistant"}}),
		event(map[string]any{"type": "response.output_text.delta", "output_index": 0,
			"content_index": 0, "delta": text}),
		event(map[string]any{"type": "response.output_text.done", "output_index": 0,
			"content_index": 0, "text": text}),
		event(map[string]any{"type": "response.completed",
			"response": map[string]any{
				"id": "resp_1", "model": "gpt-5", "status": "completed",
				"usage": map[string]any{
					"input_tokens":  150,
					"output_tokens": 20,
					"total_tokens":  170,
					"input_tokens_details": map[string]any{
						"cached_tokens": 128,
					},
					"output_tokens_details": map[string]any{
						"reasoning_tokens": 7,
					},
				},
			}}),
	)
}

func TestGenerateStreamsATextAnswer(t *testing.T) {
	instance, recorder := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, textStream("The change looks correct."))
	}))

	stream, err := instance.Generate(context.Background(), &llm.GenerateRequest{
		Model:       "gpt-5",
		MaxTokens:   2048,
		Temperature: floatPtr(0.2),
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
	if response.Usage.InputTokens != 150 || response.Usage.OutputTokens != 20 {
		t.Fatalf("usage = %+v", response.Usage)
	}
	if response.Usage.CacheReadTokens != 128 || response.Usage.ReasoningTokens != 7 {
		t.Fatalf("usage details = %+v", response.Usage)
	}

	request := recorder.requests[0]
	if request.Method != http.MethodPost || request.Path != "/v1/responses" {
		t.Fatalf("request = %+v", request)
	}

	var sent responsesRequest
	if err := json.Unmarshal([]byte(request.Body), &sent); err != nil {
		t.Fatalf("cannot decode the request: %v", err)
	}
	if sent.Instructions != "You review Go code." {
		t.Fatalf("instructions = %q", sent.Instructions)
	}
	if sent.Store {
		t.Fatalf("a read-only advisor must not persist conversations in the vendor account")
	}
	if sent.MaxOutputTokens != 2048 || !sent.Stream {
		t.Fatalf("request = %+v", sent)
	}
	if len(sent.Input) != 1 {
		t.Fatalf("input = %+v", sent.Input)
	}
}

func TestGenerateStreamsAToolCall(t *testing.T) {
	instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse(
			event(map[string]any{"type": "response.created",
				"response": map[string]any{"model": "gpt-5"}}),
			event(map[string]any{"type": "response.output_item.added", "output_index": 1,
				"item": map[string]any{"type": "function_call", "id": "fc_1",
					"call_id": "call_1", "name": "repo.read", "arguments": ""}}),
			event(map[string]any{"type": "response.function_call_arguments.delta",
				"output_index": 1, "item_id": "fc_1", "delta": `{"path":`}),
			event(map[string]any{"type": "response.function_call_arguments.delta",
				"output_index": 1, "item_id": "fc_1", "delta": `"main.go"}`}),
			event(map[string]any{"type": "response.function_call_arguments.done",
				"output_index": 1, "item_id": "fc_1", "arguments": `{"path":"main.go"}`}),
			event(map[string]any{"type": "response.completed",
				"response": map[string]any{
					"id": "resp_2", "model": "gpt-5", "status": "completed",
					"output": []any{map[string]any{"type": "function_call", "call_id": "call_1",
						"name": "repo.read", "arguments": `{"path":"main.go"}`}},
					"usage": map[string]any{"input_tokens": 90, "output_tokens": 15},
				}}),
		))
	}))

	stream, err := instance.Generate(context.Background(), &llm.GenerateRequest{
		Model:    "gpt-5",
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
	if call.ID != "call_1" || call.Name != "repo.read" {
		t.Fatalf("call = %+v", call)
	}
	if call.Arguments != `{"path":"main.go"}` {
		t.Fatalf("arguments = %q", call.Arguments)
	}
	// The terminal event repeats the whole response; the call must not be
	// reported twice.
	if len(response.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", response.Message.ToolCalls)
	}
}

func TestGenerateTranslatesTheToolConversation(t *testing.T) {
	instance, recorder := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, textStream("done"))
	}))

	stream, err := instance.Generate(context.Background(), &llm.GenerateRequest{
		Model: "gpt-5",
		Messages: []llm.Message{
			llm.NewSystemMessage("Review."),
			llm.NewUserMessage("Read both files."),
			llm.NewAssistantToolCallMessage(
				llm.ToolCall{ID: "call_1", Name: "repo.read", Arguments: `{"path":"a.go"}`}),
			llm.NewToolResultMessage(llm.ToolResult{
				ToolCallID: "call_1", Name: "repo.read", Content: "package a"}),
		},
		Tools: []llm.ToolDefinition{{
			Name:        "repo.read",
			InputSchema: llm.ObjectSchema(map[string]llm.JSONSchema{"path": llm.StringSchema()}, "path"),
		}},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	_, _ = llm.Collect(context.Background(), stream)

	var sent responsesRequest
	if err := json.Unmarshal([]byte(recorder.requests[0].Body), &sent); err != nil {
		t.Fatalf("cannot decode the request: %v", err)
	}

	// user message, function_call, function_call_output
	if len(sent.Input) != 3 {
		t.Fatalf("input = %+v", sent.Input)
	}

	call, ok := sent.Input[1].(map[string]any)
	if !ok {
		t.Fatalf("input[1] = %T", sent.Input[1])
	}
	if call["type"] != "function_call" || call["call_id"] != "call_1" || call["name"] != "repo.read" {
		t.Fatalf("function call = %+v", call)
	}
	if call["arguments"] != `{"path":"a.go"}` {
		t.Fatalf("arguments = %+v", call["arguments"])
	}

	output, ok := sent.Input[2].(map[string]any)
	if !ok {
		t.Fatalf("input[2] = %T", sent.Input[2])
	}
	if output["type"] != "function_call_output" || output["call_id"] != "call_1" {
		t.Fatalf("function output = %+v", output)
	}
	if output["output"] != "package a" {
		t.Fatalf("output = %+v", output["output"])
	}

	if len(sent.Tools) != 1 || sent.Tools[0].Type != "function" || sent.Tools[0].Name != "repo.read" {
		t.Fatalf("tools = %+v", sent.Tools)
	}
	if sent.ToolChoice != "auto" {
		t.Fatalf("tool choice = %+v", sent.ToolChoice)
	}
}

func TestGenerateMarksAFailedToolResult(t *testing.T) {
	instance, recorder := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, textStream("done"))
	}))

	stream, err := instance.Generate(context.Background(), &llm.GenerateRequest{
		Model: "gpt-5",
		Messages: []llm.Message{
			llm.NewUserMessage("Read the file."),
			llm.NewAssistantToolCallMessage(
				llm.ToolCall{ID: "call_1", Name: "repo.read", Arguments: `{}`}),
			llm.NewToolResultMessage(llm.ToolResult{
				ToolCallID: "call_1", Name: "repo.read", Content: "no such file", IsError: true}),
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	_, _ = llm.Collect(context.Background(), stream)

	var sent responsesRequest
	if err := json.Unmarshal([]byte(recorder.requests[0].Body), &sent); err != nil {
		t.Fatalf("cannot decode the request: %v", err)
	}

	output := sent.Input[2].(map[string]any)
	if !strings.HasPrefix(output["output"].(string), "[tool error]") {
		t.Fatalf("the API has no error flag, so the failure must be in the text: %+v", output)
	}
}

func TestGenerateStreamsReasoningSummary(t *testing.T) {
	instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse(
			event(map[string]any{"type": "response.created",
				"response": map[string]any{"model": "gpt-5"}}),
			event(map[string]any{"type": "response.reasoning_summary_text.delta",
				"output_index": 0, "delta": "checking the diff"}),
			event(map[string]any{"type": "response.output_text.delta",
				"output_index": 1, "delta": "It is fine."}),
			event(map[string]any{"type": "response.completed",
				"response": map[string]any{"model": "gpt-5", "status": "completed"}}),
		))
	}))

	stream, err := instance.Generate(context.Background(), &llm.GenerateRequest{
		Model:    "gpt-5",
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
	if response.Message.ReasoningText() != "checking the diff" {
		t.Fatalf("reasoning = %q", response.Message.ReasoningText())
	}
}

func TestGenerateSendsASchemaWithoutStrictMode(t *testing.T) {
	instance, recorder := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, textStream(`{"summary":"ok","findings":[]}`))
	}))

	schema := llm.ObjectSchema(map[string]llm.JSONSchema{
		"summary":  llm.StringSchema(),
		"findings": llm.ArraySchema(llm.ObjectSchema(map[string]llm.JSONSchema{"title": llm.StringSchema()})),
	}, "summary", "findings")

	stream, err := instance.Generate(context.Background(), &llm.GenerateRequest{
		Model:              "gpt-5",
		Messages:           []llm.Message{llm.NewUserMessage("Review.")},
		ResponseSchema:     &schema,
		ResponseSchemaName: "pawagents_result",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	_, _ = llm.Collect(context.Background(), stream)

	var sent responsesRequest
	if err := json.Unmarshal([]byte(recorder.requests[0].Body), &sent); err != nil {
		t.Fatalf("cannot decode the request: %v", err)
	}
	if sent.Text == nil || sent.Text.Format == nil {
		t.Fatalf("the schema was dropped: %+v", sent.Text)
	}
	if sent.Text.Format.Type != "json_schema" || sent.Text.Format.Name != "pawagents_result" {
		t.Fatalf("format = %+v", sent.Text.Format)
	}
	if sent.Text.Format.Strict {
		t.Fatalf("strict mode requires every property to be required, which the envelope is not")
	}
	if _, ok := sent.Text.Format.Schema["properties"]; !ok {
		t.Fatalf("schema = %+v", sent.Text.Format.Schema)
	}
}

func TestGenerateReportsAnIncompleteResponse(t *testing.T) {
	instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse(
			event(map[string]any{"type": "response.created",
				"response": map[string]any{"model": "gpt-5"}}),
			event(map[string]any{"type": "response.output_text.delta",
				"output_index": 0, "delta": "partial"}),
			event(map[string]any{"type": "response.incomplete",
				"response": map[string]any{
					"model":              "gpt-5",
					"status":             "incomplete",
					"incomplete_details": map[string]any{"reason": "max_output_tokens"},
					"usage":              map[string]any{"input_tokens": 10, "output_tokens": 4096},
				}}),
		))
	}))

	stream, err := instance.Generate(context.Background(), &llm.GenerateRequest{
		Model:    "gpt-5",
		Messages: []llm.Message{llm.NewUserMessage("Review.")},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	response, err := llm.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if response.Text() != "partial" {
		t.Fatalf("text = %q", response.Text())
	}
	if response.FinishReason != llm.FinishReasonLength {
		t.Fatalf("finish reason = %q", response.FinishReason)
	}
	if response.Usage.OutputTokens != 4096 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}

func TestGenerateReportsAFailedResponse(t *testing.T) {
	instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse(
			event(map[string]any{"type": "response.created",
				"response": map[string]any{"model": "gpt-5"}}),
			event(map[string]any{"type": "response.failed",
				"response": map[string]any{
					"status": "failed",
					"error": map[string]any{
						"code": "invalid_api_key", "message": "incorrect api key",
					},
				}}),
		))
	}))

	stream, err := instance.Generate(context.Background(), &llm.GenerateRequest{
		Model:    "gpt-5",
		Messages: []llm.Message{llm.NewUserMessage("Review.")},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	_, err = llm.Collect(context.Background(), stream)
	if err == nil {
		t.Fatalf("a failed response must surface")
	}
	if kind := apperrors.KindOf(err); kind != apperrors.KindAuthentication {
		t.Fatalf("kind = %s, want %s (%v)", kind, apperrors.KindAuthentication, err)
	}
	if !strings.Contains(err.Error(), "incorrect api key") {
		t.Fatalf("error = %v", err)
	}
}

func TestGenerateHandlesACompleteResponse(t *testing.T) {
	instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id": "resp_3",
			"model": "gpt-5",
			"status": "completed",
			"output": [
				{"type": "reasoning", "summary": [{"type": "summary_text", "text": "checked"}]},
				{"type": "message", "role": "assistant",
				 "content": [{"type": "output_text", "text": "All good."}]},
				{"type": "function_call", "call_id": "call_7", "name": "git.status", "arguments": "{}"}
			],
			"usage": {"input_tokens": 40, "output_tokens": 9, "total_tokens": 49}
		}`)
	}))

	stream, err := instance.Generate(context.Background(), &llm.GenerateRequest{
		Model:    "gpt-5",
		Messages: []llm.Message{llm.NewUserMessage("Status?")},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	response, err := llm.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if response.Text() != "All good." {
		t.Fatalf("text = %q", response.Text())
	}
	if response.Message.ReasoningText() != "checked" {
		t.Fatalf("reasoning = %q", response.Message.ReasoningText())
	}
	if !response.HasToolCalls() || response.Message.ToolCalls[0].ID != "call_7" {
		t.Fatalf("tool calls = %+v", response.Message.ToolCalls)
	}
	if response.FinishReason != llm.FinishReasonToolCalls {
		t.Fatalf("finish reason = %q", response.FinishReason)
	}
	if response.Usage.TotalTokens != 49 {
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
			body:   `{"error":{"message":"incorrect api key","code":"invalid_api_key"}}`,
			want:   apperrors.KindAuthentication,
		},
		{
			name:   "missing model",
			status: http.StatusNotFound,
			body:   `{"error":{"message":"model not found"}}`,
			want:   apperrors.KindNotFound,
		},
		{
			name:   "server error",
			status: http.StatusBadGateway,
			body:   `{"error":{"message":"bad gateway"}}`,
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
				Model:    "gpt-5",
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
			event(map[string]any{"type": "response.created",
				"response": map[string]any{"model": "gpt-5"}}),
			event(map[string]any{"type": "error", "code": "server_error",
				"message": "the server had a problem"}),
		))
	}))

	stream, err := instance.Generate(context.Background(), &llm.GenerateRequest{
		Model:    "gpt-5",
		Messages: []llm.Message{llm.NewUserMessage("Review.")},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	_, err = llm.Collect(context.Background(), stream)
	if err == nil {
		t.Fatalf("a mid-stream error must surface")
	}
	if !strings.Contains(err.Error(), "server had a problem") {
		t.Fatalf("error = %v", err)
	}
}

func TestCapabilitiesAndHealth(t *testing.T) {
	instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"data":[{"id":"gpt-5"},{"id":"gpt-5-mini"}]}`)
			return
		}
		_, _ = io.WriteString(w, textStream("done"))
	}))

	capabilities, err := instance.Capabilities(context.Background(), "gpt-5")
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if !capabilities.ToolCalling || !capabilities.StructuredOutput || !capabilities.Reasoning {
		t.Fatalf("capabilities = %+v", capabilities)
	}

	health, err := instance.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !health.Reachable || !strings.Contains(health.Detail, "2 models") {
		t.Fatalf("health = %+v", health)
	}
}

func TestCapabilitiesSurfaceAnAuthenticationFailure(t *testing.T) {
	instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"incorrect api key"}}`)
	}))

	_, err := instance.Capabilities(context.Background(), "gpt-5")
	if err == nil {
		t.Fatalf("a rejected credential must not be hidden")
	}
	if kind := apperrors.KindOf(err); kind != apperrors.KindAuthentication {
		t.Fatalf("kind = %s", kind)
	}
}

func TestOrganizationHeader(t *testing.T) {
	instance, recorder := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, textStream("done"))
	}), func(cfg *config.Provider) {
		cfg.Organization = "org_123"
	})

	stream, err := instance.Generate(context.Background(), &llm.GenerateRequest{
		Model:    "gpt-5",
		Messages: []llm.Message{llm.NewUserMessage("Review.")},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	_, _ = llm.Collect(context.Background(), stream)

	if got := recorder.requests[0].Headers.Get("OpenAI-Organization"); got != "org_123" {
		t.Fatalf("organization header = %q", got)
	}
}

func TestInfoDescribesTheEndpoint(t *testing.T) {
	instance, _ := newTestProvider(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	if instance.Name() != "cloud-openai" || instance.Type() != providerType {
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

func TestToInputItemsRejectsUnsupportedInput(t *testing.T) {
	if _, err := toInputItems([]llm.Message{{Role: "developer"}}); err == nil {
		t.Fatalf("an unknown role must be refused")
	}
	if _, err := toInputItems(nil); err == nil {
		t.Fatalf("an empty conversation must be refused")
	}
}

func TestToolChoiceMapping(t *testing.T) {
	cases := map[llm.ToolChoiceMode]any{
		llm.ToolChoiceAuto:     "auto",
		llm.ToolChoiceNone:     "none",
		llm.ToolChoiceRequired: "required",
		"":                     "auto",
		"made-up":              "auto",
	}

	for mode, want := range cases {
		if got := toToolChoice(llm.ToolChoice{Mode: mode}); got != want {
			t.Fatalf("toToolChoice(%q) = %v, want %v", mode, got, want)
		}
	}

	named := toToolChoice(llm.ToolChoice{Mode: llm.ToolChoiceFunction, Name: "repo.read"})
	object, ok := named.(map[string]any)
	if !ok || object["type"] != "function" || object["name"] != "repo.read" {
		t.Fatalf("named choice = %+v", named)
	}
}

func TestMapIncompleteReason(t *testing.T) {
	if got := mapIncompleteReason("max_output_tokens"); got != llm.FinishReasonLength {
		t.Fatalf("reason = %q", got)
	}
	if got := mapIncompleteReason("content_filter"); got != llm.FinishReasonContentFilter {
		t.Fatalf("reason = %q", got)
	}
	if got := mapIncompleteReason("something new"); got != llm.FinishReasonLength {
		t.Fatalf("an unknown reason means the answer stopped early: %q", got)
	}
}

// floatPtr returns a pointer to a float, for optional request fields.
func floatPtr(value float64) *float64 { return &value }
