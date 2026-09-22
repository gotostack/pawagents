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

package ollama

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/llm"
)

// ndjsonHandler replies with a fixed newline delimited JSON stream.
func ndjsonHandler(lines ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, line := range lines {
			_, _ = io.WriteString(w, line+"\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

func testRequest() *llm.GenerateRequest {
	return &llm.GenerateRequest{
		Model:    "qwen3-coder",
		Messages: []llm.Message{llm.NewUserMessage("review main.go")},
	}
}

func TestGenerateStreamsTextThinkingAndUsage(t *testing.T) {
	var received map[string]any

	instance := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+chatPath {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &received); err != nil {
			t.Errorf("cannot decode the request body: %v", err)
		}
		ndjsonHandler(
			`{"model":"qwen3-coder","message":{"role":"assistant","thinking":"let me look"},"done":false}`,
			`{"model":"qwen3-coder","message":{"role":"assistant","content":"Found "},"done":false}`,
			`{"model":"qwen3-coder","message":{"role":"assistant","content":"one issue."},"done":false}`,
			`{"model":"qwen3-coder","message":{"role":"assistant","content":""},"done":true,`+
				`"done_reason":"stop","prompt_eval_count":120,"eval_count":18}`,
		).ServeHTTP(w, r)
	}))

	stream, err := instance.Generate(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	response, err := llm.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect() = %v", err)
	}

	if response.Text() != "Found one issue." {
		t.Fatalf("text = %q", response.Text())
	}
	if response.Message.ReasoningText() != "let me look" {
		t.Fatalf("reasoning = %q", response.Message.ReasoningText())
	}
	if response.FinishReason != llm.FinishReasonStop {
		t.Fatalf("finish reason = %q", response.FinishReason)
	}
	if response.Usage.InputTokens != 120 || response.Usage.OutputTokens != 18 {
		t.Fatalf("usage = %+v", response.Usage)
	}
	if received["stream"] != true {
		t.Fatalf("stream = %v, want true", received["stream"])
	}
	if received["keep_alive"] != keepAlive {
		t.Fatalf("keep_alive = %v", received["keep_alive"])
	}
}

func TestGenerateMapsTheRequest(t *testing.T) {
	var received map[string]any

	instance := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		ndjsonHandler(`{"message":{"content":"ok"},"done":true,"done_reason":"stop"}`).ServeHTTP(w, r)
	}))

	call, err := llm.NewToolCall("call_1", "repo.read", map[string]any{"path": "main.go"})
	if err != nil {
		t.Fatalf("NewToolCall() = %v", err)
	}
	schema := llm.ObjectSchema(map[string]llm.JSONSchema{"summary": llm.StringSchema()}, "summary")
	temperature := 0.3

	request := &llm.GenerateRequest{
		Model: "qwen3-coder",
		Messages: []llm.Message{
			llm.NewSystemMessage("you review code"),
			llm.NewUserMessage("review main.go"),
			llm.NewAssistantToolCallMessage(call),
			llm.NewToolResultMessage(llm.ToolResult{
				ToolCallID: "call_1", Name: "repo.read", Content: "package main", IsError: true,
			}),
		},
		Tools: []llm.ToolDefinition{{
			Name:        "repo.read",
			Description: "Read a file.",
			InputSchema: llm.ObjectSchema(map[string]llm.JSONSchema{"path": llm.StringSchema()}, "path"),
		}},
		MaxTokens:          2048,
		Temperature:        &temperature,
		Stop:               []string{"</file>"},
		ResponseSchema:     &schema,
		ResponseSchemaName: "review_result",
		Extra:              map[string]any{"think": false},
	}

	stream, err := instance.Generate(context.Background(), request)
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	if _, err := llm.Collect(context.Background(), stream); err != nil {
		t.Fatalf("Collect() = %v", err)
	}

	messages, ok := received["messages"].([]any)
	if !ok || len(messages) != 4 {
		t.Fatalf("messages = %v", received["messages"])
	}
	if messages[0].(map[string]any)["role"] != "system" {
		t.Fatalf("messages[0] = %v", messages[0])
	}

	assistant := messages[2].(map[string]any)
	calls := assistant["tool_calls"].([]any)
	arguments := calls[0].(map[string]any)["function"].(map[string]any)["arguments"]
	// Ollama expects an object, not an encoded string.
	object, ok := arguments.(map[string]any)
	if !ok || object["path"] != "main.go" {
		t.Fatalf("arguments = %#v, want a JSON object", arguments)
	}

	toolMessage := messages[3].(map[string]any)
	if toolMessage["role"] != "tool" || toolMessage["tool_name"] != "repo.read" {
		t.Fatalf("tool message = %v", toolMessage)
	}
	if content, _ := toolMessage["content"].(string); !strings.HasPrefix(content, "[tool error] ") {
		t.Fatalf("tool content = %q", content)
	}

	options, ok := received["options"].(map[string]any)
	if !ok {
		t.Fatalf("options = %v", received["options"])
	}
	if options["num_predict"] != float64(2048) || options["temperature"] != 0.3 {
		t.Fatalf("options = %v", options)
	}
	if len(options["stop"].([]any)) != 1 {
		t.Fatalf("stop = %v", options["stop"])
	}

	if _, ok := received["format"].(map[string]any); !ok {
		t.Fatalf("format = %v, want the response schema", received["format"])
	}

	tools, ok := received["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %v", received["tools"])
	}
	if tools[0].(map[string]any)["type"] != "function" {
		t.Fatalf("tools[0] = %v", tools[0])
	}

	if received["think"] != false {
		t.Fatalf("think = %v, want the per-request extra to be merged", received["think"])
	}
}

func TestGenerateSendsImagesAsBase64(t *testing.T) {
	var received map[string]any

	instance := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		ndjsonHandler(`{"message":{"content":"ok"},"done":true}`).ServeHTTP(w, r)
	}))

	request := &llm.GenerateRequest{
		Model: "llava",
		Messages: []llm.Message{{
			Role: llm.RoleUser,
			Content: []llm.ContentPart{
				llm.TextPart("what is this?"),
				llm.ImageDataPart("image/png", []byte{1, 2, 3}),
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
	images, ok := messages[0].(map[string]any)["images"].([]any)
	if !ok || len(images) != 1 {
		t.Fatalf("images = %v", messages[0].(map[string]any)["images"])
	}
	encoded := images[0].(string)
	if strings.HasPrefix(encoded, "data:") {
		t.Fatalf("image = %q, want bare base64 without a data URL prefix", encoded)
	}
	if encoded != "AQID" {
		t.Fatalf("image = %q", encoded)
	}
}

func TestGenerateRejectsRemoteImageURLs(t *testing.T) {
	instance := newTestProvider(t, ndjsonHandler(`{"message":{"content":"ok"},"done":true}`))

	request := &llm.GenerateRequest{
		Model: "llava",
		Messages: []llm.Message{{
			Role:    llm.RoleUser,
			Content: []llm.ContentPart{llm.ImageURLPart("https://example.com/x.png")},
		}},
	}

	_, err := instance.Generate(context.Background(), request)
	if err == nil {
		t.Fatal("a local server cannot fetch an image URL, so this must fail")
	}
	if !apperrors.IsKind(err, apperrors.KindCapability) {
		t.Fatalf("kind = %q, want a capability error", apperrors.KindOf(err))
	}
	if !strings.Contains(err.Error(), "cannot fetch an image URL") {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestGenerateStreamsToolCalls(t *testing.T) {
	var received map[string]any

	instance := newTestProvider(t, func() http.HandlerFunc {
		handler := ndjsonHandler(
			`{"model":"qwen3-coder","message":{"role":"assistant","content":""},"done":false}`,
			`{"model":"qwen3-coder","message":{"role":"assistant","content":"","tool_calls":[`+
				`{"function":{"name":"repo.read","arguments":{"path":"main.go"}}},`+
				`{"function":{"name":"git.diff","arguments":{}}}]},`+
				`"done":true,"done_reason":"stop","prompt_eval_count":50,"eval_count":9}`,
		)
		return func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &received)
			handler.ServeHTTP(w, r)
		}
	}())

	stream, err := instance.Generate(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	response, err := llm.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect() = %v", err)
	}

	calls := response.Message.ToolCalls
	if len(calls) != 2 {
		t.Fatalf("tool calls = %+v", calls)
	}
	if calls[0].Name != "repo.read" || calls[0].Arguments != `{"path":"main.go"}` {
		t.Fatalf("calls[0] = %+v", calls[0])
	}
	if calls[1].Name != "git.diff" || calls[1].Arguments != `{}` {
		t.Fatalf("calls[1] = %+v", calls[1])
	}
	// Ollama does not assign identifiers, so the adapter must synthesise
	// stable ones for the tool results to match.
	if calls[0].ID == "" || calls[0].ID == calls[1].ID {
		t.Fatalf("synthesised identifiers = %q, %q", calls[0].ID, calls[1].ID)
	}
	if response.FinishReason != llm.FinishReasonToolCalls {
		t.Fatalf("finish reason = %q", response.FinishReason)
	}
	if response.Usage.TotalTokens != 59 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}

func TestGenerateDoesNotRepeatTheFinalContent(t *testing.T) {
	// Some server versions echo the accumulated content in the final object.
	instance := newTestProvider(t, ndjsonHandler(
		`{"message":{"content":"hello "},"done":false}`,
		`{"message":{"content":"world"},"done":false}`,
		`{"message":{"content":"hello world"},"done":true,"done_reason":"stop"}`,
	))

	stream, err := instance.Generate(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	response, err := llm.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect() = %v", err)
	}
	if response.Text() != "hello world" {
		t.Fatalf("text = %q, want the content exactly once", response.Text())
	}
}

func TestGenerateHandlesANonStreamingFinalObject(t *testing.T) {
	// An older server that answers with a single object still works.
	instance := newTestProvider(t, ndjsonHandler(
		`{"model":"qwen3-coder","message":{"role":"assistant","content":"the whole answer"},`+
			`"done":true,"done_reason":"stop","prompt_eval_count":5,"eval_count":4}`,
	))

	stream, err := instance.Generate(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	response, err := llm.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect() = %v", err)
	}
	if response.Text() != "the whole answer" {
		t.Fatalf("text = %q", response.Text())
	}
	if response.Usage.OutputTokens != 4 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}

func TestGenerateMapsTheLengthStopReason(t *testing.T) {
	instance := newTestProvider(t, ndjsonHandler(
		`{"message":{"content":"truncated"},"done":true,"done_reason":"length"}`,
	))

	stream, err := instance.Generate(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	response, err := llm.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect() = %v", err)
	}
	if !response.IsTruncated() {
		t.Fatalf("finish reason = %q, want a length stop", response.FinishReason)
	}
}

func TestGenerateReportsMidStreamErrors(t *testing.T) {
	instance := newTestProvider(t, ndjsonHandler(
		`{"message":{"content":"partial"},"done":false}`,
		`{"error":"model runner has unexpectedly stopped"}`,
	))

	stream, err := instance.Generate(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	_, err = llm.Collect(context.Background(), stream)
	if !apperrors.IsKind(err, apperrors.KindProvider) {
		t.Fatalf("kind = %q, want a provider error", apperrors.KindOf(err))
	}
	if !strings.Contains(err.Error(), "model runner") {
		t.Fatalf("error = %q, want the server message", err.Error())
	}
}

func TestGenerateRejectsMalformedStreams(t *testing.T) {
	instance := newTestProvider(t, ndjsonHandler("not json at all"))

	stream, err := instance.Generate(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	if _, err := llm.Collect(context.Background(), stream); !apperrors.IsKind(err, apperrors.KindProvider) {
		t.Fatalf("error = %v, want a provider error", err)
	}
}

func TestGenerateReportsServerErrors(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		wantKind apperrors.Kind
		want     string
	}{
		{
			name:     "model missing",
			status:   http.StatusNotFound,
			body:     `{"error":"model 'absent' not found"}`,
			wantKind: apperrors.KindNotFound,
			want:     "not found",
		},
		{
			name:     "unauthorized proxy",
			status:   http.StatusUnauthorized,
			body:     `{"error":"missing token"}`,
			wantKind: apperrors.KindAuthentication,
			want:     "rejected the credential",
		},
		{
			name:     "server error",
			status:   http.StatusInternalServerError,
			body:     `{"error":"runner crashed"}`,
			wantKind: apperrors.KindProvider,
			want:     "runner crashed",
		},
		{
			name:     "gateway timeout",
			status:   http.StatusGatewayTimeout,
			body:     `{"error":"timed out"}`,
			wantKind: apperrors.KindTimeout,
			want:     "timed out",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			instance := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))

			_, err := instance.Generate(context.Background(), testRequest())
			if err == nil {
				t.Fatalf("Generate() = nil, want %q", test.want)
			}
			if !apperrors.IsKind(err, test.wantKind) {
				t.Fatalf("kind = %q, want %q (%v)", apperrors.KindOf(err), test.wantKind, err)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %q, want it to contain %q", err.Error(), test.want)
			}
		})
	}
}

func TestGenerateRejectsInvalidRequests(t *testing.T) {
	instance := newTestProvider(t, ndjsonHandler(`{"message":{"content":"ok"},"done":true}`))

	if _, err := instance.Generate(context.Background(), nil); err == nil {
		t.Fatal("a nil request must be rejected")
	}
	if _, err := instance.Generate(context.Background(), &llm.GenerateRequest{}); err == nil {
		t.Fatal("a request without a model must be rejected")
	}

	badCall := llm.ToolCall{ID: "c1", Name: "repo.read", Arguments: "not json"}
	request := &llm.GenerateRequest{
		Model:    "qwen3-coder",
		Messages: []llm.Message{llm.NewAssistantToolCallMessage(badCall)},
	}
	if _, err := instance.Generate(context.Background(), request); err == nil {
		t.Fatal("tool call arguments that are not a JSON object must be rejected")
	}
}

func TestGenerateReportsCancellation(t *testing.T) {
	instance := newTestProvider(t, ndjsonHandler(`{"message":{"content":"one"},"done":false}`))

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := instance.Generate(ctx, testRequest())
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	cancel()

	if _, err := llm.Collect(ctx, stream); !apperrors.IsKind(err, apperrors.KindCancelled) {
		t.Fatalf("kind = %q, want a cancelled error", apperrors.KindOf(err))
	}
}
