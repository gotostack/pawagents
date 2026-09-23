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

package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pawagents/pawagents/internal/agent"
	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
)

// scriptedEndpoint plays one response body per request and records what it was
// asked, so that a test can assert both directions of the translation.
type scriptedEndpoint struct {
	server   *httptest.Server
	bodies   []string
	mu       sync.Mutex
	requests []string
}

// newScriptedEndpoint starts an endpoint that answers with the given bodies in
// order. The last body is repeated when more requests arrive.
func newScriptedEndpoint(t *testing.T, bodies ...string) *scriptedEndpoint {
	t.Helper()

	endpoint := &scriptedEndpoint{bodies: bodies}
	endpoint.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		// A capability probe lists models; only generations are recorded.
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"fake-model"}]}`)
			return
		}

		endpoint.mu.Lock()
		endpoint.requests = append(endpoint.requests, string(body))
		index := len(endpoint.requests) - 1
		endpoint.mu.Unlock()

		if index >= len(endpoint.bodies) {
			index = len(endpoint.bodies) - 1
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, endpoint.bodies[index])
	}))
	t.Cleanup(endpoint.server.Close)

	return endpoint
}

// Requests returns the generation requests the endpoint received.
func (e *scriptedEndpoint) Requests() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.requests...)
}

// writeProviderRunConfig installs a configuration that points one provider type
// at a test endpoint, and returns the workspace directory.
func writeProviderRunConfig(t *testing.T, providerType, endpoint, baseURL string) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfigPath, "")

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "main.go"),
		[]byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatalf("cannot write the workspace file: %v", err)
	}

	contents := fmt.Sprintf(`
version: 1
providers:
  %s:
    type: %s
    base_url: %s
    api_key: sk-test
models:
  test-model:
    provider: %s
    model: fake-model
agents:
  reviewer:
    model: test-model
    instructions: Review code carefully.
    output_mode: structured
    tools:
      - repo.read
`, endpoint, providerType, baseURL, endpoint)

	path := filepath.Join(home, config.ConfigFileName)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("cannot write the test configuration: %v", err)
	}
	return workspace
}

// anthropicSse renders an Anthropic event stream.
func anthropicSse(events ...map[string]any) string {
	lines := make([]string, 0, len(events))
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			panic(err)
		}
		lines = append(lines, "event: message\ndata: "+string(encoded))
	}
	return strings.Join(lines, "\n\n") + "\n\n"
}

// anthropicAnswer is a complete Anthropic answer.
func anthropicAnswer(text string) string {
	return anthropicSse(
		map[string]any{"type": "message_start", "message": map[string]any{
			"model": "fake-model", "usage": map[string]any{"input_tokens": 40, "output_tokens": 1}}},
		map[string]any{"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "text", "text": ""}},
		map[string]any{"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": text}},
		map[string]any{"type": "message_delta",
			"delta": map[string]any{"stop_reason": "end_turn"},
			"usage": map[string]any{"output_tokens": 18}},
		map[string]any{"type": "message_stop"},
	)
}

// anthropicToolCall is an Anthropic answer that asks for one tool.
func anthropicToolCall(id, name, arguments string) string {
	return anthropicSse(
		map[string]any{"type": "message_start", "message": map[string]any{
			"model": "fake-model", "usage": map[string]any{"input_tokens": 50, "output_tokens": 1}}},
		map[string]any{"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "tool_use", "id": id,
				"name": name, "input": map[string]any{}}},
		map[string]any{"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": arguments}},
		map[string]any{"type": "message_delta",
			"delta": map[string]any{"stop_reason": "tool_use"},
			"usage": map[string]any{"output_tokens": 12}},
		map[string]any{"type": "message_stop"},
	)
}

// responsesSse renders a Responses event stream.
func responsesSse(events ...map[string]any) string {
	lines := make([]string, 0, len(events))
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			panic(err)
		}
		lines = append(lines, "data: "+string(encoded))
	}
	return strings.Join(lines, "\n\n") + "\n\n"
}

// responsesAnswer is a complete Responses answer.
func responsesAnswer(text string) string {
	return responsesSse(
		map[string]any{"type": "response.created", "response": map[string]any{
			"id": "resp_1", "model": "fake-model", "status": "in_progress"}},
		map[string]any{"type": "response.output_text.delta", "output_index": 0,
			"content_index": 0, "delta": text},
		map[string]any{"type": "response.completed", "response": map[string]any{
			"id": "resp_1", "model": "fake-model", "status": "completed",
			"usage": map[string]any{"input_tokens": 44, "output_tokens": 16, "total_tokens": 60}}},
	)
}

// responsesToolCall is a Responses answer that asks for one tool.
func responsesToolCall(callID, name, arguments string) string {
	return responsesSse(
		map[string]any{"type": "response.created", "response": map[string]any{
			"model": "fake-model", "status": "in_progress"}},
		map[string]any{"type": "response.output_item.added", "output_index": 0,
			"item": map[string]any{"type": "function_call", "id": "fc_1",
				"call_id": callID, "name": name, "arguments": ""}},
		map[string]any{"type": "response.function_call_arguments.delta", "output_index": 0,
			"item_id": "fc_1", "delta": arguments},
		map[string]any{"type": "response.completed", "response": map[string]any{
			"model": "fake-model", "status": "completed",
			"usage": map[string]any{"input_tokens": 48, "output_tokens": 10}}},
	)
}

func TestRunThroughTheAnthropicProvider(t *testing.T) {
	endpoint := newScriptedEndpoint(t,
		anthropicToolCall("toolu_1", "repo.read", `{"path":"main.go"}`),
		anthropicAnswer(`{"summary":"The file defines main.","findings":[]}`),
	)

	workspace := writeProviderRunConfig(t, config.ProviderTypeAnthropic,
		"cloud-anthropic", endpoint.server.URL)

	stdout, stderr, code := run(t, "run", "-a", "reviewer", "-t", "Read main.go",
		"-w", workspace, "-o", "json")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}

	var result agent.Result
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("cannot decode the result: %v\n%s", err, stdout)
	}
	if result.Status != agent.StatusCompleted {
		t.Fatalf("status = %q (%s)", result.Status, result.Error)
	}
	if result.Provider != "cloud-anthropic" || result.Model != "fake-model" {
		t.Fatalf("identity = %+v", result)
	}
	if result.Usage.ToolCalls != 1 || result.Usage.Rounds != 2 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if result.Usage.InputTokens != 90 || result.Usage.OutputTokens != 30 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if result.Summary != "The file defines main." {
		t.Fatalf("summary = %q", result.Summary)
	}

	requests := endpoint.Requests()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	// The tool result must travel back inside a user message, which is what
	// the Messages API requires.
	if !strings.Contains(requests[1], `"tool_result"`) ||
		!strings.Contains(requests[1], `"tool_use_id":"toolu_1"`) {
		t.Fatalf("the tool result was not sent as a tool_result block: %s", requests[1])
	}
	if !strings.Contains(requests[1], "package main") {
		t.Fatalf("the tool output did not reach the provider: %s", requests[1])
	}
}

func TestRunThroughTheResponsesProvider(t *testing.T) {
	endpoint := newScriptedEndpoint(t,
		responsesToolCall("call_1", "repo.read", `{"path":"main.go"}`),
		responsesAnswer(`{"summary":"The file defines main.","findings":[]}`),
	)

	workspace := writeProviderRunConfig(t, config.ProviderTypeOpenAIResponses,
		"cloud-openai", endpoint.server.URL+"/v1")

	stdout, stderr, code := run(t, "run", "-a", "reviewer", "-t", "Read main.go",
		"-w", workspace, "-o", "json")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}

	var result agent.Result
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("cannot decode the result: %v\n%s", err, stdout)
	}
	if result.Status != agent.StatusCompleted {
		t.Fatalf("status = %q (%s)", result.Status, result.Error)
	}
	if result.Usage.ToolCalls != 1 || result.Usage.Rounds != 2 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if result.Usage.InputTokens != 92 || result.Usage.OutputTokens != 26 {
		t.Fatalf("usage = %+v", result.Usage)
	}

	requests := endpoint.Requests()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	if !strings.Contains(requests[1], `"type":"function_call_output"`) ||
		!strings.Contains(requests[1], `"call_id":"call_1"`) {
		t.Fatalf("the tool result was not sent as a function_call_output item: %s", requests[1])
	}
	if !strings.Contains(requests[1], "package main") {
		t.Fatalf("the tool output did not reach the provider: %s", requests[1])
	}
	// A read-only advisor must not ask the platform to keep the conversation.
	if strings.Contains(requests[0], `"store":true`) {
		t.Fatalf("the conversation must not be stored: %s", requests[0])
	}
}

func TestRunReportsAProviderRefusal(t *testing.T) {
	endpoint := newScriptedEndpoint(t, responsesSse(
		map[string]any{"type": "response.failed", "response": map[string]any{
			"status": "failed",
			"error": map[string]any{
				"code": "invalid_api_key", "message": "incorrect api key",
			},
		}},
	))

	workspace := writeProviderRunConfig(t, config.ProviderTypeOpenAIResponses,
		"cloud-openai", endpoint.server.URL+"/v1")

	stdout, stderr, code := run(t, "run", "-a", "reviewer", "-t", "Review",
		"-w", workspace, "-o", "json")
	if code != apperrors.ExitAuth {
		t.Fatalf("exit code = %d, want %d (stderr = %s)", code, apperrors.ExitAuth, stderr)
	}

	var result agent.Result
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("cannot decode the result: %v", err)
	}
	if result.Status != agent.StatusFailed {
		t.Fatalf("status = %q", result.Status)
	}
	if !strings.Contains(result.Error, "incorrect api key") {
		t.Fatalf("error = %q", result.Error)
	}
}

func TestAgentShowProbesTheAnthropicProvider(t *testing.T) {
	endpoint := newScriptedEndpoint(t, anthropicAnswer("answer"))
	writeProviderRunConfig(t, config.ProviderTypeAnthropic, "cloud-anthropic", endpoint.server.URL)

	stdout, stderr, code := run(t, "agent", "show", "reviewer", "--probe")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "(probed)") {
		t.Fatalf("the probe result was not reported:\n%s", stdout)
	}
	if !strings.Contains(stdout, "✓ the model satisfies every requirement") {
		t.Fatalf("the probed capabilities must satisfy the agent:\n%s", stdout)
	}
	// Anthropic cannot enforce a JSON schema, so the capability report must say
	// so instead of claiming a feature the endpoint would reject.
	if !strings.Contains(stdout, "structured_output:no") {
		t.Fatalf("anthropic must not claim structured output:\n%s", stdout)
	}
}
