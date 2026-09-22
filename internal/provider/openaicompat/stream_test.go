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
	"errors"
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

// providerTransport builds a transport pointed at a test server.
func providerTransport(t *testing.T, baseURL string) (*provider.Transport, error) {
	t.Helper()
	return provider.NewTransport(&config.Provider{
		Type:    config.ProviderTypeOpenAICompat,
		BaseURL: baseURL,
		Timeout: config.Duration(2 * time.Second),
	})
}

// sseHandler replies with a fixed event stream.
func sseHandler(chunks ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, chunk := range chunks {
			_, _ = io.WriteString(w, chunk)
			if flusher != nil {
				flusher.Flush()
			}
		}
	})
}

func TestGenerateStreamsTextToolCallsAndUsage(t *testing.T) {
	instance, _ := newTestProvider(t, sseHandler(
		": keep-alive\n\n",
		"data: {\"model\":\"qwen3-coder\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"I will \"}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"look at \"}}]}\n\n",
		"event: message\n"+"data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"checking\"}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"repo.read\",\"arguments\":\"{\\\"path\\\":\"}}]}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"main.go\\\"}\"}}]}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n",
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":120,\"completion_tokens\":18,\"total_tokens\":138}}\n\n",
		"data: [DONE]\n\n",
	))

	stream, err := instance.Generate(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}

	response, err := llm.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect() = %v", err)
	}

	if response.Text() != "I will look at " {
		t.Fatalf("text = %q", response.Text())
	}
	if response.Message.ReasoningText() != "checking" {
		t.Fatalf("reasoning = %q", response.Message.ReasoningText())
	}
	if response.Model != "qwen3-coder" {
		t.Fatalf("model = %q", response.Model)
	}
	if response.FinishReason != llm.FinishReasonToolCalls {
		t.Fatalf("finish reason = %q", response.FinishReason)
	}
	if response.Usage.InputTokens != 120 || response.Usage.OutputTokens != 18 || response.Usage.TotalTokens != 138 {
		t.Fatalf("usage = %+v", response.Usage)
	}
	if len(response.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", response.Message.ToolCalls)
	}
	call := response.Message.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "repo.read" || call.Arguments != `{"path":"main.go"}` {
		t.Fatalf("tool call = %+v", call)
	}
}

func TestGenerateHandlesMissingToolCallIndexes(t *testing.T) {
	instance, _ := newTestProvider(t, sseHandler(
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"call_1\",\"function\":{\"name\":\"repo.read\",\"arguments\":\"{\"}}]}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"arguments\":\"}\"}}]}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"call_2\",\"function\":{\"name\":\"git.diff\",\"arguments\":\"{}\"}}]}}]}\n\n",
		"data: [DONE]\n\n",
	))

	stream, err := instance.Generate(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	response, err := llm.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect() = %v", err)
	}

	if len(response.Message.ToolCalls) != 2 {
		t.Fatalf("tool calls = %+v", response.Message.ToolCalls)
	}
	if response.Message.ToolCalls[0].Arguments != "{}" {
		t.Fatalf("arguments = %q, want the fragments merged", response.Message.ToolCalls[0].Arguments)
	}
	if response.Message.ToolCalls[1].Name != "git.diff" {
		t.Fatalf("second call = %+v", response.Message.ToolCalls[1])
	}
	if response.FinishReason != llm.FinishReasonToolCalls {
		t.Fatalf("finish reason = %q, want it inferred", response.FinishReason)
	}
}

func TestGenerateInfersFinishReasonWhenTheStreamEnds(t *testing.T) {
	instance, _ := newTestProvider(t, sseHandler(
		"data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n",
	))

	stream, err := instance.Generate(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	response, err := llm.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect() = %v", err)
	}
	if response.FinishReason != llm.FinishReasonStop {
		t.Fatalf("finish reason = %q", response.FinishReason)
	}
	if response.Text() != "partial" {
		t.Fatalf("text = %q", response.Text())
	}
}

func TestGenerateRejectsMalformedEvents(t *testing.T) {
	instance, _ := newTestProvider(t, sseHandler("data: {not json}\n\n"))

	stream, err := instance.Generate(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	_, err = llm.Collect(context.Background(), stream)
	if !apperrors.IsKind(err, apperrors.KindProvider) {
		t.Fatalf("kind = %q, want a provider error (%v)", apperrors.KindOf(err), err)
	}
}

func TestGenerateReportsMidStreamError(t *testing.T) {
	instance, _ := newTestProvider(t, sseHandler(
		"data: {\"error\":{\"message\":\"model overloaded\"}}\n\n",
	))

	stream, err := instance.Generate(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	_, err = llm.Collect(context.Background(), stream)
	if err == nil || !strings.Contains(err.Error(), "model overloaded") {
		t.Fatalf("error = %v, want the provider message", err)
	}
}

func TestGenerateFallsBackToJSONResponses(t *testing.T) {
	instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A server that ignores `stream: true`.
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"model": "qwen3-coder",
			"choices": [{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": "the answer",
					"tool_calls": [{"id":"call_9","type":"function","function":{"name":"git.status","arguments":""}}]
				},
				"finish_reason": "tool_calls"
			}],
			"usage": {"prompt_tokens": 5, "completion_tokens": 7, "total_tokens": 12}
		}`)
	}))

	stream, err := instance.Generate(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	response, err := llm.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect() = %v", err)
	}

	if response.Text() != "the answer" {
		t.Fatalf("text = %q", response.Text())
	}
	if response.Usage.TotalTokens != 12 {
		t.Fatalf("usage = %+v", response.Usage)
	}
	if len(response.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", response.Message.ToolCalls)
	}
	if response.Message.ToolCalls[0].Arguments != "{}" {
		t.Fatalf("arguments = %q, want an empty object for a missing payload", response.Message.ToolCalls[0].Arguments)
	}
	if response.FinishReason != llm.FinishReasonToolCalls {
		t.Fatalf("finish reason = %q", response.FinishReason)
	}
}

func TestGenerateReportsProviderErrors(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		headers  map[string]string
		wantKind apperrors.Kind
		want     string
	}{
		{
			name:     "unauthorized",
			status:   http.StatusUnauthorized,
			body:     `{"error":{"message":"invalid api key","type":"invalid_request_error"}}`,
			wantKind: apperrors.KindAuthentication,
			want:     "invalid api key",
		},
		{
			name:     "forbidden",
			status:   http.StatusForbidden,
			body:     `{"error":{"message":"region not allowed"}}`,
			wantKind: apperrors.KindAuthentication,
			want:     "region not allowed",
		},
		{
			name:     "rate limited",
			status:   http.StatusTooManyRequests,
			body:     `{"error":{"message":"slow down"}}`,
			headers:  map[string]string{"Retry-After": "30"},
			wantKind: apperrors.KindProvider,
			want:     "slow down",
		},
		{
			name:     "gateway timeout",
			status:   http.StatusGatewayTimeout,
			body:     `{"error":{"message":"upstream timeout"}}`,
			wantKind: apperrors.KindTimeout,
			want:     "upstream timeout",
		},
		{
			name:     "model not found",
			status:   http.StatusNotFound,
			body:     `{"error":{"message":"model does not exist"}}`,
			wantKind: apperrors.KindProvider,
			want:     "model does not exist",
		},
		{
			name:     "server error with html body",
			status:   http.StatusBadGateway,
			body:     "<html>\n<body>bad gateway</body>\n</html>",
			wantKind: apperrors.KindProvider,
			want:     "HTTP 502",
		},
		{
			name:     "bad request",
			status:   http.StatusBadRequest,
			body:     `{"error":{"message":"unsupported parameter: top_k"}}`,
			wantKind: apperrors.KindProvider,
			want:     "unsupported parameter",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for name, value := range test.headers {
					w.Header().Set(name, value)
				}
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

			var classified *apperrors.Error
			if !errors.As(err, &classified) {
				t.Fatal("the error must be classified")
			}
			if status, ok := classified.Details["status"]; !ok || status != test.status {
				t.Fatalf("details = %#v, want the HTTP status", classified.Details)
			}
			if test.status == http.StatusTooManyRequests {
				if retryAfter, ok := classified.Details["retry_after"]; !ok || retryAfter != "30" {
					t.Fatalf("details = %#v, want retry_after", classified.Details)
				}
			}
		})
	}
}

func TestGenerateRetriesWithoutStreamOptions(t *testing.T) {
	attempts := 0
	var sawStreamOptions bool

	instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "stream_options") {
			sawStreamOptions = true
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"unknown field stream_options"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))

	stream, err := instance.Generate(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	response, err := llm.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect() = %v", err)
	}

	if attempts != 2 {
		t.Fatalf("attempts = %d, want the request to be retried once", attempts)
	}
	if !sawStreamOptions {
		t.Fatal("the first attempt must carry stream_options")
	}
	if response.Text() != "ok" {
		t.Fatalf("text = %q", response.Text())
	}
}

func TestGenerateDoesNotRetryUnrelatedBadRequests(t *testing.T) {
	attempts := 0

	instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"messages must not be empty"}}`)
	}))

	if _, err := instance.Generate(context.Background(), testRequest()); err == nil {
		t.Fatal("Generate() = nil, want the provider error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want a single attempt", attempts)
	}
}

func TestGenerateReportsUnreachableEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := server.URL
	server.Close()

	transport, err := providerTransport(t, endpoint)
	if err != nil {
		t.Fatalf("NewTransport() = %v", err)
	}

	instance, err := New(context.Background(), provider.Options{Name: "gone", Transport: transport})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	_, err = instance.Generate(context.Background(), testRequest())
	if !apperrors.IsKind(err, apperrors.KindProvider) {
		t.Fatalf("kind = %q, want a provider error (%v)", apperrors.KindOf(err), err)
	}
}

func TestGenerateReportsCancellation(t *testing.T) {
	instance, _ := newTestProvider(t, sseHandler(
		"data: {\"choices\":[{\"delta\":{\"content\":\"one\"}}]}\n\n",
	))

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := instance.Generate(ctx, testRequest())
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	cancel()

	_, err = llm.Collect(ctx, stream)
	if !errors.Is(err, context.Canceled) && !apperrors.IsKind(err, apperrors.KindCancelled) {
		t.Fatalf("error = %v, want a cancellation", err)
	}
}

func TestCapabilitiesUsesTheModelListing(t *testing.T) {
	t.Run("listed model", func(t *testing.T) {
		instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/models" {
				t.Errorf("path = %q, want the model listing", r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"qwen3-coder"},{"id":"gpt-oss:20b"}]}`)
		}))

		capabilities, err := instance.Capabilities(context.Background(), "qwen3-coder")
		if err != nil {
			t.Fatalf("Capabilities() = %v", err)
		}
		if !capabilities.Streaming || !capabilities.ToolCalling {
			t.Fatalf("capabilities = %+v, want the compatible defaults", capabilities)
		}
		// Parallel tool calls are not assumed for a compatible endpoint: some
		// servers accept only one call per turn.
		if capabilities.ParallelTools {
			t.Fatalf("capabilities = %+v, want parallel tools left unassumed", capabilities)
		}
		if capabilities.MaxContextTokens != provider.ConservativeContextTokens {
			t.Fatalf("context = %d, want the conservative default", capabilities.MaxContextTokens)
		}
	})

	t.Run("dated snapshot", func(t *testing.T) {
		instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"gpt-4o-2024-08-06"}]}`)
		}))

		if _, err := instance.Capabilities(context.Background(), "gpt-4o"); err != nil {
			t.Fatalf("Capabilities() = %v, want a dated snapshot to satisfy the alias", err)
		}
	})

	t.Run("unlisted model is a warning", func(t *testing.T) {
		instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"other-model"}]}`)
		}))

		capabilities, err := instance.Capabilities(context.Background(), "qwen3-coder")
		if err != nil {
			t.Fatalf("Capabilities() = %v, want the model to be tolerated", err)
		}
		if !capabilities.Streaming {
			t.Fatalf("capabilities = %+v", capabilities)
		}
	})

	t.Run("listing unsupported by the endpoint", func(t *testing.T) {
		instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "not found")
		}))

		capabilities, err := instance.Capabilities(context.Background(), "qwen3-coder")
		if err != nil {
			t.Fatalf("Capabilities() = %v, want a fallback instead of a failure", err)
		}
		if !capabilities.Streaming || capabilities.MaxContextTokens == 0 {
			t.Fatalf("capabilities = %+v", capabilities)
		}
	})

	t.Run("rejected credential", func(t *testing.T) {
		instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"bad key"}}`)
		}))

		_, err := instance.Capabilities(context.Background(), "qwen3-coder")
		if !apperrors.IsKind(err, apperrors.KindAuthentication) {
			t.Fatalf("kind = %q, want an authentication error (%v)", apperrors.KindOf(err), err)
		}
	})
}

func TestListModels(t *testing.T) {
	instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"a"},{"id":""},{"id":"b"}]}`)
	}))

	models, err := instance.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() = %v", err)
	}
	if strings.Join(models, ",") != "a,b" {
		t.Fatalf("models = %v", models)
	}
}

func TestListModelsReportsInvalidJSON(t *testing.T) {
	instance, _ := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "not json")
	}))

	_, err := instance.ListModels(context.Background())
	if !apperrors.IsKind(err, apperrors.KindProvider) {
		t.Fatalf("kind = %q", apperrors.KindOf(err))
	}
}

func TestMatchesModel(t *testing.T) {
	models := []string{"Qwen3-Coder", "gpt-4o-2024-08-06", "llama3.1:8b"}

	tests := []struct {
		model string
		want  bool
	}{
		{model: "qwen3-coder", want: true},
		{model: "QWEN3-CODER", want: true},
		{model: "gpt-4o", want: true},
		{model: "llama3.1", want: true},
		{model: "absent", want: false},
		{model: "", want: true},
	}

	for _, test := range tests {
		if got := matchesModel(models, test.model); got != test.want {
			t.Fatalf("matchesModel(%q) = %v, want %v", test.model, got, test.want)
		}
	}
	if matchesModel(nil, "anything") {
		t.Fatal("an empty listing must not match a model")
	}
}

func TestMapFinishReason(t *testing.T) {
	tests := map[string]llm.FinishReason{
		"":                llm.FinishReasonUnspecified,
		"stop":            llm.FinishReasonStop,
		"end_turn":        llm.FinishReasonStop,
		"length":          llm.FinishReasonLength,
		"max_tokens":      llm.FinishReasonLength,
		"tool_calls":      llm.FinishReasonToolCalls,
		"function_call":   llm.FinishReasonToolCalls,
		"content_filter":  llm.FinishReasonContentFilter,
		"vendor_specific": llm.FinishReasonStop,
	}

	for input, want := range tests {
		if got := mapFinishReason(input); got != want {
			t.Fatalf("mapFinishReason(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestToUsage(t *testing.T) {
	if got := toUsage(nil); got != (llm.Usage{}) {
		t.Fatalf("toUsage(nil) = %+v", got)
	}

	usage := toUsage(&chatUsage{
		PromptTokens:            100,
		CompletionTokens:        20,
		TotalTokens:             120,
		PromptTokensDetails:     &chatPromptTokenDetails{CachedTokens: 64},
		CompletionTokensDetails: &chatCompletionDetails{ReasoningTokens: 5},
	})
	if usage.InputTokens != 100 || usage.OutputTokens != 20 || usage.TotalTokens != 120 {
		t.Fatalf("usage = %+v", usage)
	}
	if usage.CacheReadTokens != 64 || usage.ReasoningTokens != 5 {
		t.Fatalf("usage details = %+v", usage)
	}
}

func TestProviderMessageSanitisation(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "openai envelope",
			body: `{"error":{"message":"line one\nline two"}}`,
			want: "line one line two",
		},
		{name: "plain text", body: "boom\nmore", want: "boom"},
		{name: "empty", body: "", want: ""},
		{name: "whitespace", body: "   \n ", want: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := providerMessage([]byte(test.body)); got != test.want {
				t.Fatalf("providerMessage(%q) = %q, want %q", test.body, got, test.want)
			}
		})
	}
}

func TestSanitizeMessageTruncatesLongText(t *testing.T) {
	long := strings.Repeat("x", 1000)
	got := providerMessage([]byte(long))
	if len(got) > 410 {
		t.Fatalf("len = %d, want the message to be truncated", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("message = %q, want a truncation marker", got)
	}
}

func TestIsUnsupportedStreamOptions(t *testing.T) {
	classified := apperrors.New(apperrors.KindProvider, "provider.generate",
		"the provider returned HTTP 400: unknown field stream_options").
		WithDetails(map[string]any{"status": 400})
	if !isUnsupportedStreamOptions(classified) {
		t.Fatal("a 400 about stream_options is retryable")
	}

	other := apperrors.New(apperrors.KindProvider, "provider.generate", "HTTP 400: bad model").
		WithDetails(map[string]any{"status": 400})
	if isUnsupportedStreamOptions(other) {
		t.Fatal("an unrelated 400 must not be retried")
	}

	if isUnsupportedStreamOptions(errors.New("plain")) {
		t.Fatal("an unclassified error must not be retried")
	}
}
