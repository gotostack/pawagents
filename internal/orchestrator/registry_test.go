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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/llm"
	"github.com/pawagents/pawagents/internal/provider"
)

// registryConfig builds a configuration that exercises the registry: a local
// provider, a cloud provider this build cannot use, an alias with a fallback,
// an alias pointing at a provider that does not exist, and four agents.
func registryConfig(t *testing.T, mutate func(cfg *config.Config)) *config.Config {
	t.Helper()

	directory := t.TempDir()
	promptPath := filepath.Join(directory, "reviewer.md")
	if err := os.WriteFile(promptPath, []byte("Review the diff and report findings."), 0o600); err != nil {
		t.Fatalf("cannot write the prompt file: %v", err)
	}

	cfg := &config.Config{
		Providers: map[string]*config.Provider{
			"local": {Type: config.ProviderTypeOllama, BaseURL: "http://127.0.0.1:11434"},
			"cloud": {Type: config.ProviderTypeOpenAIChat, BaseURL: "https://api.openai.com/v1"},
			"gone":  {Type: config.ProviderTypeOllama, Disabled: true},
			"gateway": {
				Type:    config.ProviderTypeOpenAICompat,
				BaseURL: "http://127.0.0.1:9/v1",
			},
		},
		Models: map[string]*config.Model{
			"local-model": {Primary: &config.ModelRef{Provider: "local", Model: "qwen3"}},
			"with-fallback": {
				Primary:  &config.ModelRef{Provider: "cloud", Model: "gpt-5"},
				Fallback: []config.ModelRef{{Provider: "local", Model: "qwen3"}},
			},
			"mixed": {
				Primary:  &config.ModelRef{Provider: "cloud", Model: "gpt-5"},
				Fallback: []config.ModelRef{{Provider: "gateway", Model: "fake-model"}},
			},
			"orphan": {Primary: &config.ModelRef{Provider: "nowhere", Model: "ghost"}},
		},
		Agents: map[string]*config.Agent{
			"file-reviewer": {
				Model:      "local-model",
				Prompt:     promptPath,
				Tools:      []string{"repo.read", "git.diff"},
				OutputMode: config.OutputModeStructured,
			},
			"inline-reviewer": {
				Model:        "local-model",
				Instructions: "Review carefully.",
				Tools:        []string{"repo.read"},
				OutputMode:   config.OutputModeText,
			},
			"fallback-reviewer": {
				Model:        "with-fallback",
				Instructions: "Review.",
				Tools:        []string{"repo.read"},
			},
			"broken": {
				Model:  "orphan",
				Prompt: filepath.Join(directory, "prompts", "reviewer.md"),
				Tools:  []string{"repo.read", "repo.write"},
			},
			"team": {
				Type:         config.AgentTypeTeam,
				Model:        "local-model",
				Instructions: "Review.",
			},
			"toolless": {
				Model: "local-model",
			},
			"mixed-reviewer": {
				Model:        "mixed",
				Instructions: "Review.",
				Tools:        []string{"repo.read"},
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

func TestProfilesDescribeEveryConfiguredAgent(t *testing.T) {
	cfg := registryConfig(t, nil)
	orchestrator := testOrchestrator(t, cfg, nil)

	profiles := orchestrator.Profiles()
	if len(profiles) != len(cfg.Agents) {
		t.Fatalf("profiles = %d, want %d", len(profiles), len(cfg.Agents))
	}

	byName := map[string]AgentProfile{}
	for _, profile := range profiles {
		byName[profile.Name] = profile
	}

	fileReviewer := byName["file-reviewer"]
	if fileReviewer.Prompt.Kind != PromptFile || fileReviewer.Prompt.Path == "" {
		t.Fatalf("prompt = %+v", fileReviewer.Prompt)
	}
	if fileReviewer.Prompt.Bytes == 0 {
		t.Fatalf("the prompt size was not reported: %+v", fileReviewer.Prompt)
	}
	if len(fileReviewer.Tools) != 2 || !fileReviewer.Known() {
		t.Fatalf("tools = %+v", fileReviewer.Tools)
	}
	if fileReviewer.OutputMode != config.OutputModeStructured {
		t.Fatalf("output mode = %q", fileReviewer.OutputMode)
	}
	if fileReviewer.Permissions.Filesystem != "read" || fileReviewer.Permissions.Shell != "deny" {
		t.Fatalf("permissions = %+v", fileReviewer.Permissions)
	}
	if len(fileReviewer.Targets) != 1 || fileReviewer.Targets[0].Model != "qwen3" {
		t.Fatalf("targets = %+v", fileReviewer.Targets)
	}

	inline := byName["inline-reviewer"]
	if inline.Prompt.Kind != PromptInline || inline.OutputMode != config.OutputModeText {
		t.Fatalf("inline profile = %+v", inline)
	}

	// A missing prompt falls back to the copy embedded in the binary, because
	// the file name matches a bundled prompt.
	broken := byName["broken"]
	if broken.Prompt.Kind != PromptBundled {
		t.Fatalf("prompt = %+v", broken.Prompt)
	}
	if broken.Known() {
		t.Fatalf("an unknown tool must be reported: %+v", broken.Tools)
	}
	unknown := broken.UnknownTools()
	if len(unknown) != 1 || unknown[0] != "repo.write" {
		t.Fatalf("unknown tools = %v", unknown)
	}

	team := byName["team"]
	if team.Type != TypeTeam || team.Executable {
		t.Fatalf("a team agent must not be executable: %+v", team)
	}

	toolless := byName["toolless"]
	if len(toolless.Tools) != 0 || toolless.Prompt.Kind != PromptNone {
		t.Fatalf("toolless profile = %+v", toolless)
	}
	if toolless.Budget.MaxRounds != config.DefaultMaxRounds {
		t.Fatalf("default rounds = %d", toolless.Budget.MaxRounds)
	}
	if toolless.Budget.Timeout != config.DefaultTimeout.String() {
		t.Fatalf("default timeout = %q", toolless.Budget.Timeout)
	}

	if _, err := orchestrator.Profile("nope"); err == nil {
		t.Fatalf("an unknown agent must fail")
	}
}

func TestProfileReportsAnUnresolvablePrompt(t *testing.T) {
	directory := t.TempDir()
	missing := filepath.Join(directory, "custom.md")

	cfg := registryConfig(t, func(cfg *config.Config) {
		cfg.Agents["no-prompt"] = &config.Agent{
			Model:  "local-model",
			Prompt: missing,
		}
	})
	orchestrator := testOrchestrator(t, cfg, nil)

	// Describing the agent must still work: the operator needs to see the
	// agent that is misconfigured.
	profile, err := orchestrator.Profile("no-prompt")
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if profile.Prompt.Kind != PromptNone || profile.Prompt.Path != missing {
		t.Fatalf("prompt = %+v", profile.Prompt)
	}

	if _, err := loadSystemPrompt(&config.Agent{Prompt: missing}); err == nil {
		t.Fatalf("loading an unresolvable prompt must fail")
	}
}

func TestCheckAgentWithoutAProbeUsesStaticCapabilities(t *testing.T) {
	cfg := registryConfig(t, nil)
	orchestrator := testOrchestrator(t, cfg, map[string]provider.Provider{
		config.ProviderTypeOllama: &fakeProvider{
			name:         "local",
			providerType: config.ProviderTypeOllama,
			capabilities: providerCapabilities(),
		},
	})

	report, err := orchestrator.CheckAgent(context.Background(), "inline-reviewer", false)
	if err != nil {
		t.Fatalf("CheckAgent: %v", err)
	}
	if report.Probed {
		t.Fatalf("a static check must not claim to be probed")
	}
	if report.OK {
		t.Fatalf("a static ollama model must not be assumed to support tools")
	}
	if len(report.Missing) != 1 || report.Missing[0] != "tool_calling" {
		t.Fatalf("missing = %v", report.Missing)
	}
	if report.Error == "" {
		t.Fatalf("the report must explain the failure")
	}
	if report.Model != "qwen3" || report.ProviderType != config.ProviderTypeOllama {
		t.Fatalf("report = %+v", report)
	}
	if !report.Requirements.Tools || !report.Requirements.SystemMessage {
		t.Fatalf("requirements = %+v", report.Requirements)
	}
}

func TestCheckAgentWithAProbeUsesTheEndpoint(t *testing.T) {
	cfg := registryConfig(t, nil)
	impl := &fakeProvider{
		name:         "local",
		providerType: config.ProviderTypeOllama,
		capabilities: providerCapabilities(),
	}
	orchestrator := testOrchestrator(t, cfg, map[string]provider.Provider{
		config.ProviderTypeOllama: impl,
	})

	for _, name := range []string{"inline-reviewer", "toolless"} {
		report, err := orchestrator.CheckAgent(context.Background(), name, true)
		if err != nil {
			t.Fatalf("CheckAgent(%s): %v", name, err)
		}
		if !report.Probed || !report.OK {
			t.Fatalf("report(%s) = %+v", name, report)
		}
		if report.Status != provider.StatusReady {
			t.Fatalf("status(%s) = %q", name, report.Status)
		}
		if report.Capabilities == nil || report.Capabilities.MaxContextTokens == 0 {
			t.Fatalf("capabilities(%s) = %+v", name, report.Capabilities)
		}
	}
}

func TestCheckAgentReportsAnUnavailableProvider(t *testing.T) {
	cfg := registryConfig(t, nil)
	// The registry has no factory for the model's provider type, which is what
	// a build without that provider looks like.
	orchestrator := testOrchestrator(t, cfg, nil)

	report, err := orchestrator.CheckAgent(context.Background(), "inline-reviewer", false)
	if err != nil {
		t.Fatalf("CheckAgent: %v", err)
	}
	if report.OK {
		t.Fatalf("a provider this build does not implement cannot satisfy a task")
	}
	if report.Error == "" {
		t.Fatalf("report = %+v", report)
	}
	if report.Status != provider.StatusUnavailable {
		t.Fatalf("status = %q", report.Status)
	}
}

func TestCheckAgentReportsAnUnreachableProvider(t *testing.T) {
	cfg := registryConfig(t, nil)
	impl := &fakeProvider{
		name:         "local",
		providerType: config.ProviderTypeOllama,
		capabilityfn: func(context.Context, string) (llm.ModelCapabilities, error) {
			return llm.ModelCapabilities{}, apperrors.New(apperrors.KindProvider,
				"provider.ollama", "the server is not reachable")
		},
	}
	orchestrator := testOrchestrator(t, cfg, map[string]provider.Provider{
		config.ProviderTypeOllama: impl,
	})

	report, err := orchestrator.CheckAgent(context.Background(), "inline-reviewer", true)
	if err == nil {
		t.Fatalf("a probe that cannot reach a provider must fail")
	}
	if report.Probed {
		t.Fatalf("a failed probe must not be reported as probed")
	}
	if report.Error == "" {
		t.Fatalf("the report must carry the failure: %+v", report)
	}
	if kind := apperrors.KindOf(err); kind != apperrors.KindProvider {
		t.Fatalf("kind = %s, want %s", kind, apperrors.KindProvider)
	}
}

func TestResolveKeepsTheFailureKindWhenEveryTargetAgrees(t *testing.T) {
	cfg := registryConfig(t, nil)
	impl := &fakeProvider{
		name:         "local",
		providerType: config.ProviderTypeOllama,
		capabilityfn: func(context.Context, string) (llm.ModelCapabilities, error) {
			return llm.ModelCapabilities{}, apperrors.New(apperrors.KindAuthentication,
				"provider.ollama", "the credential was rejected")
		},
	}
	orchestrator := testOrchestrator(t, cfg, map[string]provider.Provider{
		config.ProviderTypeOllama: impl,
	})

	// A single failure keeps its own classification, which is what drives the
	// exit status a host branches on.
	_, err := orchestrator.resolve(context.Background(), "local-model", resolveOptions{})
	if err == nil {
		t.Fatalf("a failing probe must fail the resolution")
	}
	if kind := apperrors.KindOf(err); kind != apperrors.KindAuthentication {
		t.Fatalf("kind = %s, want %s", kind, apperrors.KindAuthentication)
	}

	// Two different failures have no more precise description than "no target
	// could serve the task".
	mixed := []error{
		apperrors.New(apperrors.KindAuthentication, "provider", "denied"),
		apperrors.New(apperrors.KindNotFound, "provider", "missing"),
	}
	if kind := aggregateKind(mixed); kind != apperrors.KindProvider {
		t.Fatalf("kind = %s, want %s", kind, apperrors.KindProvider)
	}
	if kind := aggregateKind(nil); kind != apperrors.KindProvider {
		t.Fatalf("kind = %s, want %s", kind, apperrors.KindProvider)
	}
}

func TestCheckAgentDescribesTheTargetThatWouldServeTheTask(t *testing.T) {
	cfg := registryConfig(t, nil)
	gateway := &fakeProvider{
		name:         "gateway",
		providerType: config.ProviderTypeOpenAICompat,
		capabilities: providerCapabilities(),
	}
	orchestrator := testOrchestrator(t, cfg, map[string]provider.Provider{
		config.ProviderTypeOpenAICompat: gateway,
	})

	report, err := orchestrator.CheckAgent(context.Background(), "mixed-reviewer", false)
	if err != nil {
		t.Fatalf("CheckAgent: %v", err)
	}

	// A broken primary must not make the report claim the agent cannot run
	// when the fallback can serve it.
	if !report.OK {
		t.Fatalf("the fallback satisfies the requirements: %+v", report)
	}
	if report.Provider != "gateway" || report.Model != "fake-model" {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Skipped) != 1 || report.Skipped[0].Target != "cloud/gpt-5" {
		t.Fatalf("skipped = %+v", report.Skipped)
	}
	if !strings.Contains(report.Skipped[0].Reason, "openai-chat") {
		t.Fatalf("reason = %q", report.Skipped[0].Reason)
	}
}

func TestCheckAgentRejectsAnUnknownAgent(t *testing.T) {
	cfg := registryConfig(t, nil)
	orchestrator := testOrchestrator(t, cfg, nil)

	if _, err := orchestrator.CheckAgent(context.Background(), "nope", false); err == nil {
		t.Fatalf("an unknown agent must fail")
	} else if kind := apperrors.KindOf(err); kind != apperrors.KindNotFound {
		t.Fatalf("kind = %s", kind)
	}
}

func TestCheckModelReportsEveryBoundAgent(t *testing.T) {
	cfg := registryConfig(t, nil)
	orchestrator := testOrchestrator(t, cfg, nil)

	reports, err := orchestrator.CheckModel(context.Background(), "local-model", false)
	if err != nil {
		t.Fatalf("CheckModel: %v", err)
	}
	if len(reports) != 4 {
		t.Fatalf("reports = %d, want 4", len(reports))
	}
	for _, report := range reports {
		if report.ModelAlias != "local-model" {
			t.Fatalf("report = %+v", report)
		}
	}

	if _, err := orchestrator.CheckModel(context.Background(), "nope", false); err == nil {
		t.Fatalf("an unknown alias must fail")
	}
}

func TestModelsDescribeTargetsAndFallbacks(t *testing.T) {
	cfg := registryConfig(t, nil)
	orchestrator := testOrchestrator(t, cfg, map[string]provider.Provider{
		config.ProviderTypeOllama: &fakeProvider{
			name:         "local",
			providerType: config.ProviderTypeOllama,
			capabilities: providerCapabilities(),
		},
	})

	entries := orchestrator.Models()
	if len(entries) != len(cfg.Models) {
		t.Fatalf("entries = %d, want %d", len(entries), len(cfg.Models))
	}

	entry, err := orchestrator.Model("with-fallback")
	if err != nil {
		t.Fatalf("Model: %v", err)
	}
	if len(entry.Targets) != 2 {
		t.Fatalf("targets = %+v", entry.Targets)
	}
	if entry.Targets[0].Fallback {
		t.Fatalf("the primary target must not be marked as a fallback")
	}
	if !entry.Targets[1].Fallback {
		t.Fatalf("the second target must be marked as a fallback")
	}
	if entry.Targets[0].Status != provider.StatusUnavailable {
		t.Fatalf("the cloud provider is not implemented: %+v", entry.Targets[0])
	}
	if entry.Targets[1].Status != provider.StatusReady {
		t.Fatalf("the local provider is ready: %+v", entry.Targets[1])
	}
	if !entry.Ready {
		t.Fatalf("an alias with one ready target is ready: %+v", entry)
	}
	if entry.Status != provider.StatusReady {
		t.Fatalf("status = %q", entry.Status)
	}
	if len(entry.Agents) != 1 || entry.Agents[0] != "fallback-reviewer" {
		t.Fatalf("agents = %v", entry.Agents)
	}

	// A disabled provider is reported as disabled, not as missing.
	disabled, err := orchestrator.Model("orphan")
	if err != nil {
		t.Fatalf("Model: %v", err)
	}
	target, _ := disabled.Target()
	if target.Status != provider.StatusUnavailable {
		t.Fatalf("target = %+v", target)
	}
	if target.Error == "" || disabled.Ready {
		t.Fatalf("entry = %+v", disabled)
	}

	if _, err := orchestrator.Model("nope"); err == nil {
		t.Fatalf("an unknown alias must fail")
	} else if kind := apperrors.KindOf(err); kind != apperrors.KindNotFound {
		t.Fatalf("kind = %s", kind)
	}
}

func TestModelReportsADisabledProvider(t *testing.T) {
	cfg := registryConfig(t, func(cfg *config.Config) {
		cfg.Models["off"] = &config.Model{Primary: &config.ModelRef{Provider: "gone", Model: "qwen3"}}
	})
	orchestrator := testOrchestrator(t, cfg, map[string]provider.Provider{
		config.ProviderTypeOllama: &fakeProvider{
			name:         "local",
			providerType: config.ProviderTypeOllama,
			capabilities: providerCapabilities(),
		},
	})

	entry, err := orchestrator.Model("off")
	if err != nil {
		t.Fatalf("Model: %v", err)
	}
	target, _ := entry.Target()
	if target.Status != provider.StatusDisabled {
		t.Fatalf("status = %q", target.Status)
	}
	if target.Error != "the provider is disabled in the configuration" {
		t.Fatalf("error = %q", target.Error)
	}
}

func TestProbeModelRecordsPerTargetResults(t *testing.T) {
	cfg := registryConfig(t, nil)
	impl := &fakeProvider{
		name:         "local",
		providerType: config.ProviderTypeOllama,
		capabilities: providerCapabilities(),
	}
	orchestrator := testOrchestrator(t, cfg, map[string]provider.Provider{
		config.ProviderTypeOllama: impl,
	})

	entry, err := orchestrator.Model("with-fallback")
	if err != nil {
		t.Fatalf("Model: %v", err)
	}
	probed := orchestrator.ProbeModel(context.Background(), entry)

	// The cloud target has no implementation, so it is left untouched.
	if probed.Targets[0].Probed {
		t.Fatalf("an unavailable target must not be probed: %+v", probed.Targets[0])
	}
	if probed.Targets[1].Error != "" {
		t.Fatalf("the local target is reachable: %+v", probed.Targets[1])
	}
	if !probed.Targets[1].Probed {
		t.Fatalf("the local target must be probed: %+v", probed.Targets[1])
	}
	if probed.Targets[1].Capabilities == nil || !probed.Targets[1].Capabilities.ToolCalling {
		t.Fatalf("capabilities = %+v", probed.Targets[1].Capabilities)
	}
}

func TestProbeModelRecordsAProbeFailure(t *testing.T) {
	cfg := registryConfig(t, nil)
	impl := &fakeProvider{
		name:         "local",
		providerType: config.ProviderTypeOllama,
		capabilityfn: func(context.Context, string) (llm.ModelCapabilities, error) {
			return llm.ModelCapabilities{}, apperrors.New(apperrors.KindProvider,
				"provider.ollama", "the server is not reachable")
		},
	}
	orchestrator := testOrchestrator(t, cfg, map[string]provider.Provider{
		config.ProviderTypeOllama: impl,
	})

	entry, err := orchestrator.Model("local-model")
	if err != nil {
		t.Fatalf("Model: %v", err)
	}
	probed := orchestrator.ProbeModel(context.Background(), entry)

	target, _ := probed.Target()
	if target.Probed {
		t.Fatalf("a failed probe must not be reported as probed")
	}
	if target.Error == "" {
		t.Fatalf("the failure must be reported: %+v", target)
	}
	if target.Capabilities == nil {
		t.Fatalf("the static capabilities must survive a failed probe")
	}
}

func TestProbeModelDoesNotMutateTheCaller(t *testing.T) {
	cfg := registryConfig(t, nil)
	impl := &fakeProvider{
		name:         "local",
		providerType: config.ProviderTypeOllama,
		capabilities: providerCapabilities(),
	}
	orchestrator := testOrchestrator(t, cfg, map[string]provider.Provider{
		config.ProviderTypeOllama: impl,
	})

	entry, err := orchestrator.Model("local-model")
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	probed := orchestrator.ProbeModel(context.Background(), entry)

	if !probed.Targets[0].Probed {
		t.Fatalf("the probe did not run: %+v", probed.Targets[0])
	}
	// The entry the caller already holds must be untouched: a probe that
	// silently rewrites it is a hidden side effect.
	if entry.Targets[0].Probed {
		t.Fatalf("the caller's entry was mutated: %+v", entry.Targets[0])
	}
	if entry.Targets[0].Capabilities == nil || entry.Targets[0].Capabilities.ToolCalling {
		t.Fatalf("the caller's static capabilities were overwritten: %+v", entry.Targets[0].Capabilities)
	}
}

func TestRunReportsTheTargetItUsedAndTheOnesItSkipped(t *testing.T) {
	cfg := registryConfig(t, nil)
	gateway := &fakeProvider{
		name:         "gateway",
		providerType: config.ProviderTypeOpenAICompat,
		capabilities: providerCapabilities(),
		scripts:      [][]llm.Event{answerEvents("answer")},
	}
	orchestrator := testOrchestrator(t, cfg, map[string]provider.Provider{
		config.ProviderTypeOpenAICompat: gateway,
	})

	result, err := orchestrator.Run(context.Background(),
		Request{Agent: "mixed-reviewer", Task: "Review"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Provider != "gateway" || result.Model != "fake-model" {
		t.Fatalf("identity = %+v", result)
	}
	if len(result.Skipped) != 1 {
		t.Fatalf("skipped = %v", result.Skipped)
	}
	// The reason has to travel with the report: a host that receives an answer
	// from a fallback must be able to see that the primary was passed over.
	if !strings.Contains(result.Skipped[0], "cloud/gpt-5") ||
		!strings.Contains(result.Skipped[0], "openai-chat") {
		t.Fatalf("skipped = %v", result.Skipped)
	}
	if !strings.Contains(result.RenderText(), "skipped:  cloud/gpt-5") {
		t.Fatalf("the text report hides the fallback:\n%s", result.RenderText())
	}
}

func TestRunRefusesANonExecutableAgentType(t *testing.T) {
	cfg := registryConfig(t, nil)
	impl := &fakeProvider{
		name:         "local",
		providerType: config.ProviderTypeOllama,
		capabilities: providerCapabilities(),
		scripts:      [][]llm.Event{answerEvents("answer")},
	}
	orchestrator := testOrchestrator(t, cfg, map[string]provider.Provider{
		config.ProviderTypeOllama: impl,
	})

	_, err := orchestrator.Run(context.Background(), Request{Agent: "team", Task: "Review"})
	if err == nil {
		t.Fatalf("a team agent must be refused")
	}
	if kind := apperrors.KindOf(err); kind != apperrors.KindCapability {
		t.Fatalf("kind = %s, want %s", kind, apperrors.KindCapability)
	}
	if len(impl.requests) != 0 {
		t.Fatalf("no request may be sent for a refused agent")
	}
}

func TestRunRefusesAnUnknownToolBeforeAnyRequest(t *testing.T) {
	cfg := registryConfig(t, nil)
	impl := &fakeProvider{
		name:         "local",
		providerType: config.ProviderTypeOllama,
		capabilities: providerCapabilities(),
		scripts:      [][]llm.Event{answerEvents("answer")},
	}
	orchestrator := testOrchestrator(t, cfg, map[string]provider.Provider{
		config.ProviderTypeOllama: impl,
	})

	_, err := orchestrator.Run(context.Background(), Request{Agent: "broken", Task: "Review"})
	if err == nil {
		t.Fatalf("an agent granting an unknown tool must be refused")
	}
	if kind := apperrors.KindOf(err); kind != apperrors.KindCapability {
		t.Fatalf("kind = %s, want %s", kind, apperrors.KindCapability)
	}
	if len(impl.requests) != 0 {
		t.Fatalf("no request may be sent for an invalid grant")
	}
}
