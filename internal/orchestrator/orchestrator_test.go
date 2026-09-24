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

package orchestrator

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pawagents/pawagents/internal/agent"
	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/llm"
	"github.com/pawagents/pawagents/internal/provider"
)

// fakeProvider is a provider implementation a test can script.
type fakeProvider struct {
	name         string
	providerType string
	capabilities llm.ModelCapabilities
	scripts      [][]llm.Event
	capabilityfn func(ctx context.Context, model string) (llm.ModelCapabilities, error)
	requests     []*llm.GenerateRequest
}

func (p *fakeProvider) Name() string { return p.name }

func (p *fakeProvider) Type() string { return p.providerType }

func (p *fakeProvider) Capabilities(ctx context.Context, model string) (llm.ModelCapabilities, error) {
	if p.capabilityfn != nil {
		return p.capabilityfn(ctx, model)
	}
	return p.capabilities, nil
}

func (p *fakeProvider) Generate(_ context.Context, request *llm.GenerateRequest) (llm.Stream, error) {
	p.requests = append(p.requests, request)
	if len(p.scripts) == 0 {
		return nil, errors.New("the fake provider has no script left")
	}
	events := p.scripts[0]
	p.scripts = p.scripts[1:]
	return llm.SliceStream(events...), nil
}

// providerCapabilities are what the fixture provider reports for its model.
func providerCapabilities() llm.ModelCapabilities {
	return llm.ModelCapabilities{
		ToolCalling:      true,
		Streaming:        true,
		SystemMessage:    true,
		MaxContextTokens: 32768,
		MaxOutputTokens:  4096,
	}
}

// answerEvents scripts a final answer.
func answerEvents(text string) []llm.Event {
	return []llm.Event{
		llm.NewStartEvent("qwen3"),
		llm.NewTextDeltaEvent(text),
		llm.NewUsageEvent(llm.Usage{InputTokens: 30, OutputTokens: 12}),
		llm.NewFinishEvent(llm.FinishReasonStop, "qwen3"),
	}
}

// toolCallEvents scripts one round of tool calls.
func toolCallEvents(calls ...llm.ToolCall) []llm.Event {
	events := []llm.Event{llm.NewStartEvent("qwen3")}
	for index, call := range calls {
		events = append(events, llm.NewToolCallEvent(index, call))
	}
	return append(events,
		llm.NewUsageEvent(llm.Usage{InputTokens: 40, OutputTokens: 8}),
		llm.NewFinishEvent(llm.FinishReasonToolCalls, "qwen3"))
}

// testConfig builds a configuration with one provider, one model and one agent.
func testConfig(t *testing.T, mutate func(cfg *config.Config)) *config.Config {
	t.Helper()

	// Sessions are part of every run, so the tests keep them inside a temporary
	// home instead of the real one.
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv(config.EnvSessionsDir, "")

	cfg := &config.Config{
		Providers: map[string]*config.Provider{
			"local": {Type: config.ProviderTypeOllama, BaseURL: "http://127.0.0.1:11434"},
		},
		Models: map[string]*config.Model{
			"local-model": {Primary: &config.ModelRef{Provider: "local", Model: "qwen3"}},
		},
		Agents: map[string]*config.Agent{
			"reviewer": {
				Model:        "local-model",
				Instructions: "You review Go code carefully.",
				Tools:        []string{"repo.read"},
				OutputMode:   config.OutputModeStructured,
			},
		},
	}

	config.ApplyDefaults(cfg)
	cfg.Normalize()

	if mutate != nil {
		mutate(cfg)
	}
	return cfg
}

// testOrchestrator builds an orchestrator whose provider types are served by
// the given implementations.
func testOrchestrator(t *testing.T, cfg *config.Config, implementations map[string]provider.Provider) *Orchestrator {
	t.Helper()

	registry := provider.NewRegistry(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for providerType, implementation := range implementations {
		impl := implementation
		registry.Register(providerType, func(context.Context, provider.Options) (provider.Provider, error) {
			return impl, nil
		})
	}
	t.Cleanup(func() { _ = registry.Close() })

	instance, err := New(cfg, registry, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return instance
}

// tempWorkspace writes a small Go file and returns the directory.
func tempWorkspace(t *testing.T) (string, string) {
	t.Helper()

	directory := t.TempDir()
	path := filepath.Join(directory, "main.go")
	if err := os.WriteFile(path, []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatalf("write the workspace file: %v", err)
	}
	return directory, path
}

func TestResolveFallsBackToTheNextTarget(t *testing.T) {
	cfg := testConfig(t, func(cfg *config.Config) {
		cfg.Models["local-model"] = &config.Model{
			Primary:  &config.ModelRef{Provider: "cloud", Model: "cloud-model"},
			Fallback: []config.ModelRef{{Provider: "local", Model: "qwen3"}},
		}
	})

	// Only the Ollama provider type has an implementation, so the primary
	// target cannot be built and the task must move to the fallback.
	fallback := &fakeProvider{name: "local", providerType: config.ProviderTypeOllama, capabilities: providerCapabilities()}
	orchestrator := testOrchestrator(t, cfg, map[string]provider.Provider{
		config.ProviderTypeOllama: fallback,
	})

	resolved, err := orchestrator.resolve(context.Background(), "local-model", resolveOptions{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Model != "qwen3" || resolved.ProviderName != "local" {
		t.Fatalf("binding = %+v", resolved)
	}
	if len(resolved.Skipped) != 1 || resolved.Skipped[0].Target != "cloud/cloud-model" {
		t.Fatalf("skipped = %v", resolved.Skipped)
	}
	if resolved.Skipped[0].Reason == "" {
		t.Fatalf("a skipped target must explain why: %+v", resolved.Skipped[0])
	}
}

func TestResolveFailsWhenNoTargetCanServeTheTask(t *testing.T) {
	cfg := testConfig(t, nil)
	orchestrator := testOrchestrator(t, cfg, nil)

	_, err := orchestrator.resolve(context.Background(), "local-model", resolveOptions{})
	if err == nil {
		t.Fatalf("a model whose provider cannot be built must fail")
	}
	// A single target that cannot be built is reported as the capability gap it
	// is, rather than as a generic provider failure.
	if kind := apperrors.KindOf(err); kind != apperrors.KindCapability {
		t.Fatalf("kind = %s, want %s", kind, apperrors.KindCapability)
	}
	if !strings.Contains(err.Error(), "local/qwen3") {
		t.Fatalf("the error must name the target: %v", err)
	}
}

func TestResolveRejectsAnUnknownAlias(t *testing.T) {
	cfg := testConfig(t, nil)
	orchestrator := testOrchestrator(t, cfg, nil)

	_, err := orchestrator.resolve(context.Background(), "nope", resolveOptions{})
	if err == nil {
		t.Fatalf("an unknown alias must fail")
	}
	if kind := apperrors.KindOf(err); kind != apperrors.KindConfig {
		t.Fatalf("kind = %s", kind)
	}
}

func TestRunDelegatesATaskToTheProvider(t *testing.T) {
	directory, _ := tempWorkspace(t)

	impl := &fakeProvider{
		name:         "local",
		providerType: config.ProviderTypeOllama,
		capabilities: providerCapabilities(),
		scripts: [][]llm.Event{
			toolCallEvents(llm.ToolCall{ID: "call-1", Name: "repo.read",
				Arguments: `{"path":"main.go"}`}),
			answerEvents(`{"summary":"README needs a note.","findings":[{"severity":"low","title":"Missing note","file":"main.go","line":1}]}`),
		},
	}

	cfg := testConfig(t, nil)
	orchestrator := testOrchestrator(t, cfg, map[string]provider.Provider{
		config.ProviderTypeOllama: impl,
	})

	result, err := orchestrator.Run(context.Background(), Request{
		Agent:     "reviewer",
		Task:      "Review the workspace",
		Workspace: directory,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Status != agent.StatusCompleted {
		t.Fatalf("status = %q", result.Status)
	}
	if result.Agent != "reviewer" || result.Provider != "local" || result.Model != "qwen3" {
		t.Fatalf("identity = %+v", result)
	}
	if result.Summary != "README needs a note." || len(result.Findings) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if result.Usage.ToolCalls != 1 || result.Usage.Rounds != 2 {
		t.Fatalf("usage = %+v", result.Usage)
	}

	// The agent instructions and the granted tool must both reach the model,
	// and the tool result must contain the file the tool actually read.
	first := impl.requests[0]
	if !strings.Contains(first.Messages[0].Text(), "You review Go code carefully.") {
		t.Fatalf("the agent instructions are missing: %q", first.Messages[0].Text())
	}
	if len(first.Tools) != 1 || first.Tools[0].Name != "repo.read" {
		t.Fatalf("tools = %+v", first.Tools)
	}
	second := impl.requests[1]
	if len(second.Messages) != 4 {
		t.Fatalf("messages = %d", len(second.Messages))
	}
	if !strings.Contains(second.Messages[3].ToolResult.Content, "package main") {
		t.Fatalf("tool content = %q", second.Messages[3].ToolResult.Content)
	}
}

func TestRunRefusesAnAgentWithToolsOnAModelWithoutToolCalling(t *testing.T) {
	impl := &fakeProvider{
		name:         "local",
		providerType: config.ProviderTypeOllama,
		capabilities: llm.ModelCapabilities{
			Streaming:        true,
			SystemMessage:    true,
			MaxContextTokens: 8192,
		},
	}

	cfg := testConfig(t, nil)
	orchestrator := testOrchestrator(t, cfg, map[string]provider.Provider{
		config.ProviderTypeOllama: impl,
	})

	_, err := orchestrator.Run(context.Background(), Request{
		Agent: "reviewer",
		Task:  "Review the workspace",
	})
	if err == nil {
		t.Fatalf("a capability mismatch must fail the task")
	}
	if kind := apperrors.KindOf(err); kind != apperrors.KindCapability {
		t.Fatalf("kind = %s, want %s", kind, apperrors.KindCapability)
	}
	if len(impl.requests) != 0 {
		t.Fatalf("no token may be spent when the model cannot do the job")
	}
}

func TestRunRefusesAnUnknownAgent(t *testing.T) {
	cfg := testConfig(t, nil)
	orchestrator := testOrchestrator(t, cfg, nil)

	_, err := orchestrator.Run(context.Background(), Request{Agent: "nope", Task: "Review"})
	if err == nil {
		t.Fatalf("an unknown agent must fail")
	}
	if kind := apperrors.KindOf(err); kind != apperrors.KindNotFound {
		t.Fatalf("kind = %s, want %s", kind, apperrors.KindNotFound)
	}
}

func TestRunRejectsAnIncompleteRequest(t *testing.T) {
	cfg := testConfig(t, nil)
	orchestrator := testOrchestrator(t, cfg, nil)

	cases := []Request{
		{Task: "Review"},
		{Agent: "reviewer"},
		{Agent: "reviewer", Task: "Review", MaxRounds: -1},
		{Agent: "reviewer", Task: "Review", Timeout: -time.Second},
		{Agent: "reviewer", Task: "Review", OutputMode: "yaml"},
	}

	for _, request := range cases {
		if _, err := orchestrator.Run(context.Background(), request); err == nil {
			t.Fatalf("request %+v must be refused", request)
		}
	}
}

func TestRunUsesTheConfiguredOutputModeAndOverride(t *testing.T) {
	cfg := testConfig(t, func(cfg *config.Config) {
		cfg.Agents["reviewer"].OutputMode = config.OutputModeText
	})
	impl := &fakeProvider{
		name:         "local",
		providerType: config.ProviderTypeOllama,
		capabilities: providerCapabilities(),
		scripts:      [][]llm.Event{answerEvents("plain answer"), answerEvents("plain answer")},
	}
	orchestrator := testOrchestrator(t, cfg, map[string]provider.Provider{
		config.ProviderTypeOllama: impl,
	})

	// The agent asks for text: the finding envelope must not be invented.
	result, err := orchestrator.Run(context.Background(), Request{Agent: "reviewer", Task: "Review"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Findings) != 0 || result.Summary != "plain answer" {
		t.Fatalf("result = %+v", result)
	}

	// The request can ask for the structured envelope instead.
	result, err = orchestrator.Run(context.Background(), Request{
		Agent:      "reviewer",
		Task:       "Review",
		OutputMode: config.OutputModeStructured,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Text != "plain answer" {
		t.Fatalf("result = %+v", result)
	}
}

func TestAgentsDescribeTheConfiguredProfiles(t *testing.T) {
	cfg := testConfig(t, nil)
	orchestrator := testOrchestrator(t, cfg, nil)

	agents := orchestrator.Agents()
	if len(agents) != 1 {
		t.Fatalf("agents = %+v", agents)
	}

	summary := agents[0]
	if summary.Name != "reviewer" || summary.ModelAlias != "local-model" {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.Provider != "local" || summary.Model != "qwen3" {
		t.Fatalf("summary target = %+v", summary)
	}
	if summary.OutputMode != config.OutputModeStructured {
		t.Fatalf("output mode = %q", summary.OutputMode)
	}
	if summary.MaxRounds != config.DefaultMaxRounds {
		t.Fatalf("max rounds = %d", summary.MaxRounds)
	}
	if summary.Timeout != config.DefaultTimeout.String() {
		t.Fatalf("timeout = %q", summary.Timeout)
	}
	if len(summary.Tools) != 1 || summary.Tools[0] != "repo.read" {
		t.Fatalf("tools = %v", summary.Tools)
	}

	if _, err := orchestrator.Agent("nope"); err == nil {
		t.Fatalf("an unknown agent must be reported")
	}
}

func TestBudgetForAppliesDefaultsAndOverrides(t *testing.T) {
	cfg := testConfig(t, nil)
	agentCfg := cfg.Agents["reviewer"]
	capabilities := providerCapabilities()

	budget := budgetFor(agentCfg, Request{Agent: "reviewer", Task: "Review"}, capabilities)
	if budget.MaxRounds != config.DefaultMaxRounds {
		t.Fatalf("rounds = %d", budget.MaxRounds)
	}
	if budget.MaxToolCalls != config.DefaultMaxToolCalls {
		t.Fatalf("tool calls = %d", budget.MaxToolCalls)
	}
	if budget.Timeout != config.DefaultTimeout {
		t.Fatalf("timeout = %s", budget.Timeout)
	}
	if budget.MaxContextTokens != capabilities.MaxContextTokens {
		t.Fatalf("context = %d", budget.MaxContextTokens)
	}
	if budget.MaxToolOutputBytes != config.DefaultMaxToolOutputBytes {
		t.Fatalf("tool output = %d", budget.MaxToolOutputBytes)
	}

	budget = budgetFor(agentCfg, Request{
		Agent:     "reviewer",
		Task:      "Review",
		MaxRounds: 3,
		Timeout:   time.Minute,
	}, capabilities)
	if budget.MaxRounds != 3 || budget.Timeout != time.Minute {
		t.Fatalf("overrides were ignored: %+v", budget)
	}
}

func TestRequirementsForCollectsWhatAnAgentNeeds(t *testing.T) {
	cfg := testConfig(t, nil)
	requirements := RequirementsFor(cfg.Agents["reviewer"])

	if !requirements.Tools {
		t.Fatalf("the agent grants a tool, so tool calling is required")
	}
	if !requirements.SystemMessage {
		t.Fatalf("the agent has instructions, so a system message is required")
	}
	if requirements.MinContextTokens != 0 {
		t.Fatalf("no minimum window was configured: %d", requirements.MinContextTokens)
	}
	if requirements.MaxOutputTokens != 0 {
		t.Fatalf("max output must not be a model requirement: %d", requirements.MaxOutputTokens)
	}
	if requirements.StructuredOutput {
		t.Fatalf("structured output must never be required")
	}
}

func TestRequirementsForAliasMergesEveryBoundAgent(t *testing.T) {
	cfg := testConfig(t, func(cfg *config.Config) {
		cfg.Agents["plain"] = &config.Agent{Model: "local-model", OutputMode: config.OutputModeText}
		cfg.Agents["demanding"] = &config.Agent{
			Model:            "local-model",
			Instructions:     "review",
			Tools:            []string{"repo.read"},
			MaxContextTokens: 32768,
			OutputMode:       config.OutputModeText,
		}
		cfg.Agents["other"] = &config.Agent{Model: "nowhere", Instructions: "review"}
	})

	merged := RequirementsForAlias(cfg, "local-model")
	if !merged.Tools || !merged.SystemMessage {
		t.Fatalf("merged requirements = %+v", merged)
	}
	if merged.MinContextTokens != 32768 {
		t.Fatalf("the largest window requirement must win: %d", merged.MinContextTokens)
	}

	if requirements := RequirementsForAlias(cfg, "nowhere"); requirements.Tools {
		t.Fatalf("an agent without tools contributes no tool requirement")
	}
	if requirements := RequirementsForAlias(nil, "local-model"); requirements != (llm.Requirements{}) {
		t.Fatalf("a nil configuration yields no requirements: %+v", requirements)
	}
}

func TestLoadSystemPromptPrefersInlineInstructions(t *testing.T) {
	prompt, err := loadSystemPrompt(&config.Agent{Instructions: "  inline  "})
	if err != nil {
		t.Fatalf("loadSystemPrompt: %v", err)
	}
	if prompt != "inline" {
		t.Fatalf("prompt = %q", prompt)
	}

	prompt, err = loadSystemPrompt(&config.Agent{})
	if err != nil || prompt != "" {
		t.Fatalf("an agent without instructions yields an empty prompt: %q, %v", prompt, err)
	}
}

func TestLoadSystemPromptFallsBackToTheBundledCopy(t *testing.T) {
	// A prompt file that does not exist, but whose base name is bundled with
	// the binary: the agent must still get its instructions.
	missing := filepath.Join(t.TempDir(), "reviewer.md")

	prompt, err := loadSystemPrompt(&config.Agent{Prompt: missing})
	if err != nil {
		t.Fatalf("loadSystemPrompt: %v", err)
	}
	if strings.TrimSpace(prompt) == "" {
		t.Fatalf("the bundled prompt was not used")
	}

	_, err = loadSystemPrompt(&config.Agent{Prompt: filepath.Join(t.TempDir(), "nope.md")})
	if err == nil {
		t.Fatalf("a prompt that cannot be resolved must fail")
	}
	if kind := apperrors.KindOf(err); kind != apperrors.KindConfig {
		t.Fatalf("kind = %s", kind)
	}
}
