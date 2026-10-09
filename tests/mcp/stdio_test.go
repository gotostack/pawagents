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

//go:build integration

// Package mcp runs the host side of the chain: a real MCP client speaking to
// the built binary over stdio, exactly as Codex and Claude Code do.
//
// The tests spawn the process. Everything else - the handshake, the tool list,
// the schemas, the delegation - goes through the protocol, so a change that
// breaks the host contract fails here rather than inside a host.
package mcp

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pawagents/pawagents/internal/agent"
	"github.com/pawagents/pawagents/internal/mcpserver"
	"github.com/pawagents/pawagents/tests/internal/harness"
)

// connect spawns the server and completes the MCP handshake.
func connect(t *testing.T, home string) (*mcp.ClientSession, context.Context) {
	t.Helper()

	if !harness.Enabled(harness.EnvMCP) {
		t.Skipf("%s is not set: set it to 1 to run the tests that spawn the MCP server",
			harness.EnvMCP)
	}

	binary := harness.Binary(t)
	command := exec.Command(binary, "mcp", "serve", "--stdio")
	command.Env = harness.Env(home)
	command.Dir = harness.RepoRoot(t)

	// The client is given its own context so that a hung server fails the test
	// instead of hanging the run.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	client := mcp.NewClient(&mcp.Implementation{Name: "pawagents-integration", Version: "1"},
		nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatalf("cannot complete the MCP handshake with %s: %v", binary, err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, ctx
}

// callTool invokes one tool and returns its structured payload.
func callTool[T any](t *testing.T, ctx context.Context, session *mcp.ClientSession,
	name string, arguments any) (T, *mcp.CallToolResult) {

	t.Helper()

	var empty T
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("calling %s failed at the protocol level: %v", name, err)
	}
	if len(result.Content) == 0 {
		t.Fatalf("%s returned no content", name)
	}

	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("%s returned %T, want text", name, result.Content[0])
	}
	if result.IsError {
		return empty, result
	}

	var payload T
	if err := json.Unmarshal([]byte(text.Text), &payload); err != nil {
		t.Fatalf("%s returned a payload that is not valid JSON: %v\n%s", name, err, text.Text)
	}
	return payload, result
}

// offlineConfig is a configuration whose agent cannot reach a model, used for
// the parts of the contract that must not depend on one.
func offlineConfig(t *testing.T) string {
	t.Helper()

	return harness.Config(t, `
version: 1

providers:
  local-endpoint:
    type: openai-compatible
    base_url: https://api.example.invalid/v1
    api_key_env: PAWAGENTS_TEST_ABSENT_KEY

models:
  slow-coder:
    provider: local-endpoint
    model: some-model

agents:
  reviewer:
    description: Reviews a change.
    model: slow-coder
    instructions: Review the change.
    output_mode: structured
    tools:
      - repo.read
`)
}

// TestStdioExposesTheHostFacingContract checks what a host validates before it
// exposes a tool: the names, and the root type of every schema.
//
// A root that is not an object makes a host drop the whole tool list, so this
// is the first thing to check and the cheapest thing to break.
func TestStdioExposesTheHostFacingContract(t *testing.T) {
	session, ctx := connect(t, offlineConfig(t))

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list failed: %v", err)
	}

	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
		if tool.InputSchema == nil {
			t.Fatalf("%s has no input schema", tool.Name)
		}
		if tool.OutputSchema == nil {
			t.Fatalf("%s has no output schema", tool.Name)
		}
		// Marshalling the schema is how a host reads it: the root type has to
		// be the string "object", not a list that contains it.
		for label, schema := range map[string]any{
			"input":  tool.InputSchema,
			"output": tool.OutputSchema,
		} {
			encoded, err := json.Marshal(schema)
			if err != nil {
				t.Fatalf("%s %s schema is not marshallable: %v", tool.Name, label, err)
			}
			var root struct {
				Type any `json:"type"`
			}
			if err := json.Unmarshal(encoded, &root); err != nil {
				t.Fatalf("%s %s schema is not valid JSON: %v", tool.Name, label, err)
			}
			if value, ok := root.Type.(string); !ok || value != "object" {
				t.Fatalf("%s %s schema root type = %v, want \"object\"; a host drops "+
					"the tool list when it is not", tool.Name, label, root.Type)
			}
		}
		if tool.Description == "" {
			t.Fatalf("%s has no description, so a host cannot decide when to call it",
				tool.Name)
		}
	}

	want := []string{mcpserver.ToolDelegateTask, mcpserver.ToolGetAgent, mcpserver.ToolListAgents}
	if len(names) != len(want) {
		t.Fatalf("tools = %v, want exactly %v", names, want)
	}
	for _, name := range want {
		if !contains(names, name) {
			t.Fatalf("tools = %v, missing %s", names, name)
		}
	}
}

// TestStdioListsAndDescribesAgents walks the discovery path a host takes before
// it delegates anything.
func TestStdioListsAndDescribesAgents(t *testing.T) {
	session, ctx := connect(t, offlineConfig(t))

	list, result := callTool[mcpserver.ListAgentsOutput](t, ctx, session,
		mcpserver.ToolListAgents, map[string]any{})
	if result.IsError {
		t.Fatalf("list_agents failed: %s", textOf(result))
	}
	if len(list.Agents) != 1 {
		t.Fatalf("agents = %+v, want the single configured agent", list.Agents)
	}

	summary := list.Agents[0]
	if summary.Name != "reviewer" {
		t.Fatalf("name = %q, want reviewer", summary.Name)
	}
	if summary.Model != "slow-coder" {
		t.Fatalf("model = %q, want slow-coder", summary.Model)
	}
	if len(summary.Tools) == 0 {
		t.Fatal("the summary does not report the tool grant")
	}
	if summary.Confirmed {
		t.Fatalf("an endpoint that was never asked cannot produce a confirmed verdict: %+v",
			summary)
	}
	if summary.Ready && summary.Why != "" {
		t.Fatalf("a ready agent has nothing to explain: %+v", summary)
	}

	detail, result := callTool[mcpserver.AgentDetail](t, ctx, session,
		mcpserver.ToolGetAgent, map[string]any{"name": "reviewer"})
	if result.IsError {
		t.Fatalf("get_agent failed: %s", textOf(result))
	}
	if detail.Name != "reviewer" || detail.Budget.MaxRounds == 0 {
		t.Fatalf("the detail is incomplete: %+v", detail)
	}
	if len(detail.Targets) == 0 {
		t.Fatalf("the detail names no target: %+v", detail)
	}
	if detail.Permissions.Shell != "deny" {
		t.Fatalf("the detail reports shell %q, want deny: an external subagent is read-only",
			detail.Permissions.Shell)
	}
}

// TestStdioReportsAnUnknownAgent checks that a host gets a classified failure
// instead of a protocol error.
func TestStdioReportsAnUnknownAgent(t *testing.T) {
	session, ctx := connect(t, offlineConfig(t))

	_, result := callTool[mcpserver.AgentDetail](t, ctx, session,
		mcpserver.ToolGetAgent, map[string]any{"name": "no-such-agent"})
	if !result.IsError {
		t.Fatal("get_agent accepted an agent that does not exist")
	}

	message := textOf(result)
	if !strings.Contains(message, "NotFoundError") {
		t.Fatalf("the failure is not classified for the host: %s", message)
	}
	if !strings.Contains(message, "no-such-agent") {
		t.Fatalf("the failure does not name the agent: %s", message)
	}

	// The tool list must still work afterwards: one failed call may not break
	// the session.
	if _, err := session.ListTools(ctx, nil); err != nil {
		t.Fatalf("the session broke after a classified failure: %v", err)
	}
}

// TestStdioDelegateTaskRunsTheLocalAgent is the chain the MVP promises:
//
//	host -> MCP -> PawAgents -> Ollama -> structured result
//
// It needs a real model, so it is gated twice: the MCP gate for the server and
// the Ollama gate for the model.
func TestStdioDelegateTaskRunsTheLocalAgent(t *testing.T) {
	installed := harness.RequireOllama(t)
	model := harness.Model(t, installed)

	home := harness.OllamaConfig(t, model)
	session, ctx := connect(t, home)
	repo := harness.GitRepo(t)

	// The host discovers first, then delegates: doing it in the other order is
	// what a host cannot do, because it does not know the agent names yet.
	list, result := callTool[mcpserver.ListAgentsOutput](t, ctx, session,
		mcpserver.ToolListAgents, map[string]any{})
	if result.IsError {
		t.Fatalf("list_agents failed: %s", textOf(result))
	}
	if len(list.Agents) != 1 || list.Agents[0].Name != "local-reviewer" {
		t.Fatalf("agents = %+v, want local-reviewer", list.Agents)
	}

	out, call := callTool[mcpserver.DelegateTaskOutput](t, ctx, session,
		mcpserver.ToolDelegateTask, map[string]any{
			"agent":     "local-reviewer",
			"workspace": repo,
			"task": "Review the uncommitted change in this repository. " +
				"Start with git.diff and read the file it touches. " +
				"Report at most three findings and name the file each one is about.",
			"background": "The change removes a guard from a rate limiter.",
		})
	if call.IsError {
		t.Fatalf("delegate_task failed: %s", textOf(call))
	}

	if out.Status != agent.StatusCompleted {
		t.Fatalf("the delegation did not complete: status=%q kind=%q error=%s",
			out.Status, out.ErrorKind, out.Error)
	}
	if out.Agent != "local-reviewer" {
		t.Fatalf("agent = %q, want local-reviewer", out.Agent)
	}
	if !strings.Contains(out.Model, model) {
		t.Fatalf("model = %q, want the configured %q", out.Model, model)
	}
	if out.SessionID == "" {
		t.Fatal("the delegation recorded no session, so the host cannot read it back")
	}
	if out.Usage.InputTokens == 0 {
		t.Fatal("the delegation reports no input tokens")
	}
	if out.ErrorKind != "" {
		t.Fatalf("a completed task carries no error kind, got %q", out.ErrorKind)
	}
}

// textOf returns the text content of a tool result.
func textOf(result *mcp.CallToolResult) string {
	if len(result.Content) == 0 {
		return ""
	}
	if text, ok := result.Content[0].(*mcp.TextContent); ok {
		return text.Text
	}
	return ""
}

// contains reports whether a list holds a value.
func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
