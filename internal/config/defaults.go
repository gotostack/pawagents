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
	"path/filepath"
	"strings"
	"time"

	"github.com/pawagents/pawagents/internal/security"
)

// Default values applied by ApplyDefaults. They are documented in the README
// and in docs/configuration.md, so keep the two in sync when changing them.
const (
	// DefaultMaxRounds bounds the agent loop when an agent does not set it.
	DefaultMaxRounds = 20
	// DefaultMaxToolCalls bounds the number of tool invocations per task.
	DefaultMaxToolCalls = 50
	// DefaultTimeout bounds one delegated task.
	DefaultTimeout = 5 * time.Minute
	// DefaultMaxToolOutputBytes caps a single tool result, protecting the
	// context window from a 40 MB `git diff`.
	DefaultMaxToolOutputBytes = 256 * 1024
	// DefaultMaxFileSize caps repo.read.
	DefaultMaxFileSize = 1 << 20
	// DefaultProviderTimeout bounds one provider request.
	DefaultProviderTimeout = 60 * time.Second
	// DefaultOllamaProviderTimeout is more generous because local models are
	// slower to produce the first token.
	DefaultOllamaProviderTimeout = 120 * time.Second
	// DefaultLogLevel and DefaultLogFormat configure the structured logger.
	DefaultLogLevel  = "info"
	DefaultLogFormat = "text"
)

// Default returns a fully defaulted configuration with no providers, models or
// agents. It is used when no configuration file exists yet, so that
// `pagent config show` and `pagent doctor` still describe the built-in
// defaults instead of failing.
func Default() *Config {
	cfg := &Config{
		Version:   CurrentVersion,
		Providers: map[string]*Provider{},
		Models:    map[string]*Model{},
		Agents:    map[string]*Agent{},
	}
	ApplyDefaults(cfg)
	cfg.Normalize()
	return cfg
}

// ApplyDefaults fills every unset field with its documented default. It is
// idempotent and safe to call on a partially populated configuration.
func ApplyDefaults(cfg *Config) {
	if cfg == nil {
		return
	}
	cfg.Version = normalizeVersion(cfg.Version)

	for _, provider := range cfg.Providers {
		if provider == nil {
			continue
		}
		if provider.Timeout.IsZero() {
			if provider.Type == ProviderTypeOllama {
				provider.Timeout = Duration(DefaultOllamaProviderTimeout)
			} else {
				provider.Timeout = Duration(DefaultProviderTimeout)
			}
		}
	}

	for _, model := range cfg.Models {
		if model == nil {
			continue
		}
		if model.Primary == nil && (model.Provider != "" || model.Model != "") {
			model.Primary = &ModelRef{Provider: model.Provider, Model: model.Model}
		}
	}

	for _, agent := range cfg.Agents {
		if agent == nil {
			continue
		}
		if agent.Type == "" {
			agent.Type = AgentTypeSingle
		}
		if agent.MaxRounds == 0 {
			agent.MaxRounds = DefaultMaxRounds
		}
		if agent.MaxToolCalls == 0 {
			agent.MaxToolCalls = DefaultMaxToolCalls
		}
		if agent.MaxToolOutputBytes == 0 {
			agent.MaxToolOutputBytes = DefaultMaxToolOutputBytes
		}
		foldLegacyTimeout(agent)
		if agent.Timeout.IsZero() {
			agent.Timeout = Duration(DefaultTimeout)
		}
		if agent.OutputMode == "" {
			agent.OutputMode = OutputModeStructured
		}
	}

	if cfg.Security.MaxFileSize == 0 {
		cfg.Security.MaxFileSize = DefaultMaxFileSize
	}
	if len(cfg.Security.RedactEnv) == 0 {
		cfg.Security.RedactEnv = security.DefaultSecretPatterns()
	}

	if cfg.Sessions.Directory == "" {
		if home, err := Home(); err == nil {
			cfg.Sessions.Directory = filepath.Join(home, "sessions")
		}
	}

	if cfg.Telemetry.LogLevel == "" {
		cfg.Telemetry.LogLevel = DefaultLogLevel
	}
	if cfg.Telemetry.LogFormat == "" {
		cfg.Telemetry.LogFormat = DefaultLogFormat
	}
}

// normalizeVersion maps the unset version to CurrentVersion.
func normalizeVersion(version int) int {
	if version <= 0 {
		return CurrentVersion
	}
	return version
}

// foldLegacyTimeout folds the deprecated timeout_sec alias into Timeout and
// clears the alias so that `pagent config show` prints a single canonical
// field. It runs before the default timeout is applied, otherwise the default
// would mask a value provided only through the alias.
func foldLegacyTimeout(agent *Agent) {
	if agent == nil || agent.TimeoutSec <= 0 {
		return
	}
	if agent.Timeout.IsZero() {
		agent.Timeout = Duration(secondsToDuration(agent.TimeoutSec))
	}
	agent.TimeoutSec = 0
}

// secondsToDuration converts a whole number of seconds into a duration.
func secondsToDuration(seconds int) time.Duration {
	return time.Duration(seconds) * time.Second
}

// DefaultYAML renders the template written by `pagent config init`. The
// placeholder for the PawAgents home directory is expanded with homeDir.
//
// The template is intentionally written against the built-in defaults: it must
// pass `pagent config validate` immediately after `pagent config init`,
// including on a machine with no cloud credentials configured (missing API key
// environment variables are warnings, not errors).
func DefaultYAML(homeDir string) string {
	tpl := `# PawAgents configuration
#
# Default location: ~/.pawagents/config.yaml
# Validate:         pagent config validate
# Inspect:          pagent config show
#
# Every value below can be overridden with an environment variable listed in
# docs/configuration.md. Credentials are read from the environment, never
# stored in this file, unless you explicitly set api_key.

version: 1

providers:

  # OpenAI Responses API.
  openai:
    type: openai-responses
    base_url: https://api.openai.com/v1
    api_key_env: OPENAI_API_KEY

  # Anthropic Messages API.
  anthropic:
    type: anthropic
    base_url: https://api.anthropic.com
    api_key_env: ANTHROPIC_API_KEY

  # Any OpenAI-compatible endpoint: DeepSeek, DashScope, OpenRouter, a company
  # gateway, ...
  deepseek:
    type: openai-compatible
    base_url: https://api.deepseek.com/v1
    api_key_env: DEEPSEEK_API_KEY

  # Native Ollama server. Works without any cloud API.
  local-ollama:
    type: ollama
    base_url: http://127.0.0.1:11434
    timeout: 120s

models:

  cloud-reviewer:
    provider: anthropic
    model: claude-sonnet

  reasoning:
    provider: openai
    model: gpt-reasoning

  cheap-coder:
    provider: deepseek
    model: deepseek-chat

  local-coder:
    provider: local-ollama
    model: qwen3-coder

agents:

  reviewer:
    description: Deep source-code review on a cloud model.
    model: cloud-reviewer
    prompt: __HOME__/prompts/reviewer.md
    tools:
      - repo.read
      - repo.search
      - repo.list
      - git.diff
      - git.show
      - git.status
    max_rounds: 20
    timeout: 5m
    permissions:
      filesystem: read
      shell: deny

  local-reviewer:
    description: Fully local code review using Ollama.
    model: local-coder
    prompt: __HOME__/prompts/reviewer.md
    tools:
      - repo.read
      - repo.search
      - git.diff
    max_rounds: 20
    timeout: 10m

  debugger:
    description: Evidence-driven debugging of a reported failure.
    model: reasoning
    prompt: __HOME__/prompts/debugger.md
    tools:
      - repo.read
      - repo.search
      - git.diff
      - git.log
    max_rounds: 30
    timeout: 10m

security:
  allow_outside_workspace: false
  follow_symlinks: false
  max_file_size: 1048576
  redact_env:
    - "*_TOKEN"
    - "*_KEY"
    - "*_SECRET"
    - "*_PASSWORD"

sessions:
  directory: __HOME__/sessions
  persist_messages: true
  persist_tool_calls: true

telemetry:
  enabled: false
  log_level: info
  log_format: text
`
	return strings.ReplaceAll(tpl, "__HOME__", strings.TrimRight(homeDir, "/"))
}

// ExampleConfig documents the shape of a configuration file for the generated
// documentation. It is not used at runtime.
func ExampleConfig() string { return DefaultYAML("~/.pawagents") }
