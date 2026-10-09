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

package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/llm"
	"github.com/pawagents/pawagents/internal/orchestrator"
	"github.com/pawagents/pawagents/internal/provider"
)

// fakeProvider satisfies provider.Provider, plus the two optional capabilities
// the checks use, without a network.
type fakeProvider struct {
	name     string
	typ      string
	health   provider.Health
	healthFn func() (provider.Health, error)
	models   []string
	err      error
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) Type() string { return f.typ }

func (f *fakeProvider) Capabilities(context.Context, string) (llm.ModelCapabilities, error) {
	return llm.ModelCapabilities{}, f.err
}

func (f *fakeProvider) Generate(context.Context, *llm.GenerateRequest) (llm.Stream, error) {
	return nil, errors.New("not implemented in the fake")
}

func (f *fakeProvider) Health(context.Context) (provider.Health, error) {
	if f.healthFn != nil {
		return f.healthFn()
	}
	return f.health, f.err
}

func (f *fakeProvider) ListModels(context.Context) ([]string, error) { return f.models, f.err }

// fakeEndpoints resolves provider names to fake providers.
type fakeEndpoints struct {
	infos     []provider.Info
	providers map[string]*fakeProvider
	supported map[string]bool
}

func (f *fakeEndpoints) Infos() []provider.Info { return f.infos }

func (f *fakeEndpoints) Provider(_ context.Context, name string) (provider.Provider, error) {
	instance, ok := f.providers[name]
	if !ok {
		return nil, apperrors.New(apperrors.KindNotFound, "registry.provider",
			"provider %q is not configured", name)
	}
	return instance, nil
}

func (f *fakeEndpoints) Supports(providerType string) bool { return f.supported[providerType] }

// fakeRuntime answers the agent and model questions.
type fakeRuntime struct {
	profiles []orchestrator.AgentProfile
	models   []orchestrator.ModelEntry
	reports  map[string]orchestrator.CapabilityReport
	err      error
}

func (f *fakeRuntime) Profiles() []orchestrator.AgentProfile { return f.profiles }

func (f *fakeRuntime) Models() []orchestrator.ModelEntry { return f.models }

func (f *fakeRuntime) CheckAgent(_ context.Context, name string, _ bool) (orchestrator.CapabilityReport, error) {
	if f.err != nil {
		return orchestrator.CapabilityReport{}, f.err
	}
	return f.reports[name], nil
}

// profile builds an agent profile for the checks.
func profile(name, alias string, targets ...config.ModelRef) orchestrator.AgentProfile {
	return orchestrator.AgentProfile{
		Name:       name,
		ModelAlias: alias,
		Type:       orchestrator.TypeSingle,
		Executable: true,
		Targets:    targets,
	}
}

// baseConfig is a configuration with one reachable local model server.
func baseConfig() *config.Config {
	return &config.Config{
		Version: 1,
		Providers: map[string]*config.Provider{
			"local-ollama": {Type: config.ProviderTypeOllama, BaseURL: "http://127.0.0.1:11434"},
			"cloud":        {Type: config.ProviderTypeAnthropic, APIKeyEnv: "PAWAGENTS_DOCTOR_TEST_KEY"},
		},
	}
}

// check finds one check by its label, so that a test can assert on it without
// depending on the order of a section.
func check(t *testing.T, report *Report, label string) Check {
	t.Helper()

	for _, section := range report.Sections {
		for _, candidate := range section.Checks {
			if candidate.Label == label {
				return candidate
			}
		}
	}
	t.Fatalf("no check labelled %q in the report:\n%s", label, report.RenderText())
	return Check{}
}

// sectionNames lists the sections of a report in order.
func sectionNames(report *Report) []string {
	names := make([]string, 0, len(report.Sections))
	for _, section := range report.Sections {
		names = append(names, section.Name)
	}
	return names
}

func TestRunReportsAnUnloadableConfiguration(t *testing.T) {
	report := Run(context.Background(), Options{
		ConfigPath:  "/tmp/nope/config.yaml",
		ConfigError: apperrors.New(apperrors.KindConfig, "config.load", "file not found"),
	})

	if len(report.Sections) != 1 {
		t.Fatalf("sections = %v, want only the configuration", sectionNames(report))
	}
	if got := check(t, report, "configuration"); got.Status != StatusFail {
		t.Fatalf("status = %q, want a failure", got.Status)
	}
	if !strings.Contains(report.RenderText(), "file not found") {
		t.Fatalf("the report does not explain the failure:\n%s", report.RenderText())
	}
	if kind := report.ExitKind(); kind != apperrors.KindConfig {
		t.Fatalf("exit kind = %s, want %s", kind, apperrors.KindConfig)
	}
}

func TestRunReportsTheSectionsInOrder(t *testing.T) {
	report := Run(context.Background(), Options{
		Config:     baseConfig(),
		ConfigPath: "/tmp/config.yaml",
		Runtime: &fakeRuntime{
			profiles: []orchestrator.AgentProfile{profile("reviewer", "local-coder")},
			reports: map[string]orchestrator.CapabilityReport{
				"reviewer": {OK: true, Provider: "local-ollama", Model: "qwen3", Capabilities: &llm.ModelCapabilities{}},
			},
		},
		Endpoints: &fakeEndpoints{
			infos:     []provider.Info{{Name: "local-ollama", Type: config.ProviderTypeOllama}},
			providers: map[string]*fakeProvider{"local-ollama": {name: "local-ollama", typ: config.ProviderTypeOllama, health: provider.Health{Reachable: true, Detail: "Ollama 0.40.1"}}},
			supported: map[string]bool{config.ProviderTypeOllama: true, config.ProviderTypeAnthropic: true},
		},
	})

	want := []string{"Config", "Providers", "Ollama", "Agents", "Workspace", "MCP"}
	if got := sectionNames(report); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("sections = %v, want %v", got, want)
	}
	if report.Summary.OK == 0 || report.Failed() {
		t.Fatalf("summary = %+v", report.Summary)
	}
}

func TestProvidersWithoutACredential(t *testing.T) {
	t.Setenv("PAWAGENTS_DOCTOR_TEST_KEY", "")

	cases := []struct {
		name     string
		used     bool
		want     Status
		wantKind apperrors.Kind
	}{
		{"used by an alias", true, StatusFail, apperrors.KindAuthentication},
		{"unused by any alias", false, StatusWarn, ""},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var models []orchestrator.ModelEntry
			if testCase.used {
				models = []orchestrator.ModelEntry{{
					Alias:   "strong-coder",
					Targets: []orchestrator.ModelTarget{{Provider: "cloud", Model: "claude-sonnet"}},
				}}
			}

			report := Run(context.Background(), Options{
				Config:    baseConfig(),
				Runtime:   &fakeRuntime{models: models},
				Endpoints: &fakeEndpoints{supported: map[string]bool{config.ProviderTypeAnthropic: true}},
			})

			got := check(t, report, "cloud (anthropic)")
			if got.Status != testCase.want {
				t.Fatalf("status = %q, want %q (%s)", got.Status, testCase.want, got.Detail)
			}
			if !strings.Contains(got.Detail, "PAWAGENTS_DOCTOR_TEST_KEY") {
				t.Fatalf("detail = %q, want the variable name", got.Detail)
			}
			if testCase.wantKind != "" && got.Kind != testCase.wantKind {
				t.Fatalf("kind = %s, want %s", got.Kind, testCase.wantKind)
			}
		})
	}
}

func TestProviderThatTheBuildDoesNotImplement(t *testing.T) {
	cfg := baseConfig()
	cfg.Providers["gateway"] = &config.Provider{Type: "gemini"}

	used := []orchestrator.ModelEntry{{
		Alias:   "gemini-alias",
		Targets: []orchestrator.ModelTarget{{Provider: "gateway", Model: "gemini-2.5-pro"}},
	}}
	endpoints := &fakeEndpoints{supported: map[string]bool{config.ProviderTypeOllama: true}}

	report := Run(context.Background(), Options{
		Config:    cfg,
		Runtime:   &fakeRuntime{models: used},
		Endpoints: endpoints,
	})
	if got := check(t, report, "gateway (gemini)"); got.Status != StatusFail || got.Kind != apperrors.KindCapability {
		t.Fatalf("a provider in use must fail: %+v", got)
	}

	unused := Run(context.Background(), Options{Config: cfg, Runtime: &fakeRuntime{}, Endpoints: endpoints})
	if got := check(t, unused, "gateway (gemini)"); got.Status != StatusWarn {
		t.Fatalf("an unused provider must only warn: %+v", got)
	}
}

func TestDisabledProviderOnlyWarns(t *testing.T) {
	cfg := baseConfig()
	cfg.Providers["local-ollama"].Disabled = true

	report := Run(context.Background(), Options{Config: cfg, Runtime: &fakeRuntime{}})
	if got := check(t, report, "local-ollama (ollama)"); got.Status != StatusWarn {
		t.Fatalf("status = %q, want a warning", got.Status)
	}
}

func TestOllamaModelsAndEndpoint(t *testing.T) {
	models := []orchestrator.ModelEntry{{
		Alias:   "local-coder",
		Targets: []orchestrator.ModelTarget{{Provider: "local-ollama", Model: "qwen3-coder"}},
	}}

	cases := []struct {
		name       string
		installed  []string
		health     provider.Health
		healthErr  error
		wantStatus Status
		wantLabel  string
	}{
		{
			name:       "installed",
			installed:  []string{"qwen3-coder:latest"},
			health:     provider.Health{Reachable: true, Detail: "Ollama 0.40.1"},
			wantStatus: StatusOK,
			wantLabel:  "qwen3-coder",
		},
		{
			name:       "not installed",
			installed:  []string{"llama3:8b"},
			health:     provider.Health{Reachable: true},
			wantStatus: StatusFail,
			wantLabel:  "qwen3-coder",
		},
		{
			name:       "endpoint down",
			installed:  nil,
			healthErr:  errors.New("connection refused"),
			wantStatus: StatusFail,
			wantLabel:  "local-ollama endpoint",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			report := Run(context.Background(), Options{
				Config:  baseConfig(),
				Runtime: &fakeRuntime{models: models},
				Endpoints: &fakeEndpoints{
					infos: []provider.Info{{Name: "local-ollama", Type: config.ProviderTypeOllama}},
					providers: map[string]*fakeProvider{"local-ollama": {
						name: "local-ollama", typ: config.ProviderTypeOllama,
						health: testCase.health, healthFn: nil, models: testCase.installed,
						err: testCase.healthErr,
					}},
					supported: map[string]bool{config.ProviderTypeOllama: true},
				},
			})

			got := check(t, report, testCase.wantLabel)
			// A health failure is reported by the Ollama section too, with the
			// same outcome, so the status is what matters here.
			if got.Status != testCase.wantStatus {
				t.Fatalf("%s: status = %q, want %q (%s)",
					testCase.wantLabel, got.Status, testCase.wantStatus, got.Detail)
			}
		})
	}
}

func TestAgents(t *testing.T) {
	cases := []struct {
		name       string
		profile    orchestrator.AgentProfile
		report     orchestrator.CapabilityReport
		wantStatus Status
		wantKind   apperrors.Kind
		wantDetail string
	}{
		{
			name:       "ready and confirmed",
			profile:    profile("reviewer", "local-coder", config.ModelRef{Provider: "local-ollama", Model: "qwen3-coder"}),
			report:     orchestrator.CapabilityReport{OK: true, Provider: "local-ollama", Model: "qwen3-coder", ProviderType: config.ProviderTypeAnthropic, Capabilities: &llm.ModelCapabilities{}},
			wantStatus: StatusOK,
			wantDetail: "local-ollama/qwen3-coder",
		},
		{
			name:    "ready but not confirmed",
			profile: profile("reviewer", "local-coder", config.ModelRef{Provider: "local-ollama", Model: "qwen3-coder"}),
			report: orchestrator.CapabilityReport{
				OK: true, Provider: "local-ollama", Model: "qwen3-coder",
				ProviderType: config.ProviderTypeOllama, Capabilities: &llm.ModelCapabilities{},
			},
			wantStatus: StatusOK,
			wantDetail: "confirmed by the endpoint",
		},
		{
			name:    "unprobed local model with a static refusal",
			profile: profile("reviewer", "local-coder", config.ModelRef{Provider: "local-ollama", Model: "qwen3-coder"}),
			report: orchestrator.CapabilityReport{
				Missing: []string{"tool_calling"}, Error: "does not support tool_calling",
				Provider: "local-ollama", Model: "qwen3-coder",
				ProviderType: config.ProviderTypeOllama, Capabilities: &llm.ModelCapabilities{},
			},
			wantStatus: StatusWarn,
			wantDetail: "the endpoint decides",
		},
		{
			name:    "probed refusal",
			profile: profile("reviewer", "local-coder", config.ModelRef{Provider: "local-ollama", Model: "qwen3-coder"}),
			report: orchestrator.CapabilityReport{
				Probed: true, Missing: []string{"tool_calling"}, Provider: "local-ollama",
				Model: "qwen3-coder", ProviderType: config.ProviderTypeOllama,
				Capabilities: &llm.ModelCapabilities{},
			},
			wantStatus: StatusFail,
			wantKind:   apperrors.KindCapability,
			wantDetail: "does not provide tool_calling",
		},
		{
			name:       "alias that does not resolve",
			profile:    profile("reviewer", "nope"),
			report:     orchestrator.CapabilityReport{Error: "model alias \"nope\" is not defined in the configuration"},
			wantStatus: StatusFail,
			wantKind:   apperrors.KindConfig,
			wantDetail: "is not defined",
		},
		{
			name:       "not executable in this release",
			profile:    orchestrator.AgentProfile{Name: "team", ModelAlias: "local-coder", Type: orchestrator.TypeTeam},
			wantStatus: StatusWarn,
			wantKind:   apperrors.KindCapability,
			wantDetail: "only single agents",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			report := Run(context.Background(), Options{
				Config: baseConfig(),
				Runtime: &fakeRuntime{
					profiles: []orchestrator.AgentProfile{testCase.profile},
					reports:  map[string]orchestrator.CapabilityReport{testCase.profile.Name: testCase.report},
				},
				Endpoints: &fakeEndpoints{supported: map[string]bool{config.ProviderTypeOllama: true}},
			})

			got := check(t, report, testCase.profile.Name+" ("+testCase.profile.ModelAlias+")")
			if got.Status != testCase.wantStatus {
				t.Fatalf("status = %q, want %q (%s)", got.Status, testCase.wantStatus, got.Detail)
			}
			if !strings.Contains(got.Detail, testCase.wantDetail) {
				t.Fatalf("detail = %q, want it to contain %q", got.Detail, testCase.wantDetail)
			}
			if testCase.wantKind != "" && got.Kind != testCase.wantKind {
				t.Fatalf("kind = %s, want %s", got.Kind, testCase.wantKind)
			}
		})
	}
}

func TestAgentWithAnAliasThatNeedsACredential(t *testing.T) {
	t.Setenv("PAWAGENTS_DOCTOR_TEST_KEY", "")

	report := Run(context.Background(), Options{
		Config: baseConfig(),
		Runtime: &fakeRuntime{
			profiles: []orchestrator.AgentProfile{profile("cloud-reviewer", "strong-coder",
				config.ModelRef{Provider: "cloud", Model: "claude-sonnet"})},
			reports: map[string]orchestrator.CapabilityReport{
				"cloud-reviewer": {OK: true, Provider: "cloud", Model: "claude-sonnet", ProviderType: config.ProviderTypeAnthropic, Capabilities: &llm.ModelCapabilities{}},
			},
		},
		Endpoints: &fakeEndpoints{supported: map[string]bool{config.ProviderTypeAnthropic: true}},
	})

	got := check(t, report, "cloud-reviewer (strong-coder)")
	if got.Status != StatusFail || got.Kind != apperrors.KindAuthentication {
		t.Fatalf("a missing credential must fail the agent: %+v", got)
	}
	if !strings.Contains(got.Detail, "PAWAGENTS_DOCTOR_TEST_KEY") {
		t.Fatalf("detail = %q, want the variable to set", got.Detail)
	}
}

func TestWorkspace(t *testing.T) {
	repository := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatalf("cannot create the fixture: %v", err)
	}
	plain := t.TempDir()

	cases := []struct {
		name       string
		directory  string
		wantStatus Status
		wantLabel  string
	}{
		{"repository", repository, StatusOK, "Git repository"},
		{"plain directory", plain, StatusWarn, "Git repository"},
		{"missing directory", filepath.Join(plain, "nope"), StatusFail, filepath.Join(plain, "nope")},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			report := Run(context.Background(), Options{
				Config:    &config.Config{},
				Workspace: testCase.directory,
			})

			if testCase.wantStatus == StatusFail {
				if kind := report.ExitKind(); kind != apperrors.KindWorkspace {
					t.Fatalf("exit kind = %s, want %s", kind, apperrors.KindWorkspace)
				}
				return
			}
			if got := check(t, report, testCase.wantLabel); got.Status != testCase.wantStatus {
				t.Fatalf("status = %q, want %q (%s)", got.Status, testCase.wantStatus, got.Detail)
			}
		})
	}
}

func TestMCPReportsTheToolsAndTheRegistrationCommands(t *testing.T) {
	report := Run(context.Background(), Options{
		Config:  baseConfig(),
		Runtime: &fakeRuntime{profiles: []orchestrator.AgentProfile{profile("reviewer", "local-coder")}},
	})

	transport := check(t, report, "stdio transport available")
	if transport.Status != StatusOK {
		t.Fatalf("status = %q", transport.Status)
	}
	for _, tool := range []string{"list_agents", "get_agent", "delegate_task"} {
		if !strings.Contains(transport.Detail, tool) {
			t.Fatalf("detail = %q, want %q", transport.Detail, tool)
		}
	}
	if registration := check(t, report, "registration"); !strings.Contains(registration.Detail, "codex mcp add") {
		t.Fatalf("the report does not tell a host how to connect: %+v", registration)
	}

	// A server with no agent would connect and have nothing to delegate to.
	empty := Run(context.Background(), Options{Config: baseConfig(), Runtime: &fakeRuntime{}})
	if got := check(t, empty, "agents"); got.Status != StatusWarn {
		t.Fatalf("status = %q, want a warning", got.Status)
	}
}

func TestExitKindIsTheFirstFailure(t *testing.T) {
	report := &Report{Sections: []Section{
		{Name: "A", Checks: []Check{pass("a", "")}},
		{Name: "B", Checks: []Check{warn("b", "", "")}},
		{Name: "C", Checks: []Check{fail("c", "", apperrors.KindWorkspace)}},
		{Name: "D", Checks: []Check{fail("d", "", apperrors.KindAuthentication)}},
	}}
	report.Summarize()

	if kind := report.ExitKind(); kind != apperrors.KindWorkspace {
		t.Fatalf("exit kind = %s, want the first failure", kind)
	}
	if !report.Failed() {
		t.Fatal("the report claims to have no failures")
	}
	if got := report.Summary; got.Fail != 2 || got.Warn != 1 || got.OK != 1 {
		t.Fatalf("summary = %+v", got)
	}
}

func TestRenderTextUsesTheDocumentedSymbols(t *testing.T) {
	report := &Report{Sections: []Section{{
		Name: "Config",
		Checks: []Check{
			pass("/tmp/config.yaml", "loaded"),
			warn("warning", "", ""),
			fail("problem", "", apperrors.KindConfig),
		},
	}}}
	report.Summarize()

	rendered := report.RenderText()
	for _, want := range []string{"PawAgents diagnostics", "Config", symbolOK, symbolWarn, symbolFail, "1 failed"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("the report does not contain %q:\n%s", want, rendered)
		}
	}

	if text := (&Report{}).RenderText(); !strings.Contains(text, "all 0 checks passed") {
		t.Fatalf("an empty report reads oddly:\n%s", text)
	}
}

func TestReportJSONRoundTrip(t *testing.T) {
	report := Run(context.Background(), Options{
		Config: baseConfig(),
		Runtime: &fakeRuntime{
			profiles: []orchestrator.AgentProfile{profile("reviewer", "local-coder")},
			reports: map[string]orchestrator.CapabilityReport{
				"reviewer": {OK: true, Provider: "local-ollama", Model: "qwen3", Capabilities: &llm.ModelCapabilities{}},
			},
		},
	})

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("cannot encode the report: %v", err)
	}

	var decoded Report
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("cannot decode the report: %v\n%s", err, encoded)
	}
	if len(decoded.Sections) != len(report.Sections) {
		t.Fatalf("sections = %d, want %d", len(decoded.Sections), len(report.Sections))
	}
	if decoded.Summary != report.Summary {
		t.Fatalf("summary = %+v, want %+v", decoded.Summary, report.Summary)
	}
	if decoded.Sections[0].Checks[0].Status != StatusOK {
		t.Fatalf("the first check did not survive the round trip: %+v", decoded.Sections[0].Checks[0])
	}
}

func TestLocalAndLoopbackDetection(t *testing.T) {
	cases := map[string]bool{
		"":                                   false,
		"http://127.0.0.1:11434":             true,
		"http://localhost:8080/v1":           true,
		"http://[::1]:11434":                 true,
		"https://api.anthropic.com":          false,
		"https://ws.example.aliyuncs.com/v1": false,
		"not a url":                          false,
	}

	for baseURL, want := range cases {
		if got := isLoopback(baseURL); got != want {
			t.Fatalf("isLoopback(%q) = %v, want %v", baseURL, got, want)
		}
	}

	// Ollama is a local model server whatever its address says.
	if !isLocalServer(&config.Provider{Type: config.ProviderTypeOllama}) {
		t.Fatal("an Ollama provider is not treated as local")
	}
	if isLocalServer(&config.Provider{Type: config.ProviderTypeAnthropic, BaseURL: "https://api.anthropic.com"}) {
		t.Fatal("a hosted provider is treated as local")
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0:               "unbounded",
		512:             "512 bytes",
		2048:            "2 KiB",
		4 * 1024 * 1024: "4 MiB",
	}
	for size, want := range cases {
		if got := humanBytes(size); got != want {
			t.Fatalf("humanBytes(%d) = %q, want %q", size, got, want)
		}
	}
}
