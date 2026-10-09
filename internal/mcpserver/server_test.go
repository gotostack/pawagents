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

package mcpserver

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
	"errors"
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pawagents/pawagents/internal/agent"
	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/orchestrator"
	"github.com/pawagents/pawagents/internal/session"
)

// fakeRuntime stands in for the orchestrator so that the tools can be tested
// without a provider, an endpoint or a model.
type fakeRuntime struct {
	profiles []orchestrator.AgentProfile
	report   orchestrator.CapabilityReport
	checkErr error

	// requests records what the tools asked the runtime to do.
	requests []orchestrator.Request

	result *agent.Result
	err    error
}

func (f *fakeRuntime) Run(_ context.Context, request orchestrator.Request) (*agent.Result, error) {
	f.requests = append(f.requests, request)
	return f.result, f.err
}

func (f *fakeRuntime) Profiles() []orchestrator.AgentProfile { return f.profiles }

func (f *fakeRuntime) Profile(name string) (orchestrator.AgentProfile, error) {
	for _, profile := range f.profiles {
		if profile.Name == name {
			return profile, nil
		}
	}
	return orchestrator.AgentProfile{}, apperrors.New(apperrors.KindNotFound,
		"orchestrator.agent", "agent %q is not defined in the configuration", name).
		WithDetails(map[string]any{"available": []string{"reviewer", "local-reviewer"}})
}

func (f *fakeRuntime) CheckAgent(context.Context, string, bool) (orchestrator.CapabilityReport, error) {
	if f.checkErr != nil {
		return orchestrator.CapabilityReport{}, f.checkErr
	}
	return f.report, nil
}

func (f *fakeRuntime) Sessions() *session.Store { return nil }

// testProfile builds a profile that looks like a configured agent.
func testProfile(name string, tools ...string) orchestrator.AgentProfile {
	grants := make([]orchestrator.ToolGrant, 0, len(tools))
	for _, tool := range tools {
		grants = append(grants, orchestrator.ToolGrant{Name: tool, Known: true})
	}
	return orchestrator.AgentProfile{
		Name:        name,
		Description: "Deep source-code review",
		Type:        orchestrator.TypeSingle,
		Executable:  true,
		ModelAlias:  "local-coder",
		Targets: []config.ModelRef{
			{Provider: "local-ollama", Model: "qwen3-coder"},
			{Provider: "cloud-anthropic", Model: "claude-sonnet"},
		},
		Prompt:     orchestrator.PromptSource{Kind: orchestrator.PromptFile, Path: "reviewer.md", Bytes: 120},
		Tools:      grants,
		OutputMode: config.OutputModeStructured,
		Budget: orchestrator.BudgetView{
			MaxRounds: 12, MaxToolCalls: 40, MaxToolOutputBytes: 32768, Timeout: "10m0s",
		},
		Permissions: orchestrator.PermissionsView{Filesystem: "read", Shell: "deny"},
	}
}

// newTestServer builds a server plus a client connected to it in memory, which
// is how the tests observe exactly what a host would see.
func newTestServer(t *testing.T, runtime *fakeRuntime) *mcp.ClientSession {
	t.Helper()

	server, err := New(Options{
		Runtime: runtime,
		Config: &config.Config{
			Providers: map[string]*config.Provider{
				"local-ollama":    {Type: "ollama"},
				"cloud-anthropic": {Type: "anthropic"},
			},
		},
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

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

// callTool invokes a tool and decodes its structured content.
func callTool(t *testing.T, session *mcp.ClientSession, name string, arguments any, out any) *mcp.CallToolResult {
	t.Helper()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      name,
		Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if out != nil && result.StructuredContent != nil {
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatalf("cannot re-encode the structured content: %v", err)
		}
		if err := json.Unmarshal(encoded, out); err != nil {
			t.Fatalf("cannot decode the structured content of %s: %v\n%s", name, err, encoded)
		}
	}
	return result
}

// text returns the text content of a tool result.
func text(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if textContent, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, textContent.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func TestNewRequiresTheRuntimeAndTheConfiguration(t *testing.T) {
	if _, err := New(Options{Config: &config.Config{}}); err == nil {
		t.Fatal("New accepted a server without a runtime")
	}
	if _, err := New(Options{Runtime: &fakeRuntime{}}); err == nil {
		t.Fatal("New accepted a server without a configuration")
	}
}

func TestRunRequiresATransport(t *testing.T) {
	server, err := New(Options{Runtime: &fakeRuntime{}, Config: &config.Config{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := server.Run(context.Background(), nil); err == nil {
		t.Fatal("Run accepted a nil transport")
	}
}

func TestToolDiscovery(t *testing.T) {
	session := newTestServer(t, &fakeRuntime{})

	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	// tools/list is sorted by name by the protocol, so the order is not the
	// registration order; what matters is that every tool is discoverable with a
	// description, a schema and the read-only hint.
	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
		if tool.Description == "" {
			t.Fatalf("tool %q has no description", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Fatalf("tool %q has no input schema", tool.Name)
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("tool %q is not marked read-only", tool.Name)
		}
	}
	sort.Strings(names)
	want := append([]string(nil), Tools()...)
	sort.Strings(want)
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("tools = %v, want %v", names, want)
	}

	// The instructions are the only place a host is told what the server is for,
	// and they have to carry the advisory rule.
	if !strings.Contains(session.InitializeResult().Instructions, "advisory") {
		t.Fatalf("instructions = %q, want the advisory rule", session.InitializeResult().Instructions)
	}
}

// TestToolSchemasHaveAnObjectRoot guards a host compatibility trap.
//
// A host validates the schemas it is handed before it exposes a server's tools.
// Claude Code rejects a root type that is not a plain object, and a root of
// ["null","array"] - what a bare Go slice produces - makes it drop the whole
// tool list with "Invalid input (at tools.N.outputSchema.type)". The shape of
// the payload must therefore never leak into the schema root.
func TestToolSchemasHaveAnObjectRoot(t *testing.T) {
	session := newTestServer(t, &fakeRuntime{profiles: []orchestrator.AgentProfile{testProfile("reviewer")}})

	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	for _, tool := range result.Tools {
		for name, schema := range map[string]any{
			"inputSchema":  tool.InputSchema,
			"outputSchema": tool.OutputSchema,
		} {
			if schema == nil {
				continue
			}
			object, ok := schema.(map[string]any)
			if !ok {
				t.Fatalf("%s: %s is not an object schema: %T", tool.Name, name, schema)
			}
			if kind, ok := object["type"].(string); !ok || kind != "object" {
				t.Fatalf("%s: %s root type = %v, want the string \"object\"",
					tool.Name, name, object["type"])
			}
		}
	}
}

func TestListAgents(t *testing.T) {
	runtime := &fakeRuntime{
		profiles: []orchestrator.AgentProfile{
			testProfile("reviewer", "repo.read", "git.diff"),
			testProfile("local-reviewer", "repo.read"),
		},
		report: orchestrator.CapabilityReport{OK: true, Status: "ready"},
	}
	session := newTestServer(t, runtime)

	var payload ListAgentsOutput
	result := callTool(t, session, ToolListAgents, map[string]any{}, &payload)
	if result.IsError {
		t.Fatalf("list_agents failed: %s", text(result))
	}

	agents := payload.Agents
	if len(agents) != 2 {
		t.Fatalf("agents = %d, want 2", len(agents))
	}
	reviewer := agents[0]
	if reviewer.Name != "reviewer" || reviewer.Model != "local-coder" {
		t.Fatalf("unexpected agent: %+v", reviewer)
	}
	if reviewer.Description != "Deep source-code review" || reviewer.OutputMode != config.OutputModeStructured {
		t.Fatalf("unexpected agent: %+v", reviewer)
	}
	if len(reviewer.Tools) != 2 || !reviewer.Ready || reviewer.Why != "" {
		t.Fatalf("unexpected agent: %+v", reviewer)
	}
	// The provider is a local model server, whose capabilities only a probe can
	// establish, so a ready verdict is reported as unconfirmed rather than as
	// verified.
	if reviewer.Confirmed {
		t.Fatalf("an unprobed local model is reported as confirmed: %+v", reviewer)
	}

	// The text content carries the same payload, which is what a host that does
	// not read structured content falls back to.
	if !strings.Contains(text(result), `"name":"reviewer"`) {
		t.Fatalf("the text content does not carry the payload:\n%s", text(result))
	}
}

func TestListAgentsExplainsAnAgentThatCannotRun(t *testing.T) {
	cases := []struct {
		name         string
		providerType string
		probed       bool
		wantWhy      string
		wantCertain  bool
	}{
		{
			// The endpoint was asked: the refusal is a fact.
			name:         "a probed local model",
			providerType: "ollama",
			probed:       true,
			wantWhy:      "the model does not provide tool_calling",
			wantCertain:  true,
		},
		{
			// The contract documents the model, so the refusal is also a fact.
			name:         "a hosted model whose contract denies it",
			providerType: "anthropic",
			wantWhy:      "the model does not provide tool_calling",
			wantCertain:  true,
		},
		{
			// Nobody knows until the endpoint is asked: this must not be
			// reported as "the model cannot", or a host would never use a local
			// model it has not probed.
			name:         "an unprobed local model",
			providerType: "ollama",
			wantWhy:      "only the endpoint knows",
			wantCertain:  false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			runtime := &fakeRuntime{
				profiles: []orchestrator.AgentProfile{testProfile("broken")},
				report: orchestrator.CapabilityReport{
					OK:           false,
					Status:       "ready",
					ProviderType: testCase.providerType,
					Probed:       testCase.probed,
					Missing:      []string{"tool_calling"},
				},
			}
			session := newTestServer(t, runtime)

			var payload ListAgentsOutput
			callTool(t, session, ToolListAgents, map[string]any{}, &payload)
			agents := payload.Agents
			if len(agents) != 1 {
				t.Fatalf("agents = %d, want 1", len(agents))
			}
			if agents[0].Ready {
				t.Fatalf("an agent whose model lacks tool calling is reported as ready: %+v", agents[0])
			}
			if !strings.Contains(agents[0].Why, testCase.wantWhy) {
				t.Fatalf("Why = %q, want it to contain %q", agents[0].Why, testCase.wantWhy)
			}
			if agents[0].Confirmed != testCase.wantCertain {
				t.Fatalf("Confirmed = %v, want %v (%+v)", agents[0].Confirmed, testCase.wantCertain, agents[0])
			}
		})
	}
}

func TestGetAgent(t *testing.T) {
	runtime := &fakeRuntime{
		profiles: []orchestrator.AgentProfile{testProfile("reviewer", "repo.read", "git.diff")},
		report: orchestrator.CapabilityReport{
			OK:           true,
			Status:       "ready",
			ProviderType: "anthropic",
		},
	}
	session := newTestServer(t, runtime)

	var detail AgentDetail
	result := callTool(t, session, ToolGetAgent, map[string]any{"name": "reviewer"}, &detail)
	if result.IsError {
		t.Fatalf("get_agent failed: %s", text(result))
	}

	if detail.Name != "reviewer" || detail.Model != "local-coder" || !detail.Ready {
		t.Fatalf("unexpected detail: %+v", detail)
	}
	if !detail.Confirmed || detail.Why != "" {
		t.Fatalf("unexpected verdict: %+v", detail)
	}
	if len(detail.Targets) != 2 || detail.Targets[0].Target != "local-ollama/qwen3-coder" {
		t.Fatalf("targets = %+v", detail.Targets)
	}
	if detail.Targets[0].Fallback || !detail.Targets[1].Fallback {
		t.Fatalf("the fallback target is not marked: %+v", detail.Targets)
	}
	if detail.Targets[0].ProviderType != "ollama" {
		t.Fatalf("provider type = %q, want ollama", detail.Targets[0].ProviderType)
	}
	if detail.Budget.MaxRounds != 12 || detail.Budget.Timeout != "10m0s" {
		t.Fatalf("budget = %+v", detail.Budget)
	}
	if detail.Permissions.Filesystem != "read" || detail.Permissions.Shell != "deny" {
		t.Fatalf("permissions = %+v", detail.Permissions)
	}
	if detail.Prompt != orchestrator.PromptFile {
		t.Fatalf("prompt = %q", detail.Prompt)
	}
}

func TestGetAgentReportsAnUnknownAgent(t *testing.T) {
	session := newTestServer(t, &fakeRuntime{profiles: []orchestrator.AgentProfile{testProfile("reviewer")}})

	result := callTool(t, session, ToolGetAgent, map[string]any{"name": "typo"}, nil)
	if !result.IsError {
		t.Fatalf("get_agent accepted an unknown agent: %s", text(result))
	}
	if !strings.HasPrefix(text(result), string(apperrors.KindNotFound)+": ") {
		t.Fatalf("failure = %q, want a classified message", text(result))
	}
	if !strings.Contains(text(result), "typo") {
		t.Fatalf("failure = %q, want the agent name", text(result))
	}
}

func TestGetAgentRequiresAName(t *testing.T) {
	session := newTestServer(t, &fakeRuntime{})

	// A missing field is refused against the schema, before the handler runs.
	result := callTool(t, session, ToolGetAgent, map[string]any{}, nil)
	if !result.IsError {
		t.Fatal("get_agent accepted a call without a name")
	}
	if !strings.Contains(text(result), "required") {
		t.Fatalf("failure = %q, want a schema error", text(result))
	}

	// A blank name passes the schema and is classified by the handler.
	result = callTool(t, session, ToolGetAgent, map[string]any{"name": "   "}, nil)
	if !result.IsError {
		t.Fatal("get_agent accepted a blank name")
	}
	if !strings.HasPrefix(text(result), string(apperrors.KindInvalidArgument)+": ") {
		t.Fatalf("failure = %q, want an invalid argument", text(result))
	}
}

func TestDelegateTaskReturnsTheEnvelope(t *testing.T) {
	finished := time.Now()
	runtime := &fakeRuntime{
		profiles: []orchestrator.AgentProfile{testProfile("reviewer")},
		result: &agent.Result{
			Status:     agent.StatusCompleted,
			Agent:      "reviewer",
			Provider:   "local-ollama",
			Model:      "qwen3-coder",
			Summary:    "One concurrency problem.",
			Findings:   []agent.Finding{{Severity: "high", File: "lib/dpif-netdev.c", Line: 3184, Title: "Race"}},
			Usage:      agent.Usage{InputTokens: 14325, OutputTokens: 3120, ToolCalls: 17, Rounds: 8},
			SessionID:  "agt_0123456789abcdef",
			StartedAt:  finished.Add(-2 * time.Second),
			FinishedAt: finished,
		},
	}
	session := newTestServer(t, runtime)

	var output DelegateTaskOutput
	result := callTool(t, session, ToolDelegateTask, map[string]any{
		"agent":      "reviewer",
		"task":       "Review the OVS changes for concurrency problems.",
		"workspace":  "/tmp/workspace",
		"background": "The patch changes PMD scheduling.",
		"files":      []string{"lib/dpif-netdev.c"},
		"constraints": []string{
			"Do not modify files",
		},
		"output_mode": "structured",
		"budget":      map[string]any{"max_rounds": 20, "timeout_sec": 300},
	}, &output)

	if result.IsError {
		t.Fatalf("delegate_task failed: %s", text(result))
	}
	if output.Status != agent.StatusCompleted || output.Summary != "One concurrency problem." {
		t.Fatalf("unexpected envelope: %+v", output)
	}
	if output.SessionID != "agt_0123456789abcdef" || output.ErrorKind != "" {
		t.Fatalf("unexpected envelope: %+v", output)
	}
	if len(output.Findings) != 1 || output.Findings[0].Line != 3184 {
		t.Fatalf("findings did not survive the protocol: %+v", output.Findings)
	}
	if output.Usage.Rounds != 8 || output.Usage.ToolCalls != 17 {
		t.Fatalf("usage = %+v", output.Usage)
	}

	if len(runtime.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(runtime.requests))
	}
	request := runtime.requests[0]
	if request.Agent != "reviewer" || request.Workspace != "/tmp/workspace" {
		t.Fatalf("request = %+v", request)
	}
	if request.Background != "The patch changes PMD scheduling." {
		t.Fatalf("background was lost: %+v", request)
	}
	if len(request.Files) != 1 || len(request.Constraints) != 1 {
		t.Fatalf("files or constraints were lost: %+v", request)
	}
	if request.OutputMode != config.OutputModeStructured {
		t.Fatalf("output mode = %q", request.OutputMode)
	}
	if request.MaxRounds != 20 || request.Timeout != 300*time.Second {
		t.Fatalf("budget was not applied: %+v", request)
	}
}

func TestDelegateTaskReportsAFailureAfterItStarted(t *testing.T) {
	runtime := &fakeRuntime{
		profiles: []orchestrator.AgentProfile{testProfile("reviewer")},
		result: &agent.Result{
			Status:    agent.StatusBudgetExceeded,
			Agent:     "reviewer",
			Summary:   "Stopped after the round budget.",
			SessionID: "agt_0123456789abcdef",
		},
		err: apperrors.New(apperrors.KindBudgetExceeded, "agent.run",
			"the round budget of 12 was exhausted"),
	}
	session := newTestServer(t, runtime)

	var output DelegateTaskOutput
	result := callTool(t, session, ToolDelegateTask, map[string]any{
		"agent": "reviewer",
		"task":  "Review the changes",
	}, &output)

	if !result.IsError {
		t.Fatal("a delegation that ran out of budget was reported as a success")
	}
	if output.Status != agent.StatusBudgetExceeded {
		t.Fatalf("status = %q", output.Status)
	}
	if output.ErrorKind != string(apperrors.KindBudgetExceeded) {
		t.Fatalf("error kind = %q", output.ErrorKind)
	}
	// The partial answer and the session id stay available: a host can read what
	// the agent managed to establish and inspect the transcript.
	if output.Summary == "" || output.SessionID == "" {
		t.Fatalf("the partial result was discarded: %+v", output)
	}
	if !strings.HasPrefix(text(result), string(apperrors.KindBudgetExceeded)) {
		t.Fatalf("failure = %q, want a classified message", text(result))
	}
}

// TestDelegateTaskClassifiesAStatusWithoutAnError pins the one classification a
// host sees when the runtime returns a result and no error. The status is mapped
// once, and the envelope and the MCP error both carry that same kind: a host
// that reads the CallTool failure must not see an empty one while the payload
// names a budget or a timeout.
func TestDelegateTaskClassifiesAStatusWithoutAnError(t *testing.T) {
	cases := []struct {
		name     string
		status   string
		wantKind apperrors.Kind
	}{
		{"budget exceeded", agent.StatusBudgetExceeded, apperrors.KindBudgetExceeded},
		{"timeout", agent.StatusTimeout, apperrors.KindTimeout},
		{"cancelled", agent.StatusCancelled, apperrors.KindCancelled},
		{"failed", agent.StatusFailed, apperrors.KindAgent},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			runtime := &fakeRuntime{
				profiles: []orchestrator.AgentProfile{testProfile("reviewer")},
				result: &agent.Result{
					Status:    testCase.status,
					Agent:     "reviewer",
					Summary:   "Stopped before the answer.",
					Error:     "the task did not finish",
					SessionID: "agt_0123456789abcdef",
				},
			}
			session := newTestServer(t, runtime)

			var output DelegateTaskOutput
			result := callTool(t, session, ToolDelegateTask, map[string]any{
				"agent": "reviewer",
				"task":  "Review the changes",
			}, &output)

			if !result.IsError {
				t.Fatal("a failed delegation was reported as a success")
			}
			if output.ErrorKind != string(testCase.wantKind) {
				t.Fatalf("envelope error kind = %q, want %s", output.ErrorKind, testCase.wantKind)
			}
			if !strings.HasPrefix(text(result), string(testCase.wantKind)+": ") {
				t.Fatalf("failure = %q, want the %s classification the envelope names",
					text(result), testCase.wantKind)
			}
			// The partial answer and the session stay available: the failure is
			// worth reporting, not worth discarding.
			if output.Summary == "" || output.SessionID == "" {
				t.Fatalf("the partial result was discarded: %+v", output)
			}
		})
	}
}

// TestDelegateTaskAcceptsACompletedStatusWithoutAnError keeps the other half of
// the contract: a task that answered is not an error, whichever kind an empty
// status would have mapped to.
func TestDelegateTaskAcceptsACompletedStatusWithoutAnError(t *testing.T) {
	runtime := &fakeRuntime{
		profiles: []orchestrator.AgentProfile{testProfile("reviewer")},
		result:   &agent.Result{Status: agent.StatusCompleted, Agent: "reviewer", Summary: "All good."},
	}
	session := newTestServer(t, runtime)

	var output DelegateTaskOutput
	result := callTool(t, session, ToolDelegateTask, map[string]any{
		"agent": "reviewer",
		"task":  "Review the changes",
	}, &output)

	if result.IsError {
		t.Fatalf("a completed task was reported as a failure: %s", text(result))
	}
	if output.ErrorKind != "" {
		t.Fatalf("error kind = %q, want none", output.ErrorKind)
	}
}

func TestEffectiveKind(t *testing.T) {
	classified := apperrors.New(apperrors.KindPermission, "tools.executor", "repo.read is not granted")

	cases := []struct {
		name   string
		runErr error
		status string
		want   apperrors.Kind
	}{
		{"a classified error wins", classified, agent.StatusFailed, apperrors.KindPermission},
		{"its kind is used even when the status says otherwise", classified, agent.StatusCompleted, apperrors.KindPermission},
		{"an unclassified error is internal", errors.New("the endpoint hung up"), agent.StatusFailed, apperrors.KindInternal},
		{"a completed status needs no kind", nil, agent.StatusCompleted, ""},
		{"a budget status is mapped", nil, agent.StatusBudgetExceeded, apperrors.KindBudgetExceeded},
		{"a timeout status is mapped", nil, agent.StatusTimeout, apperrors.KindTimeout},
		{"a cancelled status is mapped", nil, agent.StatusCancelled, apperrors.KindCancelled},
		{"a failed status is mapped", nil, agent.StatusFailed, apperrors.KindAgent},
		{"an unknown status still classifies", nil, "something-new", apperrors.KindInternal},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := effectiveKind(testCase.runErr, testCase.status); got != testCase.want {
				t.Fatalf("effectiveKind() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestDelegateTaskReportsAFailureBeforeItStarted(t *testing.T) {
	runtime := &fakeRuntime{
		profiles: []orchestrator.AgentProfile{testProfile("reviewer")},
		err: apperrors.New(apperrors.KindWorkspace, "orchestrator.workspace",
			"/etc is not inside the allowed roots"),
	}
	session := newTestServer(t, runtime)

	var output DelegateTaskOutput
	result := callTool(t, session, ToolDelegateTask, map[string]any{
		"agent":     "reviewer",
		"task":      "Review the changes",
		"workspace": "/etc",
	}, &output)

	if !result.IsError {
		t.Fatal("a refused workspace was reported as a success")
	}
	if !strings.HasPrefix(text(result), string(apperrors.KindWorkspace)+": ") {
		t.Fatalf("failure = %q, want a workspace error", text(result))
	}
	// Nothing ran, so there is no envelope to report.
	if output.Status != "" {
		t.Fatalf("an envelope was invented for a task that never ran: %+v", output)
	}
}

func TestDelegateTaskValidatesTheContract(t *testing.T) {
	runtime := &fakeRuntime{profiles: []orchestrator.AgentProfile{testProfile("reviewer")}}
	session := newTestServer(t, runtime)

	// The required fields are absent: the SDK rejects those against the tool
	// schema before the handler runs, which is the cheapest possible refusal.
	for _, arguments := range []map[string]any{
		{"task": "review"},
		{"agent": "reviewer"},
	} {
		result := callTool(t, session, ToolDelegateTask, arguments, nil)
		if !result.IsError {
			t.Fatalf("a call without a required field was accepted: %s", text(result))
		}
		if !strings.Contains(text(result), "required") {
			t.Fatalf("failure = %q, want a schema error", text(result))
		}
	}

	// Everything else is classified by the handler, so that a host can branch on
	// the kind instead of on the wording.
	cases := []struct {
		name      string
		arguments map[string]any
		wantKind  apperrors.Kind
	}{
		{"with a blank agent", map[string]any{"agent": "  ", "task": "review"}, apperrors.KindInvalidArgument},
		{"with a blank task", map[string]any{"agent": "reviewer", "task": "  "}, apperrors.KindInvalidArgument},
		{"with an unknown agent", map[string]any{"agent": "typo", "task": "review"}, apperrors.KindNotFound},
		{"with a bad output mode", map[string]any{"agent": "reviewer", "task": "review", "output_mode": "yaml"}, apperrors.KindInvalidArgument},
		{"with negative rounds", map[string]any{"agent": "reviewer", "task": "review", "budget": map[string]any{"max_rounds": -1}}, apperrors.KindInvalidArgument},
		{"with negative seconds", map[string]any{"agent": "reviewer", "task": "review", "budget": map[string]any{"timeout_sec": -5}}, apperrors.KindInvalidArgument},
		{"with an oversized timeout", map[string]any{"agent": "reviewer", "task": "review", "budget": map[string]any{"timeout_sec": maxTimeoutSec + 1}}, apperrors.KindInvalidArgument},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := callTool(t, session, ToolDelegateTask, testCase.arguments, nil)
			if !result.IsError {
				t.Fatalf("the delegation was accepted: %s", text(result))
			}
			if !strings.HasPrefix(text(result), string(testCase.wantKind)+": ") {
				t.Fatalf("failure = %q, want %s", text(result), testCase.wantKind)
			}
		})
	}

	if len(runtime.requests) != 0 {
		t.Fatalf("a refused contract still reached the runtime: %+v", runtime.requests)
	}
}

func TestDelegateTaskSendsTheDefaultBudgetWhenNoneIsGiven(t *testing.T) {
	runtime := &fakeRuntime{
		profiles: []orchestrator.AgentProfile{testProfile("reviewer")},
		result:   &agent.Result{Status: agent.StatusCompleted, Agent: "reviewer"},
	}
	session := newTestServer(t, runtime)

	callTool(t, session, ToolDelegateTask, map[string]any{
		"agent": "reviewer",
		"task":  "Review the changes",
	}, nil)

	if len(runtime.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(runtime.requests))
	}
	request := runtime.requests[0]
	if request.MaxRounds != 0 || request.Timeout != 0 {
		t.Fatalf("a delegation without a budget invented one: %+v", request)
	}
	if request.OutputMode != "" {
		t.Fatalf("output mode = %q, want the agent default", request.OutputMode)
	}
}

func TestDelegateTaskKeepsAnUnclassifiedFailureClassified(t *testing.T) {
	runtime := &fakeRuntime{
		profiles: []orchestrator.AgentProfile{testProfile("reviewer")},
		err:      errors.New("the endpoint hung up"),
	}
	session := newTestServer(t, runtime)

	result := callTool(t, session, ToolDelegateTask, map[string]any{
		"agent": "reviewer",
		"task":  "Review the changes",
	}, nil)

	if !result.IsError {
		t.Fatal("the failure was reported as a success")
	}
	if !strings.HasPrefix(text(result), string(apperrors.KindInternal)+": ") {
		t.Fatalf("failure = %q, want an internal error", text(result))
	}
	if !strings.Contains(text(result), "the endpoint hung up") {
		t.Fatalf("failure = %q, want the cause", text(result))
	}
}

func TestFailureRendersTheClassification(t *testing.T) {
	classified := apperrors.New(apperrors.KindPermission, "tools.executor",
		"repo.read is not granted to this agent")
	rendered := failure(classified)
	if rendered == nil {
		t.Fatal("failure returned nil for a classified error")
	}
	if got := rendered.Error(); got != "PermissionError: tools.executor: repo.read is not granted to this agent" {
		t.Fatalf("failure() = %q", got)
	}

	wrapped := apperrors.Wrap(apperrors.KindProvider, "orchestrator.run",
		"cannot reach the endpoint", errors.New("connection refused"))
	if got := failure(wrapped).Error(); !strings.HasPrefix(got, "ProviderError: ") {
		t.Fatalf("failure() = %q, want the provider classification", got)
	}

	if failure(nil) != nil {
		t.Fatal("failure did not ignore a nil error")
	}
}
