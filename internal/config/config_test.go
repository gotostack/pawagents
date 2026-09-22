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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeConfig creates a configuration file inside a temporary directory and
// returns its path.
func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("cannot write the test configuration: %v", err)
	}
	return path
}

// prepare decodes, defaults and normalizes a YAML document the same way Read
// does, without touching the filesystem.
func prepare(t *testing.T, contents string) *Config {
	t.Helper()
	cfg, err := Decode([]byte(contents))
	if err != nil {
		t.Fatalf("Decode() returned %v", err)
	}
	ApplyDefaults(cfg)
	cfg.Normalize()
	return cfg
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	_, err := Decode([]byte("version: 1\nproviders:\n  openai:\n    type: openai-responses\n    base_ur: https://example.com\n"))
	if err == nil {
		t.Fatal("Decode should reject an unknown field")
	}
	if !strings.Contains(err.Error(), `unknown field "base_ur"`) {
		t.Fatalf("error = %q, want it to name the unknown field", err.Error())
	}
	if strings.Contains(err.Error(), "not found in type") {
		t.Fatalf("error = %q, want the yaml.v3 wording to be cleaned up", err.Error())
	}
}

func TestDecodeEmptyAndMultipleDocuments(t *testing.T) {
	if _, err := Decode(nil); err == nil {
		t.Fatal("Decode should reject an empty document")
	} else if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error = %q, want it to mention that the file is empty", err.Error())
	}

	multi := "version: 1\n---\nversion: 2\n"
	if _, err := Decode([]byte(multi)); err == nil {
		t.Fatal("Decode should reject a second YAML document")
	} else if !strings.Contains(err.Error(), "single YAML document") {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestDecodeDurationForms(t *testing.T) {
	cfg := prepare(t, `
version: 1
providers:
  local:
    type: ollama
    timeout: 90s
  gateway:
    type: openai-compatible
    base_url: https://llm.example.com/v1
    timeout: 300
agents:
  quick:
    model: local-model
    timeout: 2m30s
  legacy:
    model: local-model
    timeout_sec: 45
models:
  local-model:
    provider: local
    model: qwen3-coder
`)

	if got := cfg.Providers["local"].Timeout.Duration(); got != 90*time.Second {
		t.Fatalf("provider timeout = %s, want 90s", got)
	}
	if got := cfg.Providers["gateway"].Timeout.Duration(); got != 300*time.Second {
		t.Fatalf("numeric timeout = %s, want 300s", got)
	}
	if got := cfg.Agents["quick"].Timeout.Duration(); got != 150*time.Second {
		t.Fatalf("agent timeout = %s, want 2m30s", got)
	}
	if got := cfg.Agents["legacy"].Timeout.Duration(); got != 45*time.Second {
		t.Fatalf("legacy timeout_sec = %s, want 45s", got)
	}
}

func TestDecodeRejectsInvalidDuration(t *testing.T) {
	_, err := Decode([]byte("version: 1\nproviders:\n  local:\n    type: ollama\n    timeout: fast\n"))
	if err == nil {
		t.Fatal("Decode should reject an invalid duration")
	}
	if !strings.Contains(err.Error(), "invalid duration") {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("cannot determine the home directory: %v", err)
	}

	tests := []struct {
		in   string
		want string
	}{
		{in: "", want: ""},
		{in: "~", want: home},
		{in: "~/prompts/reviewer.md", want: filepath.Join(home, "prompts/reviewer.md")},
		{in: "/etc/hosts", want: "/etc/hosts"},
		{in: "relative/path.md", want: "relative/path.md"},
		{in: "https://example.com/v1", want: "https://example.com/v1"},
	}

	for _, test := range tests {
		if got := ExpandPath(test.in); got != test.want {
			t.Fatalf("ExpandPath(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestResolvePathPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvHome, home)

	def, err := ResolvePath("")
	if err != nil {
		t.Fatalf("ResolvePath() returned %v", err)
	}
	if def != filepath.Join(home, ConfigFileName) {
		t.Fatalf("default path = %q", def)
	}

	t.Setenv(EnvConfigPath, "~/custom.yaml")
	fromEnv, err := ResolvePath("")
	if err != nil {
		t.Fatalf("ResolvePath() returned %v", err)
	}
	userHome, _ := os.UserHomeDir()
	if fromEnv != filepath.Join(userHome, "custom.yaml") {
		t.Fatalf("path from PAWAGENTS_CONFIG = %q", fromEnv)
	}

	explicit := filepath.Join(home, "explicit.yaml")
	got, err := ResolvePath(explicit)
	if err != nil {
		t.Fatalf("ResolvePath() returned %v", err)
	}
	if got != explicit {
		t.Fatalf("explicit path = %q, want %q", got, explicit)
	}
}

func TestReadAppliesEnvOverrides(t *testing.T) {
	path := writeConfig(t, `
version: 1
providers:
  local:
    type: ollama
agents:
  reviewer:
    model: local-model
    tools: [repo.read]
models:
  local-model:
    provider: local
    model: qwen3-coder
telemetry:
  log_level: info
  log_format: text
`)

	t.Setenv(EnvLogLevel, "debug")
	t.Setenv(EnvLogFormat, "json")
	t.Setenv(EnvSessionsDir, "~/alt-sessions")
	t.Setenv(EnvTelemetryEnabled, "true")
	t.Setenv(EnvAllowOutsideWorkspace, "1")

	cfg, err := Read(path, false)
	if err != nil {
		t.Fatalf("Read() returned %v", err)
	}

	if cfg.Telemetry.LogLevel != "debug" {
		t.Fatalf("log level = %q, want debug", cfg.Telemetry.LogLevel)
	}
	if cfg.Telemetry.LogFormat != "json" {
		t.Fatalf("log format = %q, want json", cfg.Telemetry.LogFormat)
	}
	if !cfg.Telemetry.Enabled {
		t.Fatal("telemetry should be enabled by the environment override")
	}
	if !cfg.Security.AllowOutsideWorkspace {
		t.Fatal("allow_outside_workspace should be enabled by the environment override")
	}
	if !strings.HasSuffix(cfg.Sessions.Directory, "alt-sessions") {
		t.Fatalf("sessions directory = %q, want it to use the override", cfg.Sessions.Directory)
	}

	// The override must be skippable so that tests and tooling can observe the
	// file verbatim.
	raw, err := Read(path, true)
	if err != nil {
		t.Fatalf("Read() returned %v", err)
	}
	if raw.Telemetry.LogLevel != "info" {
		t.Fatalf("log level = %q, want the file value", raw.Telemetry.LogLevel)
	}
}

func TestReadMissingFile(t *testing.T) {
	_, err := Read(filepath.Join(t.TempDir(), "absent.yaml"), true)
	if err == nil {
		t.Fatal("Read should fail when the file is missing")
	}
	if !os.IsNotExist(err) && !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("error = %q, want a not-exist error", err.Error())
	}
}

func TestLoadOrDefaultsFallsBack(t *testing.T) {
	t.Setenv(EnvHome, t.TempDir())
	t.Setenv(EnvConfigPath, "")

	cfg, result, usedDefaults, err := LoadOrDefaults(LoadOptions{})
	if err != nil {
		t.Fatalf("LoadOrDefaults() returned %v", err)
	}
	if !usedDefaults {
		t.Fatal("LoadOrDefaults should report that defaults were used")
	}
	if result.HasErrors() {
		t.Fatalf("built-in defaults must validate cleanly, got %s", result.Report())
	}
	if cfg.Version != CurrentVersion {
		t.Fatalf("version = %d, want %d", cfg.Version, CurrentVersion)
	}
	if !strings.HasSuffix(cfg.Sessions.Directory, "sessions") {
		t.Fatalf("sessions directory = %q", cfg.Sessions.Directory)
	}
}

func TestNormalizeCollapsesModelShortForm(t *testing.T) {
	cfg := prepare(t, `
version: 1
providers:
  local:
    type: ollama
models:
  coder:
    provider: local
    model: qwen3-coder
    fallback:
      - provider: local
        model: llama3
`)

	model := cfg.Models["coder"]
	targets := model.Targets()
	if len(targets) != 2 {
		t.Fatalf("targets = %v, want two entries", targets)
	}
	if targets[0] != (ModelRef{Provider: "local", Model: "qwen3-coder"}) {
		t.Fatalf("primary target = %v", targets[0])
	}
	if targets[1] != (ModelRef{Provider: "local", Model: "llama3"}) {
		t.Fatalf("fallback target = %v", targets[1])
	}
	if model.Provider != "" || model.Model != "" {
		t.Fatalf("short form must be folded into primary, got %q/%q", model.Provider, model.Model)
	}
}

func TestNormalizeDefaultsAgentPermissions(t *testing.T) {
	cfg := prepare(t, `
version: 1
agents:
  reviewer:
    model: any
`)
	agent := cfg.Agents["reviewer"]
	if agent.Permissions.Filesystem != PermissionRead {
		t.Fatalf("filesystem = %q, want read", agent.Permissions.Filesystem)
	}
	if agent.Permissions.Shell != PermissionDeny {
		t.Fatalf("shell = %q, want deny", agent.Permissions.Shell)
	}
	if agent.MaxRounds != DefaultMaxRounds {
		t.Fatalf("max_rounds = %d, want %d", agent.MaxRounds, DefaultMaxRounds)
	}
	if agent.MaxToolCalls != DefaultMaxToolCalls {
		t.Fatalf("max_tool_calls = %d, want %d", agent.MaxToolCalls, DefaultMaxToolCalls)
	}
	if agent.Timeout.Duration() != DefaultTimeout {
		t.Fatalf("timeout = %s, want %s", agent.Timeout, DefaultTimeout)
	}
	if agent.OutputMode != OutputModeStructured {
		t.Fatalf("output_mode = %q", agent.OutputMode)
	}
}

func TestDefaultOllamaTimeoutIsLonger(t *testing.T) {
	cfg := prepare(t, `
version: 1
providers:
  local:
    type: ollama
  cloud:
    type: anthropic
`)
	if got := cfg.Providers["local"].Timeout.Duration(); got != DefaultOllamaProviderTimeout {
		t.Fatalf("ollama timeout = %s, want %s", got, DefaultOllamaProviderTimeout)
	}
	if got := cfg.Providers["cloud"].Timeout.Duration(); got != DefaultProviderTimeout {
		t.Fatalf("cloud timeout = %s, want %s", got, DefaultProviderTimeout)
	}
}

func TestDefaultTemplateValidates(t *testing.T) {
	home := t.TempDir()
	cfg := prepare(t, DefaultYAML(home))
	cfg.Path = filepath.Join(home, ConfigFileName)

	result := Validate(cfg)
	if result.HasErrors() {
		t.Fatalf("the generated template must not contain errors:\n%s", result.Report())
	}

	// The only tolerated warnings are missing credentials and missing prompt
	// files; both are expected in a freshly generated directory.
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "environment variable") || strings.Contains(warning, "does not exist") {
			continue
		}
		t.Fatalf("unexpected warning in the generated template: %s", warning)
	}

	for _, name := range []string{"reviewer", "local-reviewer", "debugger"} {
		if _, ok := cfg.Agents[name]; !ok {
			t.Fatalf("the generated template is missing the %q agent", name)
		}
	}
	if !strings.Contains(DefaultYAML(home), home) {
		t.Fatal("the generated template must embed the resolved home directory")
	}
}

func TestRedacted(t *testing.T) {
	cfg := prepare(t, `
version: 1
providers:
  gateway:
    type: openai-compatible
    base_url: https://llm.example.com/v1
    api_key: sk-inline-secret
    headers:
      Authorization: Bearer header-secret
      X-Tenant: acme
    extra_body:
      api_key: body-secret
`)

	redacted := cfg.Redacted()
	provider := redacted.Providers["gateway"]
	if provider.APIKey == "sk-inline-secret" {
		t.Fatal("api_key must be redacted")
	}
	if provider.Headers["Authorization"] == "Bearer header-secret" {
		t.Fatal("the Authorization header must be redacted")
	}
	if provider.Headers["X-Tenant"] != "acme" {
		t.Fatalf("X-Tenant = %q, want it preserved", provider.Headers["X-Tenant"])
	}
	if body, ok := provider.ExtraBody["api_key"]; !ok || body == "body-secret" {
		t.Fatalf("extra_body.api_key = %v, want it redacted", body)
	}

	if cfg.Providers["gateway"].APIKey != "sk-inline-secret" {
		t.Fatal("Redacted must not mutate the original configuration")
	}

	yamlDoc, err := redacted.YAMLDocument()
	if err != nil {
		t.Fatalf("YAMLDocument() returned %v", err)
	}
	if strings.Contains(string(yamlDoc), "sk-inline-secret") {
		t.Fatalf("rendered YAML leaked the credential:\n%s", yamlDoc)
	}

	jsonDoc, err := redacted.JSONDocument()
	if err != nil {
		t.Fatalf("JSONDocument() returned %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(jsonDoc, &decoded); err != nil {
		t.Fatalf("JSONDocument() produced invalid JSON: %v", err)
	}
	if decoded["version"] != float64(CurrentVersion) {
		t.Fatalf("version = %v", decoded["version"])
	}
}

func TestAccessorsAndHelpers(t *testing.T) {
	cfg := Default()
	cfg.Providers["b"] = &Provider{Type: ProviderTypeOllama}
	cfg.Providers["a"] = &Provider{Type: ProviderTypeOllama}
	cfg.Providers["off"] = &Provider{Type: ProviderTypeOllama, Disabled: true}

	if got := cfg.ProviderNames(); strings.Join(got, ",") != "a,b,off" {
		t.Fatalf("ProviderNames() = %v", got)
	}
	if got := cfg.EnabledProviders(); strings.Join(got, ",") != "a,b" {
		t.Fatalf("EnabledProviders() = %v", got)
	}

	cfg.Agents["reviewer"] = &Agent{Model: "coder"}
	if _, err := cfg.Agent("reviewer"); err != nil {
		t.Fatalf("Agent() returned %v", err)
	}
	if _, err := cfg.Agent("ghost"); err == nil {
		t.Fatal("Agent() should fail for an unknown name")
	}

	cfg.Models["coder"] = &Model{Provider: "a", Model: "qwen3-coder"}
	cfg.Normalize()
	targets, _, err := cfg.ModelTargets("coder")
	if err != nil {
		t.Fatalf("ModelTargets() returned %v", err)
	}
	if len(targets) != 1 || targets[0].Model != "qwen3-coder" {
		t.Fatalf("targets = %v", targets)
	}
	if _, _, err := cfg.ModelTargets("ghost"); err == nil {
		t.Fatal("ModelTargets() should fail for an unknown alias")
	}
	if !strings.Contains(cfg.String(), "providers=") {
		t.Fatalf("String() = %q", cfg.String())
	}
}

func TestWriteFileRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", ConfigFileName)

	if err := WriteFile(path, "version: 1\n", false); err != nil {
		t.Fatalf("WriteFile() returned %v", err)
	}
	if err := WriteFile(path, "version: 2\n", false); err == nil {
		t.Fatal("WriteFile should refuse to overwrite without force")
	}
	if err := WriteFile(path, "version: 2\n", true); err != nil {
		t.Fatalf("WriteFile(force) returned %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read back the file: %v", err)
	}
	if string(data) != "version: 2\n" {
		t.Fatalf("contents = %q", data)
	}
}
