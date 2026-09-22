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
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/llm"
	"github.com/pawagents/pawagents/internal/provider"
)

// newTestProvider points a provider at a fake Ollama server.
func newTestProvider(t *testing.T, handler http.Handler) *Provider {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	transport, err := provider.NewTransport(&config.Provider{
		Type:    config.ProviderTypeOllama,
		BaseURL: server.URL,
		Timeout: config.Duration(5 * time.Second),
	})
	if err != nil {
		t.Fatalf("NewTransport() = %v", err)
	}

	instance, err := New(context.Background(), provider.Options{
		Name:      "local-ollama",
		Config:    &config.Provider{Type: config.ProviderTypeOllama, BaseURL: server.URL},
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	ollama, ok := instance.(*Provider)
	if !ok {
		t.Fatalf("New() returned %T", instance)
	}
	return ollama
}

// showHandler answers /api/show with a fixed payload.
func showHandler(capabilities []string, contextLength int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+showPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"details": {"family": "qwen2", "parameter_size": "7.6B"},
			"model_info": {"qwen2.context_length": `+strconv.Itoa(contextLength)+`, "qwen2.embedding_length": 3584},
			"capabilities": `+encodeStrings(capabilities)+`
		}`)
	}
}

func encodeStrings(values []string) string {
	if values == nil {
		return "[]"
	}
	encoded, _ := json.Marshal(values)
	return string(encoded)
}

func TestProviderMetadata(t *testing.T) {
	instance := newTestProvider(t, showHandler(nil, 0))

	if instance.Name() != "local-ollama" {
		t.Fatalf("Name() = %q", instance.Name())
	}
	if instance.Type() != config.ProviderTypeOllama {
		t.Fatalf("Type() = %q", instance.Type())
	}

	info := instance.Info()
	if info.Type != config.ProviderTypeOllama || info.Credential != "none" {
		t.Fatalf("Info() = %+v", info)
	}
	if err := instance.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
}

func TestCapabilitiesDetectToolSupport(t *testing.T) {
	instance := newTestProvider(t, showHandler([]string{"completion", "tools", "vision"}, 32768))

	capabilities, err := instance.Capabilities(context.Background(), "qwen3-coder")
	if err != nil {
		t.Fatalf("Capabilities() = %v", err)
	}
	if !capabilities.ToolCalling {
		t.Fatal("a model advertising the tools capability must report tool calling")
	}
	if !capabilities.Vision {
		t.Fatal("a model advertising vision must report it")
	}
	if capabilities.Reasoning {
		t.Fatal("a model without the thinking capability must not report reasoning")
	}
	if !capabilities.Streaming || !capabilities.SystemMessage || !capabilities.StructuredOutput {
		t.Fatalf("capabilities = %+v", capabilities)
	}
	if capabilities.MaxContextTokens != 32768 {
		t.Fatalf("context = %d, want the value from the model metadata", capabilities.MaxContextTokens)
	}
	if err := capabilities.Validate(llm.Requirements{Tools: true}, "qwen3-coder"); err != nil {
		t.Fatalf("the model should satisfy a tool requirement: %v", err)
	}
}

func TestCapabilitiesWithoutToolSupport(t *testing.T) {
	instance := newTestProvider(t, showHandler([]string{"completion"}, 8192))

	capabilities, err := instance.Capabilities(context.Background(), "llama3.1:8b")
	if err != nil {
		t.Fatalf("Capabilities() = %v", err)
	}
	if capabilities.ToolCalling {
		t.Fatal("a model without the tools capability must not report tool calling")
	}

	// The refusal has to happen as a capability error, never as a silent
	// fallback to a text based tool protocol.
	err = capabilities.Validate(llm.Requirements{Tools: true}, "llama3.1:8b")
	if !apperrors.IsKind(err, apperrors.KindCapability) {
		t.Fatalf("kind = %q, want a capability error", apperrors.KindOf(err))
	}
}

func TestCapabilitiesReportsAMissingModel(t *testing.T) {
	instance := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"model 'qwen3-coder' not found"}`)
	}))

	_, err := instance.Capabilities(context.Background(), "qwen3-coder")
	if !apperrors.IsKind(err, apperrors.KindNotFound) {
		t.Fatalf("kind = %q, want a not found error (%v)", apperrors.KindOf(err), err)
	}
	if !strings.Contains(err.Error(), "ollama pull qwen3-coder") {
		t.Fatalf("error = %q, want a pull hint", err.Error())
	}
}

func TestCapabilitiesFallsBackWhenMetadataIsUnavailable(t *testing.T) {
	instance := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"boom"}`)
	}))

	capabilities, err := instance.Capabilities(context.Background(), "qwen3-coder")
	if err != nil {
		t.Fatalf("Capabilities() = %v, want a conservative fallback", err)
	}
	if !capabilities.Streaming || capabilities.ToolCalling {
		t.Fatalf("capabilities = %+v", capabilities)
	}
	if capabilities.MaxContextTokens != provider.ConservativeContextTokens {
		t.Fatalf("context = %d", capabilities.MaxContextTokens)
	}
}

func TestCapabilitiesCachesMetadata(t *testing.T) {
	calls := 0
	instance := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		showHandler([]string{"completion", "tools"}, 16384).ServeHTTP(w, r)
	}))

	for range 3 {
		if _, err := instance.Capabilities(context.Background(), "qwen3-coder"); err != nil {
			t.Fatalf("Capabilities() = %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("metadata calls = %d, want the result to be cached", calls)
	}
}

func TestCapabilitiesRequiresAModelName(t *testing.T) {
	instance := newTestProvider(t, showHandler(nil, 0))

	if _, err := instance.Capabilities(context.Background(), "  "); err == nil {
		t.Fatal("an empty model name must be rejected")
	}
}

func TestListModelsAndHealth(t *testing.T) {
	instance := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + tagsPath:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"models":[
				{"name":"qwen3-coder:latest"},
				{"name":"gpt-oss:20b"},
				{"model":"legacy-name"},
				{"name":"qwen3-coder:latest"}
			]}`)
		case "/" + versionPath:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"version":"0.12.3"}`)
		default:
			http.NotFound(w, r)
		}
	}))

	models, err := instance.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() = %v", err)
	}
	if strings.Join(models, ",") != "gpt-oss:20b,legacy-name,qwen3-coder:latest" {
		t.Fatalf("models = %v", models)
	}

	health, err := instance.Health(context.Background())
	if err != nil {
		t.Fatalf("Health() = %v", err)
	}
	if !health.Reachable || health.Version != "0.12.3" {
		t.Fatalf("health = %+v", health)
	}

	if got := instance.SuggestModelName(context.Background()); got != "gpt-oss:20b" {
		t.Fatalf("SuggestModelName() = %q", got)
	}
}

func TestHealthReportsAnUnreachableServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := server.URL
	server.Close()

	transport, err := provider.NewTransport(&config.Provider{
		Type:    config.ProviderTypeOllama,
		BaseURL: endpoint,
		Timeout: config.Duration(time.Second),
	})
	if err != nil {
		t.Fatalf("NewTransport() = %v", err)
	}
	instance, err := New(context.Background(), provider.Options{Name: "gone", Transport: transport})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	ollama, ok := instance.(*Provider)
	if !ok {
		t.Fatalf("New() returned %T", instance)
	}

	health, err := ollama.Health(context.Background())
	if err == nil {
		t.Fatal("Health() = nil, want a failure")
	}
	if health.Reachable {
		t.Fatal("an unreachable server must not be reported as reachable")
	}
	if !strings.Contains(err.Error(), "ollama serve") {
		t.Fatalf("error = %q, want an actionable hint", err.Error())
	}
}

func TestContextLengthFromInfo(t *testing.T) {
	tests := []struct {
		name string
		info map[string]any
		want int
	}{
		{name: "nil", info: nil, want: 0},
		{name: "prefixed key", info: map[string]any{"llama.context_length": float64(8192)}, want: 8192},
		{name: "integer form", info: map[string]any{"qwen2.context_length": 32768}, want: 32768},
		{name: "string form", info: map[string]any{"phi3.context_length": "4096"}, want: 4096},
		{
			name: "longest wins",
			info: map[string]any{
				"a.context_length": float64(2048),
				"b.context_length": float64(131072),
			},
			want: 131072,
		},
		{name: "unrelated keys", info: map[string]any{"general.parameter_count": float64(7)}, want: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := contextLengthFromInfo(test.info); got != test.want {
				t.Fatalf("contextLengthFromInfo() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestToInt(t *testing.T) {
	tests := []struct {
		value any
		want  int
	}{
		{value: float64(12), want: 12},
		{value: 12, want: 12},
		{value: int64(12), want: 12},
		{value: json.Number("12"), want: 12},
		{value: json.Number("abc"), want: 0},
		{value: "12", want: 12},
		{value: "abc", want: 0},
		{value: nil, want: 0},
	}

	for _, test := range tests {
		if got := toInt(test.value); got != test.want {
			t.Fatalf("toInt(%#v) = %d, want %d", test.value, got, test.want)
		}
	}
}

func TestMapDoneReason(t *testing.T) {
	tests := map[string]llm.FinishReason{
		"":        llm.FinishReasonUnspecified,
		"stop":    llm.FinishReasonStop,
		"length":  llm.FinishReasonLength,
		"load":    llm.FinishReasonStop,
		"unload":  llm.FinishReasonStop,
		"unknown": llm.FinishReasonStop,
	}

	for input, want := range tests {
		if got := mapDoneReason(input); got != want {
			t.Fatalf("mapDoneReason(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestProviderMessage(t *testing.T) {
	if got := providerMessage([]byte(`{"error":"model not found"}`)); got != "model not found" {
		t.Fatalf("providerMessage() = %q", got)
	}
	if got := providerMessage([]byte("boom\nsecond line")); got != "boom" {
		t.Fatalf("providerMessage() = %q", got)
	}
	if got := providerMessage(nil); got != "" {
		t.Fatalf("providerMessage(nil) = %q", got)
	}
}
