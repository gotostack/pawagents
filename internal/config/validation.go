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
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/llm"
)

// Validation limits. They are named so that the error messages can quote them.
const (
	// MaxTaskTimeout is the largest task timeout accepted by validation. A
	// longer bound almost always means a stalled agent rather than a slow one.
	MaxTaskTimeout = 2 * time.Hour
	// MinUsefulContextTokens guards against a context window so small that no
	// agent loop could make progress.
	MinUsefulContextTokens = 2048
	// MaxTemperature is the highest sampling temperature accepted.
	MaxTemperature = 2.0
)

// identPattern matches provider, model and agent names.
var identPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidationResult is the structured report produced by Validate.
type ValidationResult struct {
	// Path is the configuration file the report refers to. It may be empty
	// when the report was produced from built-in defaults.
	Path string
	// Errors are problems that make the configuration unusable.
	Errors []error
	// Warnings are problems that are tolerated but should be fixed.
	Warnings []string
}

// HasErrors reports whether validation failed.
func (r *ValidationResult) HasErrors() bool {
	return r != nil && len(r.Errors) > 0
}

// HasWarnings reports whether validation produced warnings.
func (r *ValidationResult) HasWarnings() bool {
	return r != nil && len(r.Warnings) > 0
}

// IsClean reports whether validation produced neither errors nor warnings.
func (r *ValidationResult) IsClean() bool {
	return r != nil && !r.HasErrors() && !r.HasWarnings()
}

// Err returns the aggregated error, or nil when validation passed.
func (r *ValidationResult) Err() error {
	if r == nil || len(r.Errors) == 0 {
		return nil
	}
	header := fmt.Sprintf("configuration %s is invalid", r.describePath())
	return apperrors.Multi(apperrors.KindConfig, "config.validate", header, r.Errors...)
}

// Summary returns a short human description such as "2 errors, 1 warning".
func (r *ValidationResult) Summary() string {
	if r == nil {
		return "no report"
	}
	errors := len(r.Errors)
	warnings := len(r.Warnings)
	if errors == 0 && warnings == 0 {
		return "no problems found"
	}
	return fmt.Sprintf("%s, %s", count(errors, "error", "errors"), count(warnings, "warning", "warnings"))
}

// Report renders the result as an indented, multi-line block suitable for CLI
// output. Every problem lists the configuration key it belongs to.
func (r *ValidationResult) Report() string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	for _, err := range r.Errors {
		fmt.Fprintf(&b, "  ✗ %s\n", strings.ReplaceAll(err.Error(), "\n", "\n    "))
	}
	for _, warning := range r.Warnings {
		fmt.Fprintf(&b, "  ! %s\n", warning)
	}
	if r.IsClean() {
		b.WriteString("  ✓ no problems found\n")
	}
	return b.String()
}

func (r *ValidationResult) describePath() string {
	if r.Path == "" {
		return "(built-in defaults)"
	}
	return r.Path
}

func (r *ValidationResult) add(format string, args ...any) {
	r.Errors = append(r.Errors, fmt.Errorf(format, args...))
}

func (r *ValidationResult) warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// Validate checks a configuration document and returns every problem it finds.
//
// Errors make the configuration unusable: an unknown provider type, a model
// that points at a provider that does not exist, an agent that requests shell
// access. Warnings describe a configuration that can still run: a missing API
// key environment variable, a prompt file that has not been created yet, or a
// roadmap feature that is accepted but not implemented.
func Validate(cfg *Config) *ValidationResult {
	result := &ValidationResult{}
	if cfg == nil {
		result.add("configuration is nil")
		return result
	}
	result.Path = cfg.Path

	validateVersion(cfg, result)
	validateProviders(cfg, result)
	validateModels(cfg, result)
	validateAgents(cfg, result)
	validateSecurity(cfg, result)
	validateSessions(cfg, result)
	validateTelemetry(cfg, result)

	return result
}

func validateVersion(cfg *Config, result *ValidationResult) {
	if cfg.Version != CurrentVersion {
		result.add("version: unsupported configuration version %d (this build understands version %d)",
			cfg.Version, CurrentVersion)
	}
}

func validateProviders(cfg *Config, result *ValidationResult) {
	for _, name := range cfg.ProviderNames() {
		provider := cfg.Providers[name]
		label := "providers." + name

		if !identPattern.MatchString(name) {
			result.add("%s: provider name must match %s", label, identPattern.String())
		}
		if provider == nil {
			result.add("%s: provider entry is empty", label)
			continue
		}
		if provider.Disabled {
			continue
		}
		if provider.Type == "" {
			result.add("%s: type is required (known types: %s)", label,
				strings.Join(KnownProviderTypes(), ", "))
			continue
		}
		if !IsKnownProviderType(provider.Type) {
			result.add("%s: unsupported provider type %q (known types: %s)", label,
				provider.Type, strings.Join(KnownProviderTypes(), ", "))
			continue
		}
		if IsPlannedProviderType(provider.Type) {
			result.warn("%s: provider type %q is on the roadmap and not implemented yet; "+
				"requests to this provider will fail", label, provider.Type)
		}
		if provider.BaseURL == "" && ProviderNeedsBaseURL(provider.Type) {
			result.add("%s: base_url is required for provider type %q", label, provider.Type)
		}
		if provider.BaseURL != "" {
			parsed, err := url.Parse(provider.BaseURL)
			switch {
			case err != nil:
				result.add("%s: base_url %q is not a valid URL", label, provider.BaseURL)
			case parsed.Scheme != "http" && parsed.Scheme != "https":
				result.add("%s: base_url %q must use the http or https scheme", label, provider.BaseURL)
			case parsed.Host == "":
				result.add("%s: base_url %q is missing a host", label, provider.BaseURL)
			default:
				if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
					result.warn("%s: base_url uses plaintext http to a remote host; "+
						"the API key would be sent unencrypted", label)
				}
			}
		}
		if provider.APIKey != "" && provider.APIKeyEnv != "" {
			result.warn("%s: both api_key and api_key_env are set; api_key_env takes precedence", label)
		}
		if provider.APIKey == "" && provider.APIKeyEnv == "" && ProviderNeedsCredential(provider.Type) {
			result.warn("%s: no credentials configured; set api_key_env to an environment variable", label)
		}
		if provider.APIKeyEnv != "" && os.Getenv(provider.APIKeyEnv) == "" {
			result.warn("%s: environment variable %s is not set", label, provider.APIKeyEnv)
		}
		if provider.Timeout.Duration() < 0 {
			result.add("%s: timeout must not be negative", label)
		}
		if provider.TLS.ClientCertFile != "" && provider.TLS.ClientKeyFile == "" {
			result.add("%s: tls.client_key_file is required when tls.client_cert_file is set", label)
		}
		if provider.TLS.ClientKeyFile != "" && provider.TLS.ClientCertFile == "" {
			result.add("%s: tls.client_cert_file is required when tls.client_key_file is set", label)
		}
		if provider.TLS.InsecureSkipVerify {
			result.warn("%s: tls.insecure_skip_verify disables certificate validation", label)
		}
		if provider.Proxy != "" {
			parsed, err := url.Parse(provider.Proxy)
			if err != nil || parsed.Host == "" {
				result.add("%s: proxy %q is not a valid URL", label, provider.Proxy)
			}
		}
	}
}

func validateModels(cfg *Config, result *ValidationResult) {
	for _, name := range cfg.ModelNames() {
		model := cfg.Models[name]
		label := "models." + name

		if !identPattern.MatchString(name) {
			result.add("%s: model name must match %s", label, identPattern.String())
		}
		if model == nil {
			result.add("%s: model entry is empty", label)
			continue
		}

		targets := model.Targets()
		if len(targets) == 0 {
			result.add("%s: no provider target; set provider/model or primary", label)
			continue
		}

		for index, target := range targets {
			targetLabel := label + ".primary"
			if model.Primary == nil {
				targetLabel = label
			}
			if index > 0 {
				targetLabel = fmt.Sprintf("%s.fallback[%d]", label, index-1)
			}

			if target.Provider == "" {
				result.add("%s: provider is required", targetLabel)
			} else if _, ok := cfg.Providers[target.Provider]; !ok {
				result.add("%s: provider %q is not defined in providers", targetLabel, target.Provider)
			}
			if target.Model == "" {
				result.add("%s: model is required", targetLabel)
			}
		}

		if len(model.Fallback) > 0 {
			result.warn("%s: provider fallback is not implemented yet; only the primary "+
				"target will be used", label)
		}
		if model.Temperature != nil {
			if *model.Temperature < 0 || *model.Temperature > MaxTemperature {
				result.add("%s: temperature %.2f is outside the allowed range [0, %.1f]",
					label, *model.Temperature, MaxTemperature)
			}
		}
		if model.MaxOutputTokens < 0 {
			result.add("%s: max_output_tokens must not be negative", label)
		}
		if model.MaxContextTokens < 0 {
			result.add("%s: max_context_tokens must not be negative", label)
		}
		if model.MaxContextTokens > 0 && model.MaxContextTokens < MinUsefulContextTokens {
			result.warn("%s: max_context_tokens %d is below the %d token minimum an "+
				"agent loop needs", label, model.MaxContextTokens, MinUsefulContextTokens)
		}
		if model.Capabilities != nil && model.Capabilities.MaxContextTokens != nil &&
			*model.Capabilities.MaxContextTokens < MinUsefulContextTokens {
			result.warn("%s: capabilities.max_context_tokens %d is below the %d token minimum",
				label, *model.Capabilities.MaxContextTokens, MinUsefulContextTokens)
		}
	}
}

func validateAgents(cfg *Config, result *ValidationResult) {
	for _, name := range cfg.AgentNames() {
		agent := cfg.Agents[name]
		label := "agents." + name

		if !identPattern.MatchString(name) {
			result.add("%s: agent name must match %s", label, identPattern.String())
		}
		if agent == nil {
			result.add("%s: agent entry is empty", label)
			continue
		}

		if agent.Type != "" && agent.Type != "single" {
			result.warn("%s: agent type %q is reserved and not implemented yet; "+
				"treating it as a single agent", label, agent.Type)
		}

		switch {
		case agent.Model == "":
			result.add("%s: model is required", label)
		default:
			if _, ok := cfg.Models[agent.Model]; !ok {
				result.add("%s: model %q is not defined in models", label, agent.Model)
			}
		}

		switch {
		case agent.Prompt != "" && agent.Instructions != "":
			result.add("%s: prompt and instructions are mutually exclusive", label)
		case agent.Prompt == "" && agent.Instructions == "":
			result.warn("%s: no prompt configured; the built-in system prompt will be used", label)
		case agent.Prompt != "" && !Exists(agent.Prompt):
			result.warn("%s: prompt file %s does not exist", label, agent.Prompt)
		}

		switch agent.OutputMode {
		case OutputModeText, OutputModeStructured:
		default:
			result.add("%s: output_mode %q is invalid (want %s or %s)", label,
				agent.OutputMode, OutputModeText, OutputModeStructured)
		}

		for _, tool := range agent.Tools {
			// The tool name pattern comes from internal/llm so that a name that
			// validates here is also a name the runtime can dispatch.
			if !llm.IsValidToolName(tool) {
				result.add("%s: tool %q is not a valid tool name (want namespace.tool)", label, tool)
				continue
			}
			if !IsKnownToolName(tool) {
				result.warn("%s: tool %q is not part of the built-in tool runtime; "+
					"the run will fail with a capability error", label, tool)
			}
		}
		if len(agent.Tools) == 0 {
			result.warn("%s: no tools granted; the agent can only answer from the task text", label)
		}
		if duplicates := findDuplicates(agent.Tools); len(duplicates) > 0 {
			result.warn("%s: tool list contains duplicates: %s", label, strings.Join(duplicates, ", "))
		}

		validateAgentPermissions(agent, label, result)
		validateAgentBudgets(agent, label, result)
	}
}

func validateAgentPermissions(agent *Agent, label string, result *ValidationResult) {
	switch agent.Permissions.Filesystem {
	case PermissionRead, PermissionNone:
	case "":
		result.add("%s: permissions.filesystem must be %s or %s", label, PermissionRead, PermissionNone)
	default:
		result.add("%s: permissions.filesystem %q is not supported; PawAgents subagents are "+
			"read-only by default (want %s or %s)", label, agent.Permissions.Filesystem,
			PermissionRead, PermissionNone)
	}

	switch agent.Permissions.Shell {
	case PermissionDeny, PermissionNone:
	case "":
		result.add("%s: permissions.shell must be %s", label, PermissionDeny)
	default:
		result.add("%s: permissions.shell %q is not supported; PawAgents never executes "+
			"shell commands", label, agent.Permissions.Shell)
	}

	for _, tool := range append(append([]string{}, agent.Permissions.Allow...), agent.Permissions.Deny...) {
		if !llm.IsValidToolName(tool) {
			result.add("%s: permissions entry %q is not a valid tool name (want namespace.tool)",
				label, tool)
		}
	}
}

func validateAgentBudgets(agent *Agent, label string, result *ValidationResult) {
	if agent.MaxRounds < 1 {
		result.add("%s: max_rounds must be at least 1", label)
	}
	if agent.MaxToolCalls < 0 {
		result.add("%s: max_tool_calls must not be negative", label)
	}
	if agent.MaxInputTokens < 0 {
		result.add("%s: max_input_tokens must not be negative", label)
	}
	if agent.MaxOutputTokens < 0 {
		result.add("%s: max_output_tokens must not be negative", label)
	}
	if agent.MaxContextTokens < 0 {
		result.add("%s: max_context_tokens must not be negative", label)
	}
	if agent.MaxToolOutputBytes < 0 {
		result.add("%s: max_tool_output_bytes must not be negative", label)
	}
	if agent.Timeout.Duration() <= 0 {
		result.add("%s: timeout must be a positive duration", label)
	}
	if agent.Timeout.Duration() > MaxTaskTimeout {
		result.warn("%s: timeout %s is longer than the %s recommended maximum", label,
			agent.Timeout, MaxTaskTimeout)
	}
}

func validateSecurity(cfg *Config, result *ValidationResult) {
	if cfg.Security.MaxFileSize < 0 {
		result.add("security.max_file_size must not be negative")
	}
	if cfg.Security.AllowOutsideWorkspace {
		result.warn("security.allow_outside_workspace is enabled; repository tools may read " +
			"files outside the workspace root")
	}
	if cfg.Security.FollowSymlinks {
		result.warn("security.follow_symlinks is enabled; a symlink can point outside the " +
			"workspace root")
	}
	for _, pattern := range cfg.Security.BlockedPaths {
		if strings.TrimSpace(pattern) == "" {
			result.add("security.blocked_paths contains an empty entry")
		}
	}
	for _, name := range cfg.Security.AllowedEnv {
		if strings.TrimSpace(name) == "" {
			result.add("security.allowed_env contains an empty entry")
		}
	}
}

func validateSessions(cfg *Config, result *ValidationResult) {
	if strings.TrimSpace(cfg.Sessions.Directory) == "" {
		result.add("sessions.directory must not be empty")
	}
	if cfg.Sessions.Retention < 0 {
		result.add("sessions.retention must not be negative")
	}
}

func validateTelemetry(cfg *Config, result *ValidationResult) {
	switch cfg.Telemetry.LogLevel {
	case "debug", "info", "warn", "error", "off":
	default:
		result.add("telemetry.log_level %q is invalid (want debug, info, warn, error or off)",
			cfg.Telemetry.LogLevel)
	}
	switch cfg.Telemetry.LogFormat {
	case "text", "json":
	default:
		result.add("telemetry.log_format %q is invalid (want text or json)", cfg.Telemetry.LogFormat)
	}
	if cfg.Telemetry.OTLPEndpoint != "" && !cfg.Telemetry.Enabled {
		result.warn("telemetry.otlp_endpoint is set but telemetry is disabled")
	}
}

// isLoopbackHost reports whether a host refers to the local machine.
func isLoopbackHost(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1", "":
		return true
	}
	return strings.HasPrefix(host, "127.")
}

// findDuplicates returns the duplicated entries of a list, sorted and
// deduplicated so that a report stays stable.
func findDuplicates(values []string) []string {
	seen := make(map[string]int, len(values))
	for _, value := range values {
		seen[value]++
	}
	out := make([]string, 0, len(seen))
	for value, count := range seen {
		if count > 1 {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

// count formats "1 error" / "2 errors".
func count(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}
