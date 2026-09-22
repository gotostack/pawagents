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

// sseRecorder is a test endpoint that plays a scripted answer per request and
// records what it was asked.
type sseRecorder struct {
	server   *httptest.Server
	bodies   []string
	mu       sync.Mutex
	requests []string
}

// newSSEServer starts an OpenAI-compatible endpoint that answers with the given
// bodies, in order. The last body is repeated when more requests arrive.
//
// The endpoint also answers the model listing the provider probes before a
// task, so that the probe reports the model as installed.
func newSSEServer(t *testing.T, bodies ...string) *sseRecorder {
	t.Helper()

	recorder := &sseRecorder{bodies: bodies}
	recorder.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"fake-model"}]}`)
			return
		}

		recorder.mu.Lock()
		recorder.requests = append(recorder.requests, string(body))
		index := len(recorder.requests) - 1
		recorder.mu.Unlock()

		if index >= len(recorder.bodies) {
			index = len(recorder.bodies) - 1
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, recorder.bodies[index])
	}))
	t.Cleanup(recorder.server.Close)

	return recorder
}

// Requests returns the completion requests the endpoint was sent. The model
// listing probe is excluded: a test asserts on the conversation, not on the
// capability probe.
func (r *sseRecorder) Requests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.requests...)
}

// sse joins chunk lines into an event stream.
func sse(chunks ...string) string {
	return strings.Join(chunks, "\n\n") + "\n\n"
}

// chunk renders one "data:" line from a payload.
func chunk(payload map[string]any) string {
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return "data: " + string(encoded)
}

// textAnswer streams a complete answer.
func textAnswer(text string) string {
	return sse(
		chunk(map[string]any{
			"model":   "fake-model",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": text}}},
		}),
		chunk(map[string]any{
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
		}),
		chunk(map[string]any{
			"choices": []any{},
			"usage":   map[string]any{"prompt_tokens": 12, "completion_tokens": 8, "total_tokens": 20},
		}),
		"data: [DONE]",
	)
}

// toolCall streams a request for one tool.
func toolCall(id, name, arguments string) string {
	return sse(
		chunk(map[string]any{
			"model": "fake-model",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{
				"tool_calls": []any{map[string]any{
					"index":    0,
					"id":       id,
					"type":     "function",
					"function": map[string]any{"name": name, "arguments": arguments},
				}},
			}}},
		}),
		chunk(map[string]any{
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}},
		}),
		chunk(map[string]any{
			"choices": []any{},
			"usage":   map[string]any{"prompt_tokens": 30, "completion_tokens": 5, "total_tokens": 35},
		}),
		"data: [DONE]",
	)
}

// writeRunConfig installs a configuration pointing at a test endpoint and
// returns the workspace directory.
func writeRunConfig(t *testing.T, endpoint, agentBlock string) string {
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
  local:
    type: openai-compatible
    base_url: %s/v1
    api_key: sk-test
models:
  test-model:
    provider: local
    model: fake-model
agents:
%s
`, endpoint, agentBlock)

	path := filepath.Join(home, config.ConfigFileName)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("cannot write the test configuration: %v", err)
	}
	return workspace
}

func TestRunPrintsAStructuredReport(t *testing.T) {
	answer := `{"summary":"One problem found.","findings":[{"severity":"high","category":"correctness","file":"main.go","line":4,"title":"Missing note","description":"The file has no doc comment.","suggestion":"Add one."}]}`
	server := newSSEServer(t, textAnswer(answer))

	workspace := writeRunConfig(t, server.server.URL, `  reviewer:
    model: test-model
    instructions: Review code carefully.
    output_mode: structured
    tools:
      - repo.read`)

	stdout, stderr, code := run(t, "run", "--agent", "reviewer", "--task", "Review the workspace", "--workspace", workspace)
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}

	for _, want := range []string{
		"status:   completed",
		"agent:    reviewer",
		"model:    local/fake-model",
		"findings (1)",
		"Missing note",
		"main.go:4",
		"Add one.",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("the report is missing %q:\n%s", want, stdout)
		}
	}

	if len(server.Requests()) != 1 {
		t.Fatalf("requests = %d, want 1", len(server.Requests()))
	}
	if !strings.Contains(server.Requests()[0], "Review the workspace") {
		t.Fatalf("the task did not reach the provider: %s", server.Requests()[0])
	}
}

func TestRunPrintsJSON(t *testing.T) {
	server := newSSEServer(t, textAnswer(`{"summary":"Nothing to report.","findings":[]}`))
	workspace := writeRunConfig(t, server.server.URL, `  reviewer:
    model: test-model
    instructions: Review code carefully.
    output_mode: structured`)

	stdout, stderr, code := run(t, "run", "-a", "reviewer", "-t", "Review", "-w", workspace, "-o", "json")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}

	var result agent.Result
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("cannot decode the result: %v\n%s", err, stdout)
	}
	if result.Status != agent.StatusCompleted {
		t.Fatalf("status = %q", result.Status)
	}
	if result.Agent != "reviewer" || result.Provider != "local" || result.Model != "fake-model" {
		t.Fatalf("identity = %+v", result)
	}
	if result.Summary != "Nothing to report." {
		t.Fatalf("summary = %q", result.Summary)
	}
	if result.Usage.InputTokens != 12 || result.Usage.OutputTokens != 8 || result.Usage.Rounds != 1 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if result.StartedAt.IsZero() || result.FinishedAt.IsZero() {
		t.Fatalf("timestamps were not recorded: %+v", result)
	}
}

func TestRunExecutesAToolThroughTheWholeStack(t *testing.T) {
	server := newSSEServer(t,
		toolCall("call_1", "repo.read", `{"path":"main.go"}`),
		textAnswer(`{"summary":"The file defines main.","findings":[]}`),
	)

	workspace := writeRunConfig(t, server.server.URL, `  reviewer:
    model: test-model
    instructions: Review code carefully.
    output_mode: structured
    tools:
      - repo.read`)

	stdout, stderr, code := run(t, "run", "-a", "reviewer", "-t", "Read main.go", "-w", workspace, "-o", "json")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}

	var result agent.Result
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("cannot decode the result: %v", err)
	}
	if result.Usage.ToolCalls != 1 || result.Usage.Rounds != 2 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if result.Summary != "The file defines main." {
		t.Fatalf("summary = %q", result.Summary)
	}

	requests := server.Requests()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	// The second request must carry the file the tool read, which is the proof
	// that the grant, the workspace guard and the tool runtime all ran.
	if !strings.Contains(requests[1], "package main") {
		t.Fatalf("the tool result did not reach the provider: %s", requests[1])
	}
	if !strings.Contains(requests[0], `"tools"`) {
		t.Fatalf("the granted tool was not offered: %s", requests[0])
	}
}

func TestRunReportsAPermissionFailure(t *testing.T) {
	// The agent grants repo.read, so a call to git.diff must be refused before
	// the tool is looked up.
	server := newSSEServer(t, toolCall("call_1", "git.diff", `{}`))

	workspace := writeRunConfig(t, server.server.URL, `  reviewer:
    model: test-model
    instructions: Review code carefully.
    output_mode: structured
    tools:
      - repo.read`)

	stdout, stderr, code := run(t, "run", "-a", "reviewer", "-t", "Review", "-w", workspace, "-o", "json")
	if code != apperrors.ExitPermission {
		t.Fatalf("exit code = %d, want %d (stderr = %s)", code, apperrors.ExitPermission, stderr)
	}

	var result agent.Result
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("cannot decode the result: %v", err)
	}
	if result.Status != agent.StatusFailed {
		t.Fatalf("status = %q", result.Status)
	}
	if !strings.Contains(result.Error, "git.diff") {
		t.Fatalf("error = %q", result.Error)
	}
	if !strings.Contains(stderr, "permission") && !strings.Contains(stderr, "Permission") {
		t.Fatalf("stderr = %s", stderr)
	}
}

func TestRunRejectsBadInput(t *testing.T) {
	server := newSSEServer(t, textAnswer("answer"))
	workspace := writeRunConfig(t, server.server.URL, `  reviewer:
    model: test-model
    instructions: Review code carefully.`)

	cases := []struct {
		name string
		args []string
		code int
	}{
		{
			name: "unknown agent",
			args: []string{"run", "-a", "nope", "-t", "Review", "-w", workspace},
			code: apperrors.ExitNotFound,
		},
		{
			name: "missing task",
			args: []string{"run", "-a", "reviewer", "-w", workspace},
			code: apperrors.ExitUsage,
		},
		{
			name: "unsupported output format",
			args: []string{"run", "-a", "reviewer", "-t", "Review", "-w", workspace, "-o", "yaml"},
			code: apperrors.ExitUsage,
		},
		{
			name: "missing workspace",
			args: []string{"run", "-a", "reviewer", "-t", "Review", "-w", filepath.Join(workspace, "nope")},
			code: apperrors.ExitWorkspace,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, stderr, code := run(t, testCase.args...)
			if code != testCase.code {
				t.Fatalf("exit code = %d, want %d (stderr = %s)", code, testCase.code, stderr)
			}
			if strings.TrimSpace(stderr) == "" {
				t.Fatalf("a failure must be explained on stderr")
			}
		})
	}

	if len(server.Requests()) != 0 {
		t.Fatalf("no request may be sent for an invalid invocation")
	}
}

func TestRunReadsTheTaskFromStandardInput(t *testing.T) {
	server := newSSEServer(t, textAnswer("answer"))
	workspace := writeRunConfig(t, server.server.URL, `  reviewer:
    model: test-model
    instructions: Review code carefully.`)

	// The task is read from the process standard input, which the test drives
	// through a pipe.
	original := os.Stdin
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("cannot create a pipe: %v", err)
	}
	os.Stdin = read
	t.Cleanup(func() { os.Stdin = original })

	go func() {
		defer func() { _ = write.Close() }()
		_, _ = io.WriteString(write, "Review from a pipe\n")
	}()

	_, stderr, code := run(t, "run", "-a", "reviewer", "-t", "-", "-w", workspace)
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	if requests := server.Requests(); len(requests) != 1 || !strings.Contains(requests[0], "Review from a pipe") {
		t.Fatalf("the piped task did not reach the provider: %v", requests)
	}
}
