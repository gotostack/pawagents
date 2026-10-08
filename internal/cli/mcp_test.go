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
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pawagents/pawagents/internal/agent"
	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/mcpserver"
)

// connectMCP builds the MCP server the way `pagent mcp serve` does and connects
// an in-memory client to it, which is the closest a test can get to a host
// without spawning a process.
func connectMCP(t *testing.T) *mcp.ClientSession {
	t.Helper()

	app := NewApp()
	cfg, err := app.Config()
	if err != nil {
		t.Fatalf("cannot load the test configuration: %v", err)
	}

	server, closeRegistry, err := newMCPServer(app, cfg)
	if err != nil {
		t.Fatalf("cannot build the MCP server: %v", err)
	}
	t.Cleanup(closeRegistry)

	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := server.MCP().Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test-host", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	return session
}

// mcpText returns the text content of a tool result.
func mcpText(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if textContent, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, textContent.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func TestMCPServeRequiresATransport(t *testing.T) {
	writeRunConfig(t, "http://127.0.0.1:1", `  reviewer:
    model: test-model
    instructions: Review code carefully.`)

	_, stderr, code := run(t, "mcp", "serve")
	if code != apperrors.ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, apperrors.ExitUsage)
	}
	if !strings.Contains(stderr, "no transport selected") {
		t.Fatalf("stderr = %q, want an explanation", stderr)
	}
}

func TestMCPCommandListsItsTools(t *testing.T) {
	stdout, _, code := run(t, "mcp", "--help")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, want 0", code)
	}
	for _, want := range []string{mcpserver.ToolListAgents, mcpserver.ToolGetAgent, mcpserver.ToolDelegateTask} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("the help text does not mention %q:\n%s", want, stdout)
		}
	}
}

func TestMCPListsTheConfiguredAgents(t *testing.T) {
	writeRunConfig(t, "http://127.0.0.1:1", `  reviewer:
    description: Deep source-code review
    model: test-model
    instructions: Review code carefully.
    output_mode: structured
    tools:
      - repo.read
      - git.diff`)
	session := connectMCP(t)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      mcpserver.ToolListAgents,
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("list_agents: %v", err)
	}
	if result.IsError {
		t.Fatalf("list_agents failed: %s", mcpText(result))
	}

	var agents []mcpserver.AgentSummary
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("cannot encode the payload: %v", err)
	}
	if err := json.Unmarshal(encoded, &agents); err != nil {
		t.Fatalf("cannot decode the payload: %v\n%s", err, encoded)
	}

	if len(agents) != 1 {
		t.Fatalf("agents = %d, want 1:\n%s", len(agents), encoded)
	}
	if agents[0].Name != "reviewer" || agents[0].Model != "test-model" {
		t.Fatalf("unexpected agent: %+v", agents[0])
	}
	if !agents[0].Ready || len(agents[0].Tools) != 2 {
		t.Fatalf("unexpected agent: %+v", agents[0])
	}
}

func TestMCPDelegatesATaskThroughTheWholeStack(t *testing.T) {
	answer := `{"summary":"One problem found.","findings":[{"severity":"high","category":"correctness","file":"main.go","line":4,"title":"Missing note","description":"The file has no doc comment.","suggestion":"Add one."}]}`
	server := newSSEServer(t, textAnswer(answer))

	workspace := writeRunConfig(t, server.server.URL, `  reviewer:
    model: test-model
    instructions: Review code carefully.
    output_mode: structured
    tools:
      - repo.read`)
	session := connectMCP(t)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: mcpserver.ToolDelegateTask,
		Arguments: map[string]any{
			"agent":     "reviewer",
			"task":      "Review the workspace",
			"workspace": workspace,
			"files":     []string{"main.go"},
			"budget":    map[string]any{"max_rounds": 4},
		},
	})
	if err != nil {
		t.Fatalf("delegate_task: %v", err)
	}
	if result.IsError {
		t.Fatalf("delegate_task failed: %s", mcpText(result))
	}

	var output mcpserver.DelegateTaskOutput
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("cannot encode the payload: %v", err)
	}
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatalf("cannot decode the payload: %v\n%s", err, encoded)
	}

	// The envelope is the documented one, whatever model produced it.
	if output.Status != agent.StatusCompleted || output.Agent != "reviewer" {
		t.Fatalf("unexpected envelope: %+v", output)
	}
	if output.Provider != "local" || output.Model != "fake-model" {
		t.Fatalf("the envelope lost the target that answered: %+v", output)
	}
	if output.Summary != "One problem found." || len(output.Findings) != 1 {
		t.Fatalf("unexpected envelope: %+v", output)
	}
	if output.Findings[0].File != "main.go" || output.Findings[0].Line != 4 {
		t.Fatalf("unexpected finding: %+v", output.Findings[0])
	}
	if !strings.HasPrefix(output.SessionID, "agt_") {
		t.Fatalf("the delegation was not recorded: %+v", output)
	}
	if output.ErrorKind != "" {
		t.Fatalf("a completed task carries an error kind: %+v", output)
	}

	// The task reached the provider exactly once, with the task text.
	requests := server.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	if !strings.Contains(requests[0], "Review the workspace") {
		t.Fatalf("the task did not reach the provider: %s", requests[0])
	}

	// And the session it reported is readable, which is what makes an advisory
	// finding verifiable after the fact.
	stdout, stderr, code := run(t, "session", "show", output.SessionID)
	if code != apperrors.ExitOK {
		t.Fatalf("session show exit code = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "One problem found.") {
		t.Fatalf("the recorded session does not hold the answer:\n%s", stdout)
	}
}

func TestMCPRefusesAnUnknownAgentAndARefusedWorkspace(t *testing.T) {
	server := newSSEServer(t, textAnswer(`{"summary":"nothing","findings":[]}`))
	workspace := writeRunConfig(t, server.server.URL, `  reviewer:
    model: test-model
    instructions: Review code carefully.
    output_mode: structured
    tools:
      - repo.read`)
	session := connectMCP(t)

	cases := []struct {
		name      string
		arguments map[string]any
		wantKind  apperrors.Kind
	}{
		{
			name:      "unknown agent",
			arguments: map[string]any{"agent": "typo", "task": "Review", "workspace": workspace},
			wantKind:  apperrors.KindNotFound,
		},
		{
			name:      "workspace that does not exist",
			arguments: map[string]any{"agent": "reviewer", "task": "Review", "workspace": "/nonexistent-pawagents-workspace"},
			wantKind:  apperrors.KindWorkspace,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
				Name:      mcpserver.ToolDelegateTask,
				Arguments: testCase.arguments,
			})
			if err != nil {
				t.Fatalf("delegate_task: %v", err)
			}
			if !result.IsError {
				t.Fatalf("the delegation was accepted: %s", mcpText(result))
			}
			if !strings.HasPrefix(mcpText(result), string(testCase.wantKind)+": ") {
				t.Fatalf("failure = %q, want %s", mcpText(result), testCase.wantKind)
			}
		})
	}

	// Neither refusal may have reached the provider.
	if requests := server.Requests(); len(requests) != 0 {
		t.Fatalf("a refused delegation reached the provider: %v", requests)
	}
}

func TestMCPHonoursTheDelegationTimeout(t *testing.T) {
	// The endpoint answers a model call after a delay, so a one second deadline
	// is reached while the task is running.
	server := newSSEServer(t, textAnswer(`{"summary":"too late","findings":[]}`))
	server.delay = 1200 * time.Millisecond
	workspace := writeRunConfig(t, server.server.URL, `  reviewer:
    model: test-model
    instructions: Review code carefully.
    output_mode: structured`)
	session := connectMCP(t)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: mcpserver.ToolDelegateTask,
		Arguments: map[string]any{
			"agent":     "reviewer",
			"task":      "Review the workspace",
			"workspace": workspace,
			"budget":    map[string]any{"timeout_sec": 1},
		},
	})
	if err != nil {
		t.Fatalf("delegate_task: %v", err)
	}
	if !result.IsError {
		t.Fatalf("a timed out delegation was reported as a success: %s", mcpText(result))
	}

	var output mcpserver.DelegateTaskOutput
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("cannot encode the payload: %v", err)
	}
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatalf("cannot decode the payload: %v\n%s", err, encoded)
	}
	if output.Status != agent.StatusTimeout {
		t.Fatalf("status = %q, want %q", output.Status, agent.StatusTimeout)
	}
	if output.ErrorKind != string(apperrors.KindTimeout) {
		t.Fatalf("error kind = %q, want %s", output.ErrorKind, apperrors.KindTimeout)
	}
	if !strings.HasPrefix(mcpText(result), string(apperrors.KindTimeout)) {
		t.Fatalf("failure = %q, want a classified message", mcpText(result))
	}
	// A delegation that timed out still recorded what it did.
	if !strings.HasPrefix(output.SessionID, "agt_") {
		t.Fatalf("a timed out delegation was not recorded: %+v", output)
	}
}
