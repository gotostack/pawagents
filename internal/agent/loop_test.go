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

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/llm"
	"github.com/pawagents/pawagents/internal/security"
	"github.com/pawagents/pawagents/internal/tools"
)

// scriptedModel answers with a fixed script of events per call, and records
// every request so that a test can inspect the conversation the loop built.
type scriptedModel struct {
	name         string
	capabilities llm.ModelCapabilities
	scripts      [][]llm.Event
	errors       []error
	requests     []*llm.GenerateRequest
}

func (m *scriptedModel) Name() string { return m.name }

func (m *scriptedModel) Type() string { return "scripted" }

func (m *scriptedModel) Capabilities(_ context.Context, _ string) (llm.ModelCapabilities, error) {
	return m.capabilities, nil
}

func (m *scriptedModel) Generate(_ context.Context, request *llm.GenerateRequest) (llm.Stream, error) {
	m.requests = append(m.requests, request)
	if len(m.errors) > 0 {
		err := m.errors[0]
		m.errors = m.errors[1:]
		if err != nil {
			return nil, err
		}
	}
	if len(m.scripts) == 0 {
		return nil, errors.New("scripted model has no script left")
	}
	events := m.scripts[0]
	m.scripts = m.scripts[1:]
	return llm.SliceStream(events...), nil
}

// answerEvents scripts a final answer.
func answerEvents(text string, usage llm.Usage) []llm.Event {
	return []llm.Event{
		llm.NewStartEvent("test-model"),
		llm.NewTextDeltaEvent(text),
		llm.NewUsageEvent(usage),
		llm.NewFinishEvent(llm.FinishReasonStop, "test-model"),
	}
}

// toolCallEvents scripts one round of tool calls.
func toolCallEvents(usage llm.Usage, calls ...llm.ToolCall) []llm.Event {
	events := []llm.Event{llm.NewStartEvent("test-model")}
	for index, call := range calls {
		events = append(events, llm.NewToolCallEvent(index, call))
	}
	events = append(events,
		llm.NewUsageEvent(usage),
		llm.NewFinishEvent(llm.FinishReasonToolCalls, "test-model"))
	return events
}

// fakeTool is a tool whose behaviour a test controls.
type fakeTool struct {
	name   string
	output string
	err    error
	seen   []string
}

func (t *fakeTool) Name() string { return t.name }

func (t *fakeTool) Definition() llm.ToolDefinition {
	return tools.NewDefinition(t.name, "a tool used by the tests",
		map[string]llm.JSONSchema{"path": llm.StringSchema()}, "path")
}

func (t *fakeTool) Execute(_ context.Context, arguments json.RawMessage, _ tools.Env) (tools.Result, error) {
	t.seen = append(t.seen, string(arguments))
	if t.err != nil {
		return tools.Result{}, t.err
	}
	return tools.Result{Content: t.output}, nil
}

// testCapabilities are the capabilities a tool-calling model reports.
func testCapabilities() llm.ModelCapabilities {
	return llm.ModelCapabilities{
		ToolCalling:      true,
		Streaming:        true,
		SystemMessage:    true,
		MaxContextTokens: 8192,
		MaxOutputTokens:  2048,
	}
}

// newTestRunner builds a runner around a scripted model and one granted tool.
func newTestRunner(t *testing.T, model *scriptedModel, options runnerTestOptions) (*Runner, *fakeTool) {
	t.Helper()

	tool := &fakeTool{name: "repo.read", output: options.toolOutput}
	if tool.output == "" {
		tool.output = "file contents"
	}
	tool.err = options.toolErr

	registry := tools.NewRegistry()
	registry.Register(tool)

	grant := security.NewGrant([]string{tool.Name()}, nil, nil)

	executor := tools.NewExecutor(registry, grant, tools.Env{})

	profile := Profile{
		Name:         "test-agent",
		SystemPrompt: "You review code.",
		OutputMode:   options.outputMode,
		Tools:        grant.Names(),
		Grant:        grant,
	}
	if profile.OutputMode == "" {
		profile.OutputMode = OutputText
	}

	budget := options.budget
	if budget.MaxRounds == 0 {
		budget.MaxRounds = 5
	}

	capabilities := testCapabilities()
	capabilities.StructuredOutput = options.structuredOutput

	runner, err := NewRunner(RunnerOptions{
		Profile:         profile,
		Model:           model,
		ModelName:       "test-model",
		Capabilities:    capabilities,
		Executor:        executor,
		Budget:          budget,
		MaxOutputTokens: 1024,
		Compactor:       options.compactor,
		Recorder:        options.recorder,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner, tool
}

// runnerTestOptions tunes the fixture.
type runnerTestOptions struct {
	outputMode       string
	structuredOutput bool
	budget           Budget
	toolOutput       string
	toolErr          error
	compactor        Compactor
	recorder         Recorder
}

func TestRunnerReturnsATextAnswer(t *testing.T) {
	model := &scriptedModel{
		name:         "test",
		capabilities: testCapabilities(),
		scripts: [][]llm.Event{
			answerEvents("The change looks correct.", llm.Usage{InputTokens: 100, OutputTokens: 20}),
		},
	}

	runner, tool := newTestRunner(t, model, runnerTestOptions{})
	result, err := runner.Run(context.Background(), Task{Instruction: "Review the change"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Status != StatusCompleted {
		t.Fatalf("status = %q, want %q", result.Status, StatusCompleted)
	}
	if result.Text != "The change looks correct." {
		t.Fatalf("text = %q", result.Text)
	}
	if result.Summary != "The change looks correct." {
		t.Fatalf("summary = %q", result.Summary)
	}
	if result.Usage.Rounds != 1 || result.Usage.InputTokens != 100 || result.Usage.OutputTokens != 20 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if result.Usage.ToolCalls != 0 || len(tool.seen) != 0 {
		t.Fatalf("the tool must not have run: %+v", result.Usage)
	}
	if result.Agent != "test-agent" || result.Model != "test-model" || result.Provider != "test" {
		t.Fatalf("result identity = %+v", result)
	}
	if result.Duration() <= 0 {
		t.Fatalf("duration was not recorded")
	}

	// The first request must carry the system prompt, the task and the tool
	// definitions: a model that cannot see the task cannot do it.
	request := model.requests[0]
	if request.Model != "test-model" || len(request.Messages) != 2 {
		t.Fatalf("request = %+v", request)
	}
	if request.Messages[0].Role != llm.RoleSystem || request.Messages[1].Role != llm.RoleUser {
		t.Fatalf("roles = %s, %s", request.Messages[0].Role, request.Messages[1].Role)
	}
	if !strings.Contains(request.Messages[1].Text(), "Review the change") {
		t.Fatalf("the task is missing from the user message: %q", request.Messages[1].Text())
	}
	if len(request.Tools) != 1 || request.Tools[0].Name != "repo.read" {
		t.Fatalf("tools = %+v", request.Tools)
	}
	if request.ToolChoice.Mode != llm.ToolChoiceAuto {
		t.Fatalf("tool choice = %+v", request.ToolChoice)
	}
}

func TestRunnerParsesTheStructuredEnvelope(t *testing.T) {
	answer := "Here is what I found:\n\n```json\n" + `{
  "summary": "One concurrency problem.",
  "findings": [
    {
      "severity": "high",
      "category": "concurrency",
      "file": "internal/agent/loop.go",
      "line": 42,
      "title": "The budget is checked after the call",
      "description": "The loop can overshoot the budget by one round.",
      "evidence": "if round > budget.MaxRounds",
      "suggestion": "Check the budget first.",
      "confidence": 0.8
    }
  ]
}` + "\n```\n"

	model := &scriptedModel{
		name:         "test",
		capabilities: testCapabilities(),
		scripts:      [][]llm.Event{answerEvents(answer, llm.Usage{})},
	}

	runner, _ := newTestRunner(t, model, runnerTestOptions{outputMode: OutputStructured})
	result, err := runner.Run(context.Background(), Task{Instruction: "Review"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Summary != "One concurrency problem." {
		t.Fatalf("summary = %q", result.Summary)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("findings = %+v", result.Findings)
	}

	finding := result.Findings[0]
	if finding.Title != "The budget is checked after the call" {
		t.Fatalf("title = %q", finding.Title)
	}
	if finding.File != "internal/agent/loop.go" || finding.Line != 42 {
		t.Fatalf("location = %s:%d", finding.File, finding.Line)
	}
	if finding.Confidence != 0.8 {
		t.Fatalf("confidence = %v", finding.Confidence)
	}
	if !strings.Contains(result.Text, "\"findings\"") {
		t.Fatalf("the raw answer must be kept: %q", result.Text)
	}
	if !result.Structured {
		t.Fatalf("a parsed answer must be reported as structured")
	}

	// The terminal report must not dump the raw JSON next to the fields it was
	// parsed into.
	rendered := result.RenderText()
	if strings.Contains(rendered, "\"findings\"") {
		t.Fatalf("the rendered report repeats the raw answer:\n%s", rendered)
	}
	if !strings.Contains(rendered, "One concurrency problem.") {
		t.Fatalf("the rendered report is missing the summary:\n%s", rendered)
	}

	// The prompt must state the envelope even when the provider cannot enforce
	// a schema, which is the case here.
	system := model.requests[0].Messages[0].Text()
	if !strings.Contains(system, "\"findings\"") {
		t.Fatalf("the system prompt does not describe the envelope: %q", system)
	}
	if model.requests[0].ResponseSchema != nil {
		t.Fatalf("a provider without structured output must not be sent a schema")
	}
}

func TestRunnerSendsTheSchemaWhenTheModelSupportsIt(t *testing.T) {
	model := &scriptedModel{
		name:         "test",
		capabilities: testCapabilities(),
		scripts:      [][]llm.Event{answerEvents(`{"summary":"ok","findings":[]}`, llm.Usage{})},
	}

	runner, _ := newTestRunner(t, model, runnerTestOptions{
		outputMode:       OutputStructured,
		structuredOutput: true,
	})
	if _, err := runner.Run(context.Background(), Task{Instruction: "Review"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	request := model.requests[0]
	if request.ResponseSchema == nil {
		t.Fatalf("the schema must be sent to a capable provider")
	}
	if request.ResponseSchemaName != structuredSchemaName {
		t.Fatalf("schema name = %q", request.ResponseSchemaName)
	}
	if err := request.ResponseSchema.Validate(); err != nil {
		t.Fatalf("the schema is invalid: %v", err)
	}
}

func TestRunnerKeepsAnAnswerWithBrokenFraming(t *testing.T) {
	model := &scriptedModel{
		name:         "test",
		capabilities: testCapabilities(),
		scripts:      [][]llm.Event{answerEvents("I could not parse anything.", llm.Usage{})},
	}

	runner, _ := newTestRunner(t, model, runnerTestOptions{outputMode: OutputStructured})
	result, err := runner.Run(context.Background(), Task{Instruction: "Review"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Status != StatusCompleted {
		t.Fatalf("status = %q", result.Status)
	}
	if len(result.Findings) != 0 {
		t.Fatalf("findings = %+v", result.Findings)
	}
	if !strings.Contains(result.Summary, "could not parse") {
		t.Fatalf("summary = %q", result.Summary)
	}
}

func TestRunnerExecutesToolsAndContinues(t *testing.T) {
	model := &scriptedModel{
		name:         "test",
		capabilities: testCapabilities(),
		scripts: [][]llm.Event{
			toolCallEvents(llm.Usage{InputTokens: 50, OutputTokens: 10},
				llm.ToolCall{ID: "call-1", Name: "repo.read", Arguments: `{"path":"main.go"}`}),
			answerEvents("Read it.", llm.Usage{InputTokens: 70, OutputTokens: 15}),
		},
	}

	runner, tool := newTestRunner(t, model, runnerTestOptions{toolOutput: "package main"})
	result, err := runner.Run(context.Background(), Task{Instruction: "Review main.go"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Status != StatusCompleted || result.Text != "Read it." {
		t.Fatalf("result = %+v", result)
	}
	if result.Usage.Rounds != 2 {
		t.Fatalf("rounds = %d, want 2", result.Usage.Rounds)
	}
	if result.Usage.ToolCalls != 1 {
		t.Fatalf("tool calls = %d, want 1", result.Usage.ToolCalls)
	}
	if result.Usage.InputTokens != 120 || result.Usage.OutputTokens != 25 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if len(tool.seen) != 1 || tool.seen[0] != `{"path":"main.go"}` {
		t.Fatalf("tool arguments = %v", tool.seen)
	}

	// The second request must carry the tool result, matched to the call id.
	second := model.requests[1]
	if len(second.Messages) != 4 {
		t.Fatalf("messages = %d, want 4", len(second.Messages))
	}
	last := second.Messages[3]
	if last.Role != llm.RoleTool || last.ToolResult == nil {
		t.Fatalf("last message = %+v", last)
	}
	if last.ToolResult.ToolCallID != "call-1" || last.ToolResult.Content != "package main" {
		t.Fatalf("tool result = %+v", last.ToolResult)
	}
	if last.ToolResult.IsError {
		t.Fatalf("a successful tool must not be reported as an error")
	}
}

func TestRunnerHandsAToolFailureBackToTheModel(t *testing.T) {
	model := &scriptedModel{
		name:         "test",
		capabilities: testCapabilities(),
		scripts: [][]llm.Event{
			toolCallEvents(llm.Usage{},
				llm.ToolCall{ID: "call-1", Name: "repo.read", Arguments: `{"path":"missing.go"}`}),
			answerEvents("The file does not exist.", llm.Usage{}),
		},
	}

	runner, _ := newTestRunner(t, model, runnerTestOptions{
		toolErr: apperrors.New(apperrors.KindTool, "repo.read", "the file does not exist"),
	})
	result, err := runner.Run(context.Background(), Task{Instruction: "Review"})
	if err != nil {
		t.Fatalf("a failing tool must not fail the task: %v", err)
	}

	if result.Status != StatusCompleted {
		t.Fatalf("status = %q", result.Status)
	}
	last := model.requests[1].Messages[3]
	if !last.ToolResult.IsError {
		t.Fatalf("the failure must be marked: %+v", last.ToolResult)
	}
	if !strings.Contains(last.ToolResult.Content, "does not exist") {
		t.Fatalf("the failure must be explained: %q", last.ToolResult.Content)
	}
}

func TestRunnerRefusesADeniedTool(t *testing.T) {
	model := &scriptedModel{
		name:         "test",
		capabilities: testCapabilities(),
		scripts: [][]llm.Event{
			toolCallEvents(llm.Usage{},
				llm.ToolCall{ID: "call-1", Name: "git.diff", Arguments: `{}`}),
		},
	}

	runner, _ := newTestRunner(t, model, runnerTestOptions{})
	result, err := runner.Run(context.Background(), Task{Instruction: "Review"})
	if err == nil {
		t.Fatalf("a denied tool must fail the task")
	}
	if kind := apperrors.KindOf(err); kind != apperrors.KindPermission {
		t.Fatalf("kind = %s, want %s", kind, apperrors.KindPermission)
	}
	if result.Status != StatusFailed {
		t.Fatalf("status = %q", result.Status)
	}
	if result.Error == "" {
		t.Fatalf("the result must explain the failure")
	}
}

func TestRunnerRefusesAnUnknownTool(t *testing.T) {
	model := &scriptedModel{
		name:         "test",
		capabilities: testCapabilities(),
		scripts: [][]llm.Event{
			toolCallEvents(llm.Usage{},
				llm.ToolCall{ID: "call-1", Name: "repo.read", Arguments: `{}`}),
		},
	}

	runner, _ := newTestRunner(t, model, runnerTestOptions{})
	// Empty the registry so that the granted name is no longer known.
	runner.options.Executor = tools.NewExecutor(tools.NewRegistry(),
		security.NewGrant([]string{"repo.read"}, nil, nil), tools.Env{})

	result, err := runner.Run(context.Background(), Task{Instruction: "Review"})
	if err == nil {
		t.Fatalf("an unknown tool must fail the task")
	}
	if kind := apperrors.KindOf(err); kind != apperrors.KindTool {
		t.Fatalf("kind = %s, want %s", kind, apperrors.KindTool)
	}
	if result.Status != StatusFailed {
		t.Fatalf("status = %q", result.Status)
	}
}

func TestRunnerStopsAtTheRoundLimit(t *testing.T) {
	model := &scriptedModel{
		name:         "test",
		capabilities: testCapabilities(),
		scripts: [][]llm.Event{
			toolCallEvents(llm.Usage{}, llm.ToolCall{ID: "1", Name: "repo.read", Arguments: `{}`}),
			toolCallEvents(llm.Usage{}, llm.ToolCall{ID: "2", Name: "repo.read", Arguments: `{}`}),
			toolCallEvents(llm.Usage{}, llm.ToolCall{ID: "3", Name: "repo.read", Arguments: `{}`}),
		},
	}

	runner, _ := newTestRunner(t, model, runnerTestOptions{budget: Budget{MaxRounds: 2}})
	result, err := runner.Run(context.Background(), Task{Instruction: "Review"})
	if err == nil {
		t.Fatalf("exceeding the round budget must fail the task")
	}
	if kind := apperrors.KindOf(err); kind != apperrors.KindBudgetExceeded {
		t.Fatalf("kind = %s, want %s", kind, apperrors.KindBudgetExceeded)
	}
	if result.Status != StatusBudgetExceeded {
		t.Fatalf("status = %q", result.Status)
	}
	if len(model.requests) != 2 {
		t.Fatalf("model calls = %d, want 2", len(model.requests))
	}
	if result.Usage.Rounds != 2 {
		t.Fatalf("rounds = %d", result.Usage.Rounds)
	}
}

func TestRunnerStopsAtTheToolCallLimit(t *testing.T) {
	model := &scriptedModel{
		name:         "test",
		capabilities: testCapabilities(),
		scripts: [][]llm.Event{
			toolCallEvents(llm.Usage{},
				llm.ToolCall{ID: "1", Name: "repo.read", Arguments: `{}`},
				llm.ToolCall{ID: "2", Name: "repo.read", Arguments: `{}`}),
		},
	}

	runner, tool := newTestRunner(t, model, runnerTestOptions{budget: Budget{
		MaxRounds:    3,
		MaxToolCalls: 1,
	}})
	result, err := runner.Run(context.Background(), Task{Instruction: "Review"})
	if err == nil {
		t.Fatalf("exceeding the tool call budget must fail the task")
	}
	if kind := apperrors.KindOf(err); kind != apperrors.KindBudgetExceeded {
		t.Fatalf("kind = %s", kind)
	}
	if result.Status != StatusBudgetExceeded {
		t.Fatalf("status = %q", result.Status)
	}
	if len(tool.seen) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(tool.seen))
	}
}

func TestRunnerStopsAtTheTokenBudget(t *testing.T) {
	model := &scriptedModel{
		name:         "test",
		capabilities: testCapabilities(),
		scripts: [][]llm.Event{
			toolCallEvents(llm.Usage{InputTokens: 500, OutputTokens: 100},
				llm.ToolCall{ID: "1", Name: "repo.read", Arguments: `{}`}),
			answerEvents("done", llm.Usage{InputTokens: 10, OutputTokens: 10}),
		},
	}

	runner, _ := newTestRunner(t, model, runnerTestOptions{budget: Budget{
		MaxRounds:       5,
		MaxInputTokens:  100,
		MaxOutputTokens: 1000,
	}})
	result, err := runner.Run(context.Background(), Task{Instruction: "Review"})
	if err == nil {
		t.Fatalf("exceeding the input budget must fail the task")
	}
	if result.Status != StatusBudgetExceeded {
		t.Fatalf("status = %q", result.Status)
	}
	if !strings.Contains(result.Error, "input tokens") {
		t.Fatalf("error = %q", result.Error)
	}
}

func TestRunnerReportsATimeout(t *testing.T) {
	model := &scriptedModel{
		name:         "test",
		capabilities: testCapabilities(),
		scripts:      [][]llm.Event{answerEvents("late", llm.Usage{})},
	}

	runner, _ := newTestRunner(t, model, runnerTestOptions{budget: Budget{
		MaxRounds: 2,
		Timeout:   time.Nanosecond,
	}})
	result, err := runner.Run(context.Background(), Task{Instruction: "Review"})
	if err == nil {
		t.Fatalf("a task past its deadline must fail")
	}
	if kind := apperrors.KindOf(err); kind != apperrors.KindTimeout {
		t.Fatalf("kind = %s, want %s", kind, apperrors.KindTimeout)
	}
	if result.Status != StatusTimeout {
		t.Fatalf("status = %q", result.Status)
	}
}

func TestRunnerReportsCancellation(t *testing.T) {
	model := &scriptedModel{
		name:         "test",
		capabilities: testCapabilities(),
		scripts:      [][]llm.Event{answerEvents("never", llm.Usage{})},
	}

	runner, _ := newTestRunner(t, model, runnerTestOptions{budget: Budget{MaxRounds: 2}})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := runner.Run(ctx, Task{Instruction: "Review"})
	if err == nil {
		t.Fatalf("a cancelled task must fail")
	}
	if kind := apperrors.KindOf(err); kind != apperrors.KindCancelled {
		t.Fatalf("kind = %s, want %s", kind, apperrors.KindCancelled)
	}
	if result.Status != StatusCancelled {
		t.Fatalf("status = %q", result.Status)
	}
}

func TestRunnerRejectsAnEmptyTask(t *testing.T) {
	model := &scriptedModel{name: "test", capabilities: testCapabilities()}
	runner, _ := newTestRunner(t, model, runnerTestOptions{})

	result, err := runner.Run(context.Background(), Task{Instruction: "   "})
	if err == nil {
		t.Fatalf("an empty task must be refused")
	}
	if kind := apperrors.KindOf(err); kind != apperrors.KindInvalidArgument {
		t.Fatalf("kind = %s", kind)
	}
	if result.Status != StatusFailed || len(model.requests) != 0 {
		t.Fatalf("result = %+v, requests = %d", result, len(model.requests))
	}
}

func TestNewRunnerRefusesAModelWithoutToolCalling(t *testing.T) {
	model := &scriptedModel{
		name: "test",
		capabilities: llm.ModelCapabilities{
			Streaming:        true,
			SystemMessage:    true,
			MaxContextTokens: 8192,
		},
	}

	registry := tools.NewRegistry()
	registry.Register(&fakeTool{name: "repo.read"})
	grant := security.NewGrant([]string{"repo.read"}, nil, nil)

	_, err := NewRunner(RunnerOptions{
		Profile: Profile{
			Name:       "test-agent",
			OutputMode: OutputText,
			Tools:      grant.Names(),
			Grant:      grant,
		},
		Model:        model,
		ModelName:    "test-model",
		Capabilities: model.capabilities,
		Executor:     tools.NewExecutor(registry, grant, tools.Env{}),
		Budget:       Budget{MaxRounds: 2},
	})
	if err == nil {
		t.Fatalf("a model without tool calling must be refused")
	}
	if kind := apperrors.KindOf(err); kind != apperrors.KindCapability {
		t.Fatalf("kind = %s, want %s", kind, apperrors.KindCapability)
	}
}

func TestNewRunnerRequiresAModelAndAModelName(t *testing.T) {
	if _, err := NewRunner(RunnerOptions{}); err == nil {
		t.Fatalf("a runner without a model must be refused")
	}

	_, err := NewRunner(RunnerOptions{
		Model:        &scriptedModel{name: "test", capabilities: testCapabilities()},
		Capabilities: testCapabilities(),
	})
	if err == nil {
		t.Fatalf("a runner without a model name must be refused")
	}
}

func TestRunnerLimitsToolOutput(t *testing.T) {
	model := &scriptedModel{
		name:         "test",
		capabilities: testCapabilities(),
		scripts: [][]llm.Event{
			toolCallEvents(llm.Usage{},
				llm.ToolCall{ID: "1", Name: "repo.read", Arguments: `{}`}),
			answerEvents("done", llm.Usage{}),
		},
	}

	runner, _ := newTestRunner(t, model, runnerTestOptions{
		budget:     Budget{MaxRounds: 3, MaxToolOutputBytes: 16},
		toolOutput: strings.Repeat("abcdefghij", 10),
	})
	if _, err := runner.Run(context.Background(), Task{Instruction: "Review"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	result := model.requests[1].Messages[3].ToolResult
	if !result.Truncated {
		t.Fatalf("the result must be marked as truncated")
	}
	if !strings.Contains(result.Content, "truncated at the agent budget") {
		t.Fatalf("content = %q", result.Content)
	}
	if len(result.Content) > len(toolOutputTruncation)+16 {
		t.Fatalf("content was not cut to the budget: %d bytes", len(result.Content))
	}
}

func TestRunnerCompactsALongConversation(t *testing.T) {
	scripts := make([][]llm.Event, 0, 6)
	for i := 0; i < 5; i++ {
		scripts = append(scripts, toolCallEvents(llm.Usage{InputTokens: 10, OutputTokens: 5},
			llm.ToolCall{ID: "call", Name: "repo.read", Arguments: `{}`}))
	}
	scripts = append(scripts, answerEvents("done", llm.Usage{}))

	model := &scriptedModel{name: "test", capabilities: testCapabilities(), scripts: scripts}

	// A window small enough that the conversation must be compacted, and tool
	// output large enough that the elision rule applies to it.
	runner, _ := newTestRunner(t, model, runnerTestOptions{
		budget:     Budget{MaxRounds: 8, MaxContextTokens: 1500},
		toolOutput: strings.Repeat("data ", 1000),
	})
	result, err := runner.Run(context.Background(), Task{Instruction: "Review"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("status = %q", result.Status)
	}

	last := model.requests[len(model.requests)-1]
	found := false
	for _, message := range last.Messages {
		if message.ToolResult != nil && strings.Contains(message.ToolResult.Content, "elided") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the conversation was not compacted")
	}

	// The task itself must survive compaction.
	if !strings.Contains(last.Messages[1].Text(), "Review") {
		t.Fatalf("the task was lost: %q", last.Messages[1].Text())
	}
}

func TestParseEnvelopeIsTolerant(t *testing.T) {
	cases := []struct {
		name string
		text string
		ok   bool
	}{
		{name: "plain object", text: `{"summary":"s","findings":[]}`, ok: true},
		{name: "code fence", text: "```json\n{\"summary\":\"s\",\"findings\":[]}\n```", ok: true},
		{name: "prose around it", text: "Sure:\n{\"summary\":\"s\",\"findings\":[]}\nDone.", ok: true},
		{name: "no json", text: "I found nothing.", ok: false},
		{name: "broken json", text: "{\"summary\": }", ok: false},
		{name: "empty object", text: "{}", ok: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			envelope, ok := parseEnvelope(testCase.text)
			if ok != testCase.ok {
				t.Fatalf("ok = %v, want %v", ok, testCase.ok)
			}
			if ok && envelope.Summary != "s" {
				t.Fatalf("summary = %q", envelope.Summary)
			}
		})
	}
}

func TestCompactKeepsShortConversations(t *testing.T) {
	messages := []llm.Message{
		llm.NewSystemMessage("system"),
		llm.NewUserMessage("task"),
		llm.NewAssistantMessage(strings.Repeat("x", 5000)),
	}

	result, err := ElisionCompactor{}.Compact(messages, CompactionOptions{})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if result.Report.Changed {
		t.Fatalf("a short conversation must not be compacted")
	}
	if len(result.Messages) != len(messages) {
		t.Fatalf("messages = %d", len(result.Messages))
	}
}

func TestCompactElidesLargeToolOutput(t *testing.T) {
	messages := []llm.Message{
		llm.NewSystemMessage("system"),
		llm.NewUserMessage("task"),
	}
	for i := 0; i < 10; i++ {
		messages = append(messages, llm.NewAssistantToolCallMessage(
			llm.ToolCall{ID: "call", Name: "repo.read", Arguments: `{"path":"internal/agent/loop.go"}`}))
		messages = append(messages, llm.NewToolResultMessage(llm.ToolResult{
			ToolCallID: "call",
			Name:       "repo.read",
			Content:    "package agent\n" + strings.Repeat("data ", 1000),
		}))
	}

	result, err := ElisionCompactor{}.Compact(messages, CompactionOptions{})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if !result.Report.Changed {
		t.Fatalf("a long conversation must be compacted")
	}
	if len(result.Messages) != len(messages) {
		t.Fatalf("compaction must preserve the message count, got %d", len(result.Messages))
	}
	if estimateTokens(result.Messages) >= estimateTokens(messages) {
		t.Fatalf("compaction must reduce the estimate")
	}
	if result.Report.Elided == 0 || result.Report.AfterTokens >= result.Report.BeforeTokens {
		t.Fatalf("report = %+v", result.Report)
	}

	// The most recent exchanges stay intact so the model can continue.
	for _, message := range result.Messages[len(result.Messages)-DefaultKeepRecent:] {
		if message.ToolResult != nil && strings.Contains(message.ToolResult.Content, "elided") {
			t.Fatalf("a recent tool result was elided")
		}
	}

	// The marker keeps enough context for the model to decide whether to look
	// again: which tool, how much it returned and how it started.
	marker := result.Messages[3].ToolResult.Content
	for _, want := range []string{"repo.read", "KiB", "it began with: package agent"} {
		if !strings.Contains(marker, want) {
			t.Fatalf("the marker is missing %q: %s", want, marker)
		}
	}
}

func TestCompactSummaryDescribesTheWorkSoFar(t *testing.T) {
	messages := []llm.Message{
		llm.NewSystemMessage("system"),
		llm.NewUserMessage("task"),
	}
	for i := 0; i < 5; i++ {
		messages = append(messages, llm.NewAssistantToolCallMessage(
			llm.ToolCall{ID: "call", Name: "repo.read", Arguments: `{"path":"internal/agent/loop.go"}`},
			llm.ToolCall{ID: "call", Name: "git.diff", Arguments: `{}`},
		))
		messages = append(messages, llm.NewToolResultMessage(llm.ToolResult{
			ToolCallID: "call",
			Name:       "repo.read",
			Content:    strings.Repeat("data ", 2000),
		}))
	}

	result, err := ElisionCompactor{}.Compact(messages, CompactionOptions{})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if !result.Report.Changed {
		t.Fatalf("the conversation must be compacted")
	}

	summary := result.Report.Summary
	for _, want := range []string{
		"Compacted history",
		"repo.read (5)",
		"git.diff (5)",
		"internal/agent/loop.go",
		"do not guess",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("the summary is missing %q:\n%s", want, summary)
		}
	}
}

func TestCompactRespectsTheElideThreshold(t *testing.T) {
	messages := []llm.Message{
		llm.NewSystemMessage("system"),
		llm.NewUserMessage("task"),
	}
	for i := 0; i < 10; i++ {
		messages = append(messages, llm.NewAssistantToolCallMessage(
			llm.ToolCall{ID: "call", Name: "repo.stat", Arguments: `{}`}))
		messages = append(messages, llm.NewToolResultMessage(llm.ToolResult{
			ToolCallID: "call",
			Name:       "repo.stat",
			Content:    "small result",
		}))
	}

	result, err := ElisionCompactor{}.Compact(messages, CompactionOptions{ElideThreshold: 4})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if !result.Report.Changed || result.Report.Elided == 0 {
		t.Fatalf("a low threshold must elide small results: %+v", result.Report)
	}

	// With the default threshold nothing is large enough to elide.
	result, err = ElisionCompactor{}.Compact(messages, CompactionOptions{})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if result.Report.Changed {
		t.Fatalf("small results are the evidence a finding cites: %+v", result.Report)
	}
}

// failingCompactor always fails, to prove the loop keeps going.
type failingCompactor struct{}

func (failingCompactor) Compact([]llm.Message, CompactionOptions) (CompactionResult, error) {
	return CompactionResult{}, errors.New("summariser unavailable")
}

// recordingCompactor records what the loop asked for.
type recordingCompactor struct {
	calls  int
	report CompactionReport
	window int
}

func (c *recordingCompactor) Compact(messages []llm.Message, options CompactionOptions) (CompactionResult, error) {
	c.calls++
	c.window = options.Window
	c.report = CompactionReport{
		Elided:       2,
		BeforeTokens: 9000,
		AfterTokens:  3000,
		Summary:      "## Compacted history\n\nnothing important",
		Changed:      true,
	}
	return CompactionResult{Messages: messages, Report: c.report}, nil
}

// recordingRecorder collects the events the loop emitted.
type recordingRecorder struct {
	messages    []llm.Message
	toolCalls   int
	compactions []CompactionReport
}

func (r *recordingRecorder) Message(message llm.Message) { r.messages = append(r.messages, message) }
func (r *recordingRecorder) ToolCall(llm.ToolCall, llm.ToolResult) {
	r.toolCalls++
}
func (r *recordingRecorder) Compaction(report CompactionReport) {
	r.compactions = append(r.compactions, report)
}

func TestEstimateAndFitHelpers(t *testing.T) {
	messages := []llm.Message{
		llm.NewSystemMessage("1234"),
		llm.NewUserMessage("5678"),
	}
	if estimateTokens(messages) <= 0 {
		t.Fatalf("the estimate must be positive")
	}
	if !fitsContext(messages, 0) {
		t.Fatalf("an unknown window means no compaction")
	}
	if !fitsContext(messages, 1000) {
		t.Fatalf("a small conversation fits a large window")
	}
	if fitsContext([]llm.Message{llm.NewSystemMessage(strings.Repeat("x", 8000))}, 100) {
		t.Fatalf("a large conversation does not fit a small window")
	}

	// Compaction starts below the window edge, leaving room for the answer and
	// for the tool definitions the estimate does not count.
	window := 1000
	exact := int(float64(window) * CompactionThreshold)
	small := []llm.Message{llm.NewSystemMessage(strings.Repeat("x", (exact-100)*4))}
	if !fitsContext(small, window) {
		t.Fatalf("a conversation below the threshold must fit: %d tokens", estimateTokens(small))
	}
	large := []llm.Message{llm.NewSystemMessage(strings.Repeat("x", (exact+100)*4))}
	if fitsContext(large, window) {
		t.Fatalf("a conversation above the threshold must not fit: %d tokens", estimateTokens(large))
	}
}

func TestRunnerRebuildsTheSystemPromptWithTheSummary(t *testing.T) {
	model := &scriptedModel{
		name:         "test",
		capabilities: testCapabilities(),
		scripts: [][]llm.Event{
			toolCallEvents(llm.Usage{}, llm.ToolCall{ID: "1", Name: "repo.read", Arguments: `{}`}),
			answerEvents("done", llm.Usage{}),
		},
	}

	compactor := &recordingCompactor{}
	recorder := &recordingRecorder{}

	// A window small enough that the conversation has to be compacted even on
	// the first round: the point is the wiring, not the arithmetic.
	runner, _ := newTestRunner(t, model, runnerTestOptions{
		budget:    Budget{MaxRounds: 4, MaxContextTokens: 200},
		compactor: compactor,
		recorder:  recorder,
	})

	result, err := runner.Run(context.Background(), Task{Instruction: "Review"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("status = %q", result.Status)
	}

	if compactor.calls == 0 {
		t.Fatalf("the compactor was never asked")
	}
	if compactor.window != 200 {
		t.Fatalf("the compactor was given window %d", compactor.window)
	}
	if len(recorder.compactions) != compactor.calls {
		t.Fatalf("compactions = %d, want %d", len(recorder.compactions), compactor.calls)
	}

	// The later request must carry the summary of what was compacted.
	last := model.requests[len(model.requests)-1]
	if !strings.Contains(last.Messages[0].Text(), "Compacted history") {
		t.Fatalf("the system prompt was not rebuilt:\n%s", last.Messages[0].Text())
	}
	// And the transcript must contain the task, the assistant turn and the
	// tool result.
	if len(recorder.messages) < 4 {
		t.Fatalf("recorded messages = %d", len(recorder.messages))
	}
	if recorder.toolCalls != 1 {
		t.Fatalf("recorded tool calls = %d", recorder.toolCalls)
	}
}

func TestRunnerSurvivesAFailingCompactor(t *testing.T) {
	model := &scriptedModel{
		name:         "test",
		capabilities: testCapabilities(),
		scripts:      [][]llm.Event{answerEvents("done", llm.Usage{})},
	}

	runner, _ := newTestRunner(t, model, runnerTestOptions{
		budget:    Budget{MaxRounds: 2, MaxContextTokens: 100},
		compactor: failingCompactor{},
	})

	result, err := runner.Run(context.Background(), Task{Instruction: "Review"})
	if err != nil {
		t.Fatalf("a failing summariser must not fail the task: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("status = %q", result.Status)
	}
}

func TestResultRenderText(t *testing.T) {
	result := &Result{
		Status:  StatusCompleted,
		Agent:   "reviewer",
		Model:   "qwen3",
		Summary: "One problem found.",
		Findings: []Finding{{
			Severity:    "High",
			Category:    "security",
			File:        "main.go",
			Line:        7,
			Title:       "A secret is logged",
			Description: "The token is written\nto stderr.",
			Evidence:    "log.Print(token)",
			Suggestion:  "Redact it.",
			Confidence:  0.9,
		}},
		Usage:      Usage{InputTokens: 10, OutputTokens: 2, ToolCalls: 1, Rounds: 1},
		StartedAt:  time.Now(),
		FinishedAt: time.Now(),
	}

	rendered := result.RenderText()
	for _, want := range []string{
		"status:   completed",
		"agent:    reviewer",
		"main.go:7",
		"A secret is logged",
		"Redact it.",
		"confidence: 0.90",
		"1 tool calls",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered report is missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "to stderr") && strings.Contains(rendered, "\n   detail:     The token is written\nto stderr.") {
		t.Fatalf("multi-line fields must be collapsed")
	}

	var empty *Result
	if empty.RenderText() != "" {
		t.Fatalf("a nil result renders nothing")
	}
}

func TestParseEnvelopeAcceptsAnEmptyFindingList(t *testing.T) {
	envelope, ok := parseEnvelope(`{"summary":"nothing to report","findings":[]}`)
	if !ok {
		t.Fatalf("a summary alone must be accepted")
	}
	if envelope.Summary != "nothing to report" || len(envelope.Findings) != 0 {
		t.Fatalf("envelope = %+v", envelope)
	}
}
