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

package config

import (
	"strings"
	"testing"
	"time"
)

// baseConfig is a minimal configuration that validates without errors and
// without warnings. Every negative test starts from it and introduces exactly
// one problem, so an unexpected warning cannot be mistaken for the target one.
const baseConfig = `
version: 1
providers:
  local:
    type: ollama
    base_url: http://127.0.0.1:11434
models:
  coder:
    provider: local
    model: qwen3-coder
agents:
  reviewer:
    model: coder
    instructions: You review code.
    tools:
      - repo.read
`

// expectProblem asserts that the validation report contains a message with the
// given substring, either as an error or as a warning.
func expectProblem(t *testing.T, result *ValidationResult, want string, wantError bool) {
	t.Helper()
	if wantError {
		for _, err := range result.Errors {
			if strings.Contains(err.Error(), want) {
				return
			}
		}
		t.Fatalf("no error containing %q in:\n%s", want, result.Report())
	}
	for _, warning := range result.Warnings {
		if strings.Contains(warning, want) {
			return
		}
	}
	t.Fatalf("no warning containing %q in:\n%s", want, result.Report())
}

func TestValidateBaselineIsClean(t *testing.T) {
	cfg := prepare(t, baseConfig)
	cfg.Path = "test.yaml"

	result := Validate(cfg)
	if !result.IsClean() {
		t.Fatalf("baseline configuration should be clean, got:\n%s", result.Report())
	}
	if result.HasErrors() {
		t.Fatal("HasErrors() should be false")
	}
	if result.Err() != nil {
		t.Fatalf("Err() = %v, want nil", result.Err())
	}
	if result.Summary() != "no problems found" {
		t.Fatalf("Summary() = %q", result.Summary())
	}
	if !strings.Contains(result.Report(), "no problems found") {
		t.Fatalf("Report() = %q", result.Report())
	}
}

func TestValidateNilConfig(t *testing.T) {
	result := Validate(nil)
	if !result.HasErrors() {
		t.Fatal("a nil configuration must be reported as an error")
	}
	if result.Err() == nil {
		t.Fatal("Err() must be non-nil")
	}
}

func TestValidateErrors(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "unsupported version",
			yaml: strings.Replace(baseConfig, "version: 1", "version: 99", 1),
			want: "unsupported configuration version 99",
		},
		{
			name: "unknown provider type",
			yaml: strings.Replace(baseConfig, "type: ollama", "type: made-up", 1),
			want: `unsupported provider type "made-up"`,
		},
		{
			name: "compatible provider without base_url",
			yaml: `
version: 1
providers:
  gateway:
    type: openai-compatible
`,
			want: "base_url is required",
		},
		{
			name: "invalid base_url scheme",
			yaml: `
version: 1
providers:
  gateway:
    type: openai-compatible
    base_url: ftp://llm.example.com/v1
`,
			want: "must use the http or https scheme",
		},
		{
			name: "model references a missing provider",
			yaml: `
version: 1
providers:
  local:
    type: ollama
models:
  coder:
    provider: ghost
    model: qwen3-coder
`,
			want: `provider "ghost" is not defined`,
		},
		{
			name: "model without a target",
			yaml: `
version: 1
models:
  coder:
    temperature: 0.5
`,
			want: "no provider target",
		},
		{
			name: "model without a model name",
			yaml: `
version: 1
providers:
  local:
    type: ollama
models:
  coder:
    provider: local
`,
			want: "models.coder.primary: model is required",
		},
		{
			name: "agent without a model",
			yaml: `
version: 1
agents:
  reviewer:
    instructions: review
`,
			want: "agents.reviewer: model is required",
		},
		{
			name: "agent references an unknown model",
			yaml: `
version: 1
agents:
  reviewer:
    model: ghost
`,
			want: `model "ghost" is not defined`,
		},
		{
			name: "agent asks for shell access",
			yaml: strings.Replace(baseConfig, "    model: coder", "    model: coder\n    permissions:\n      shell: allow", 1),
			want: "never executes shell commands",
		},
		{
			name: "agent asks for write access",
			yaml: strings.Replace(baseConfig, "    model: coder", "    model: coder\n    permissions:\n      filesystem: write", 1),
			want: "read-only by default",
		},
		{
			name: "invalid tool name",
			yaml: strings.Replace(baseConfig, "- repo.read", "- reporead", 1),
			want: "is not a valid tool name",
		},
		{
			name: "prompt and instructions together",
			yaml: strings.Replace(baseConfig, "    instructions: You review code.",
				"    instructions: You review code.\n    prompt: /tmp/prompt.md", 1),
			want: "mutually exclusive",
		},
		{
			name: "invalid output mode",
			yaml: strings.Replace(baseConfig, "    model: coder", "    model: coder\n    output_mode: xml", 1),
			want: `output_mode "xml" is invalid`,
		},
		{
			name: "invalid log level",
			yaml: baseConfig + "telemetry:\n  log_level: loud\n",
			want: `telemetry.log_level "loud" is invalid`,
		},
		{
			name: "invalid log format",
			yaml: baseConfig + "telemetry:\n  log_format: xml\n",
			want: `telemetry.log_format "xml" is invalid`,
		},
		{
			name: "negative max file size",
			yaml: baseConfig + "security:\n  max_file_size: -1\n",
			want: "security.max_file_size must not be negative",
		},
		{
			name: "negative retention",
			yaml: baseConfig + "sessions:\n  retention: -5\n",
			want: "sessions.retention must not be negative",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := prepare(t, test.yaml)
			result := Validate(cfg)
			if !result.HasErrors() {
				t.Fatalf("expected an error containing %q, report:\n%s", test.want, result.Report())
			}
			expectProblem(t, result, test.want, true)
		})
	}
}

func TestValidateWarnings(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "missing API key environment variable",
			yaml: `
version: 1
providers:
  cloud:
    type: anthropic
    api_key_env: PAWAGENTS_TEST_MISSING_KEY
models:
  m:
    provider: cloud
    model: claude-sonnet
agents:
  a:
    model: m
    instructions: hi
    tools: [repo.read]
`,
			want: "environment variable PAWAGENTS_TEST_MISSING_KEY is not set",
		},
		{
			name: "provider without credentials",
			yaml: `
version: 1
providers:
  cloud:
    type: anthropic
models:
  m:
    provider: cloud
    model: claude-sonnet
agents:
  a:
    model: m
    instructions: hi
    tools: [repo.read]
`,
			want: "no credentials configured",
		},
		{
			name: "roadmap provider type",
			yaml: `
version: 1
providers:
  gem:
    type: gemini
models:
  m:
    provider: gem
    model: gemini-pro
agents:
  a:
    model: m
    instructions: hi
    tools: [repo.read]
`,
			want: "not implemented yet",
		},
		{
			name: "provider fallback",
			yaml: `
version: 1
providers:
  local:
    type: ollama
models:
  coder:
    primary:
      provider: local
      model: qwen3-coder
    fallback:
      - provider: local
        model: llama3
agents:
  a:
    model: coder
    instructions: hi
    tools: [repo.read]
`,
			want: "provider fallback is not implemented yet",
		},
		{
			name: "plaintext http to a remote host",
			yaml: `
version: 1
providers:
  gateway:
    type: openai-compatible
    base_url: http://llm.example.com/v1
`,
			want: "plaintext http",
		},
		{
			name: "tls verification disabled",
			yaml: `
version: 1
providers:
  local:
    type: ollama
    tls:
      insecure_skip_verify: true
`,
			want: "disables certificate validation",
		},
		{
			name: "unknown tool name",
			yaml: strings.Replace(baseConfig, "- repo.read", "- shell.exec", 1),
			want: `tool "shell.exec" is not part of the built-in tool runtime`,
		},
		{
			name: "duplicate tools",
			yaml: strings.Replace(baseConfig, "- repo.read", "- repo.read\n      - repo.read", 1),
			want: "contains duplicates",
		},
		{
			name: "no tools granted",
			yaml: `
version: 1
providers:
  local:
    type: ollama
models:
  coder:
    provider: local
    model: qwen3-coder
agents:
  reviewer:
    model: coder
    instructions: hi
`,
			want: "no tools granted",
		},
		{
			name: "missing prompt file",
			yaml: strings.Replace(baseConfig, "    instructions: You review code.",
				"    prompt: /nonexistent/pawagents/prompt.md", 1),
			want: "does not exist",
		},
		{
			name: "no prompt at all",
			yaml: `
version: 1
providers:
  local:
    type: ollama
models:
  coder:
    provider: local
    model: qwen3-coder
agents:
  reviewer:
    model: coder
    tools: [repo.read]
`,
			want: "no prompt configured",
		},
		{
			name: "workspace escape allowed",
			yaml: baseConfig + "security:\n  allow_outside_workspace: true\n",
			want: "may read files outside the workspace root",
		},
		{
			name: "symlinks followed",
			yaml: baseConfig + "security:\n  follow_symlinks: true\n",
			want: "a symlink can point outside",
		},
		{
			name: "team agent reserved",
			yaml: strings.Replace(baseConfig, "    model: coder", "    model: coder\n    type: team", 1),
			want: "reserved and not implemented yet",
		},
		{
			name: "telemetry endpoint without telemetry",
			yaml: baseConfig + "telemetry:\n  otlp_endpoint: http://localhost:4317\n",
			want: "telemetry.otlp_endpoint is set but telemetry is disabled",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := prepare(t, test.yaml)
			result := Validate(cfg)
			if result.HasErrors() {
				t.Fatalf("expected only a warning, but validation failed:\n%s", result.Report())
			}
			if !result.HasWarnings() {
				t.Fatalf("expected a warning containing %q, report:\n%s", test.want, result.Report())
			}
			expectProblem(t, result, test.want, false)
		})
	}
}

func TestValidateProviderNameSyntax(t *testing.T) {
	cfg := prepare(t, `
version: 1
providers:
  "bad name":
    type: ollama
`)
	result := Validate(cfg)
	if !result.HasErrors() {
		t.Fatal("a provider name with a space must be rejected")
	}
	expectProblem(t, result, "provider name must match", true)
}

func TestValidateAgentBudgetsDirectly(t *testing.T) {
	cfg := Default()
	cfg.Providers["local"] = &Provider{Type: ProviderTypeOllama, Timeout: Duration(time.Minute)}
	cfg.Models["coder"] = &Model{Primary: &ModelRef{Provider: "local", Model: "qwen3-coder"}}
	cfg.Agents["reviewer"] = &Agent{
		Model:              "coder",
		Instructions:       "review",
		Tools:              []string{"repo.read"},
		MaxRounds:          -1,
		MaxToolCalls:       -2,
		MaxInputTokens:     -3,
		MaxOutputTokens:    -4,
		MaxContextTokens:   -5,
		MaxToolOutputBytes: -6,
		Timeout:            Duration(0),
		OutputMode:         OutputModeText,
		Permissions:        Permissions{Filesystem: PermissionRead, Shell: PermissionDeny},
	}

	result := Validate(cfg)
	for _, want := range []string{
		"max_rounds must be at least 1",
		"max_tool_calls must not be negative",
		"max_input_tokens must not be negative",
		"max_output_tokens must not be negative",
		"max_context_tokens must not be negative",
		"max_tool_output_bytes must not be negative",
		"timeout must be a positive duration",
	} {
		expectProblem(t, result, want, true)
	}
}

func TestValidateLongTimeoutIsAWarning(t *testing.T) {
	cfg := prepare(t, strings.Replace(baseConfig, "    model: coder", "    model: coder\n    timeout: 6h", 1))
	result := Validate(cfg)
	if result.HasErrors() {
		t.Fatalf("a long timeout must not be an error:\n%s", result.Report())
	}
	expectProblem(t, result, "longer than the", false)
}

func TestValidateTemperatureRange(t *testing.T) {
	cfg := prepare(t, `
version: 1
providers:
  local:
    type: ollama
models:
  coder:
    provider: local
    model: qwen3-coder
    temperature: 5
`)
	result := Validate(cfg)
	if !result.HasErrors() {
		t.Fatal("an out of range temperature must be rejected")
	}
	expectProblem(t, result, "temperature 5.00 is outside the allowed range", true)
}

func TestValidateModelNameSyntax(t *testing.T) {
	cfg := prepare(t, `
version: 1
providers:
  local:
    type: ollama
models:
  "bad model":
    provider: local
    model: qwen3-coder
`)
	result := Validate(cfg)
	expectProblem(t, result, "model name must match", true)
}

func TestValidatePermissionsRequireExplicitValues(t *testing.T) {
	// A hand-built configuration skips Normalize, which is what happens when a
	// caller constructs Config in code. Validation must still catch the gap.
	cfg := &Config{
		Version: CurrentVersion,
		Providers: map[string]*Provider{
			"local": {Type: ProviderTypeOllama},
		},
		Models: map[string]*Model{
			"coder": {Primary: &ModelRef{Provider: "local", Model: "qwen3-coder"}},
		},
		Agents: map[string]*Agent{
			"reviewer": {
				Model:        "coder",
				Instructions: "review",
				Tools:        []string{"repo.read"},
				MaxRounds:    1,
				OutputMode:   OutputModeText,
				Timeout:      Duration(time.Minute),
			},
		},
		Security:  Security{MaxFileSize: DefaultMaxFileSize},
		Sessions:  Sessions{Directory: "/tmp/sessions"},
		Telemetry: Telemetry{LogLevel: "info", LogFormat: "text"},
	}

	result := Validate(cfg)
	expectProblem(t, result, "permissions.filesystem must be read or none", true)
	expectProblem(t, result, "permissions.shell must be deny", true)
}

func TestIsPlannedAndKnownProviderTypes(t *testing.T) {
	if !IsKnownProviderType(ProviderTypeOllama) {
		t.Fatal("ollama must be a known provider type")
	}
	if IsKnownProviderType("made-up") {
		t.Fatal("made-up must not be a known provider type")
	}
	if !IsPlannedProviderType(ProviderTypeGemini) || !IsPlannedProviderType(ProviderTypeBedrock) {
		t.Fatal("gemini and bedrock must be reported as planned")
	}
	if IsPlannedProviderType(ProviderTypeAnthropic) {
		t.Fatal("anthropic must not be reported as planned")
	}
	if !ProviderNeedsBaseURL(ProviderTypeOpenAICompat) {
		t.Fatal("openai-compatible must require a base_url")
	}
	if ProviderNeedsBaseURL(ProviderTypeOllama) {
		t.Fatal("ollama must have a default base_url")
	}
}

func TestIsKnownToolName(t *testing.T) {
	for _, name := range []string{"repo.read", "repo.search", "repo.list", "repo.stat", "git.diff", "git.show", "git.status", "git.log"} {
		if !IsKnownToolName(name) {
			t.Fatalf("%s must be a known tool", name)
		}
	}
	if IsKnownToolName("shell.exec") {
		t.Fatal("shell.exec must not be a known tool")
	}
}
