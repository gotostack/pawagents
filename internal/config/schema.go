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
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// CurrentVersion is the configuration schema version understood by this build.
const CurrentVersion = 1

// Provider type identifiers. They appear in the `type` field of a provider
// entry in config.yaml and are part of the public contract.
const (
	ProviderTypeOpenAIResponses = "openai-responses"
	ProviderTypeOpenAIChat      = "openai-chat"
	ProviderTypeAnthropic       = "anthropic"
	ProviderTypeOpenAICompat    = "openai-compatible"
	ProviderTypeOllama          = "ollama"
	ProviderTypeGemini          = "gemini"
	ProviderTypeBedrock         = "bedrock"
)

// Permission levels for the filesystem and shell capabilities of an agent.
const (
	PermissionRead  = "read"
	PermissionNone  = "none"
	PermissionDeny  = "deny"
	PermissionAllow = "allow"
)

// Output modes for a delegated task.
const (
	OutputModeText       = "text"
	OutputModeStructured = "structured"
)

// Duration is a time.Duration that unmarshals from either a human readable
// string ("300s", "5m") or a bare number interpreted as seconds. Marshalling
// always produces the human readable form so that `pagent config show` output
// stays self-describing.
type Duration time.Duration

// Duration returns the value as a time.Duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// IsZero reports whether the duration is unset.
func (d Duration) IsZero() bool { return time.Duration(d) == 0 }

// String renders the duration using the standard Go format.
func (d Duration) String() string { return time.Duration(d).String() }

// UnmarshalYAML accepts a number of seconds or a duration string.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	switch node.Tag {
	case "!!int", "!!float":
		var seconds float64
		if err := node.Decode(&seconds); err != nil {
			return fmt.Errorf("invalid duration %q: %w", node.Value, err)
		}
		*d = Duration(time.Duration(seconds * float64(time.Second)))
		return nil
	case "!!str":
		raw := strings.TrimSpace(node.Value)
		if raw == "" {
			*d = 0
			return nil
		}
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", raw, err)
		}
		*d = Duration(parsed)
		return nil
	case "!!null":
		*d = 0
		return nil
	default:
		return fmt.Errorf("invalid duration value %q", node.Value)
	}
}

// MarshalYAML renders the duration as a string such as "5m0s".
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// MarshalJSON renders the duration as a string such as "5m0s".
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON accepts a number of seconds or a duration string.
func (d *Duration) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		*d = 0
		return nil
	}
	if strings.HasPrefix(raw, `"`) {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		text = strings.TrimSpace(text)
		if text == "" {
			*d = 0
			return nil
		}
		parsed, err := time.ParseDuration(text)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", text, err)
		}
		*d = Duration(parsed)
		return nil
	}
	var seconds float64
	if err := json.Unmarshal(data, &seconds); err != nil {
		return fmt.Errorf("invalid duration %s: %w", raw, err)
	}
	*d = Duration(time.Duration(seconds * float64(time.Second)))
	return nil
}

// TLS holds transport security options for a provider endpoint.
type TLS struct {
	// InsecureSkipVerify disables certificate verification. It exists for
	// local gateways with self-signed certificates and produces a validation
	// warning.
	InsecureSkipVerify bool `yaml:"insecure_skip_verify,omitempty" json:"insecure_skip_verify,omitempty"`
	// CACertFile is a PEM bundle used to trust a private CA.
	CACertFile string `yaml:"ca_cert_file,omitempty" json:"ca_cert_file,omitempty"`
	// ClientCertFile and ClientKeyFile enable mutual TLS.
	ClientCertFile string `yaml:"client_cert_file,omitempty" json:"client_cert_file,omitempty"`
	ClientKeyFile  string `yaml:"client_key_file,omitempty" json:"client_key_file,omitempty"`
	// ServerName overrides the TLS server name used for verification.
	ServerName string `yaml:"server_name,omitempty" json:"server_name,omitempty"`
}

// IsZero reports whether no TLS option is configured.
func (t TLS) IsZero() bool {
	return !t.InsecureSkipVerify && t.CACertFile == "" && t.ClientCertFile == "" &&
		t.ClientKeyFile == "" && t.ServerName == ""
}

// Provider describes one model endpoint.
type Provider struct {
	// Type selects the wire protocol. See the ProviderType* constants.
	Type string `yaml:"type" json:"type"`
	// BaseURL is the endpoint root, for example https://api.deepseek.com/v1.
	BaseURL string `yaml:"base_url,omitempty" json:"base_url,omitempty"`
	// APIKey is an inline credential. Prefer APIKeyEnv so that secrets stay
	// out of the configuration file.
	APIKey string `yaml:"api_key,omitempty" json:"api_key,omitempty"`
	// APIKeyEnv names the environment variable holding the credential.
	APIKeyEnv string `yaml:"api_key_env,omitempty" json:"api_key_env,omitempty"`
	// Headers are extra HTTP headers merged into every request.
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	// ExtraBody is merged into the JSON request body, which lets users reach
	// vendor specific knobs without a code change.
	ExtraBody map[string]any `yaml:"extra_body,omitempty" json:"extra_body,omitempty"`
	// Timeout bounds a single provider request. Streaming responses refresh
	// the bound on every event.
	Timeout Duration `yaml:"timeout" json:"timeout"`
	// Proxy is an HTTP or SOCKS5 proxy URL.
	Proxy string `yaml:"proxy,omitempty" json:"proxy,omitempty"`
	// TLS configures certificate handling.
	TLS TLS `yaml:"tls,omitempty" json:"tls,omitzero"`
	// Organization and Region are optional vendor fields (OpenAI, Bedrock).
	Organization string `yaml:"organization,omitempty" json:"organization,omitempty"`
	Region       string `yaml:"region,omitempty" json:"region,omitempty"`
	// Disabled removes the provider from the registry without deleting the
	// configuration block.
	Disabled bool `yaml:"disabled,omitempty" json:"disabled,omitempty"`
}

// ModelRef points at a concrete model served by a named provider.
type ModelRef struct {
	Provider string `yaml:"provider" json:"provider"`
	Model    string `yaml:"model" json:"model"`
}

// IsZero reports whether the reference is unset.
func (m ModelRef) IsZero() bool { return m.Provider == "" && m.Model == "" }

// String renders "provider/model" for diagnostics.
func (m ModelRef) String() string { return m.Provider + "/" + m.Model }

// CapabilityOverrides lets an operator correct the capability detection for a
// model, for example when a local model supports tools but does not advertise
// them.
type CapabilityOverrides struct {
	ToolCalling      *bool `yaml:"tool_calling" json:"tool_calling,omitempty"`
	ParallelTools    *bool `yaml:"parallel_tools" json:"parallel_tools,omitempty"`
	Streaming        *bool `yaml:"streaming" json:"streaming,omitempty"`
	StructuredOutput *bool `yaml:"structured_output" json:"structured_output,omitempty"`
	Vision           *bool `yaml:"vision" json:"vision,omitempty"`
	Reasoning        *bool `yaml:"reasoning" json:"reasoning,omitempty"`
	SystemMessage    *bool `yaml:"system_message" json:"system_message,omitempty"`
	MaxContextTokens *int  `yaml:"max_context_tokens" json:"max_context_tokens,omitempty"`
}

// IsZero reports whether no override is configured.
func (c CapabilityOverrides) IsZero() bool {
	return c.ToolCalling == nil && c.ParallelTools == nil && c.Streaming == nil &&
		c.StructuredOutput == nil && c.Vision == nil && c.Reasoning == nil &&
		c.SystemMessage == nil && c.MaxContextTokens == nil
}

// Model binds an alias such as "strong-coder" to one or more provider targets.
//
// Two shapes are accepted. The short form:
//
//	models:
//	  strong-coder:
//	    provider: anthropic
//	    model: claude-sonnet
//
// and the fallback-aware form:
//
//	models:
//	  strong-coder:
//	    primary:
//	      provider: anthropic
//	      model: claude-sonnet
//	    fallback:
//	      - provider: openai
//	        model: gpt-codex
//
// Normalize collapses the short form into Primary so the runtime only ever
// reads Targets().
type Model struct {
	Provider string     `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model    string     `yaml:"model,omitempty" json:"model,omitempty"`
	Primary  *ModelRef  `yaml:"primary,omitempty" json:"primary,omitempty"`
	Fallback []ModelRef `yaml:"fallback,omitempty" json:"fallback,omitempty"`

	// Temperature overrides the provider default when set.
	Temperature *float64 `yaml:"temperature,omitempty" json:"temperature,omitempty"`
	// MaxOutputTokens caps a single completion.
	MaxOutputTokens int `yaml:"max_output_tokens,omitempty" json:"max_output_tokens,omitempty"`
	// MaxContextTokens declares the usable context window of the model.
	MaxContextTokens int `yaml:"max_context_tokens,omitempty" json:"max_context_tokens,omitempty"`
	// Capabilities overrides auto-detected model capabilities.
	Capabilities *CapabilityOverrides `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
	// Extra is merged into the provider request body.
	Extra map[string]any `yaml:"extra,omitempty" json:"extra,omitempty"`
}

// Targets returns the ordered list of provider targets, primary first.
func (m *Model) Targets() []ModelRef {
	if m == nil {
		return nil
	}
	out := make([]ModelRef, 0, 1+len(m.Fallback))
	if m.Primary != nil && !m.Primary.IsZero() {
		out = append(out, *m.Primary)
	} else if m.Provider != "" || m.Model != "" {
		out = append(out, ModelRef{Provider: m.Provider, Model: m.Model})
	}
	for _, ref := range m.Fallback {
		if !ref.IsZero() {
			out = append(out, ref)
		}
	}
	return out
}

// Permissions narrows what an agent may do. PawAgents subagents are read-only
// by default and shell access is disabled; both defaults are enforced by
// validation rather than only by documentation.
type Permissions struct {
	// Filesystem is read or none.
	Filesystem string `yaml:"filesystem" json:"filesystem,omitempty"`
	// Shell must stay deny; PawAgents never executes arbitrary commands.
	Shell string `yaml:"shell" json:"shell,omitempty"`
	// Allow lists additional tool names granted on top of Agent.Tools.
	Allow []string `yaml:"allow,omitempty" json:"allow,omitempty"`
	// Deny removes tool names even when they appear in Agent.Tools.
	Deny []string `yaml:"deny,omitempty" json:"deny,omitempty"`
}

// IsZero reports whether no explicit permission is configured.
func (p Permissions) IsZero() bool {
	return p.Filesystem == "" && p.Shell == "" && len(p.Allow) == 0 && len(p.Deny) == 0
}

// Agent types. Only a single agent is executed today; a team is reserved for
// the multi agent roadmap and is accepted by validation with a warning so that
// a forward looking configuration still loads.
const (
	// AgentTypeSingle is one agent, one model, one task.
	AgentTypeSingle = "single"
	// AgentTypeTeam is a group of agents.
	AgentTypeTeam = "team"
)

// Agent is a reusable, model-agnostic delegation target.
type Agent struct {
	// Type is AgentTypeSingle for a normal agent. AgentTypeTeam is reserved
	// for a later release and is rejected by validation with a warning.
	Type string `yaml:"type" json:"type,omitempty"`
	// Description is shown by list_agents so a host can pick an agent.
	Description string `yaml:"description" json:"description,omitempty"`
	// Model references a key in the top-level models map.
	Model string `yaml:"model" json:"model"`
	// Prompt is a path to a markdown system prompt. Supports "~".
	Prompt string `yaml:"prompt,omitempty" json:"prompt,omitempty"`
	// Instructions is an inline system prompt. Mutually exclusive with Prompt.
	Instructions string `yaml:"instructions,omitempty" json:"instructions,omitempty"`
	// Tools lists the tool names granted to this agent.
	Tools []string `yaml:"tools,omitempty" json:"tools,omitempty"`
	// OutputMode is text or structured. Structured makes the runtime ask for
	// the finding envelope described in the README.
	OutputMode string `yaml:"output_mode,omitempty" json:"output_mode,omitempty"`

	// Budget knobs.
	MaxRounds          int      `yaml:"max_rounds,omitempty" json:"max_rounds,omitempty"`
	MaxToolCalls       int      `yaml:"max_tool_calls,omitempty" json:"max_tool_calls,omitempty"`
	MaxInputTokens     int      `yaml:"max_input_tokens,omitempty" json:"max_input_tokens,omitempty"`
	MaxOutputTokens    int      `yaml:"max_output_tokens,omitempty" json:"max_output_tokens,omitempty"`
	MaxContextTokens   int      `yaml:"max_context_tokens,omitempty" json:"max_context_tokens,omitempty"`
	MaxToolOutputBytes int      `yaml:"max_tool_output_bytes,omitempty" json:"max_tool_output_bytes,omitempty"`
	Timeout            Duration `yaml:"timeout" json:"timeout"`
	// TimeoutSec is the legacy alias accepted by the specification. Normalize
	// folds it into Timeout.
	TimeoutSec int `yaml:"timeout_sec,omitempty" json:"timeout_sec,omitempty"`

	// Permissions narrows the tool grant.
	Permissions Permissions `yaml:"permissions,omitempty" json:"permissions,omitzero"`

	// Team fields, reserved for a later release.
	Strategy string   `yaml:"strategy,omitempty" json:"strategy,omitempty"`
	Members  []string `yaml:"members,omitempty" json:"members,omitempty"`
}

// Security holds workspace and secret handling policy.
type Security struct {
	// AllowOutsideWorkspace permits tools to touch paths outside the
	// workspace root. It stays false by default.
	AllowOutsideWorkspace bool `yaml:"allow_outside_workspace" json:"allow_outside_workspace"`
	// FollowSymlinks allows repository tools to traverse symlinks. It stays
	// false by default so a symlink cannot escape the workspace.
	FollowSymlinks bool `yaml:"follow_symlinks" json:"follow_symlinks"`
	// MaxFileSize caps how many bytes repo.read returns.
	MaxFileSize int64 `yaml:"max_file_size" json:"max_file_size"`
	// WorkspaceRoot overrides the workspace root. Empty means the current
	// working directory or the workspace passed by the host.
	WorkspaceRoot string `yaml:"workspace_root,omitempty" json:"workspace_root,omitempty"`
	// RedactEnv lists glob patterns for environment variables that must never
	// reach a model.
	RedactEnv []string `yaml:"redact_env" json:"redact_env"`
	// AllowedEnv lists environment variable names that may be exposed when an
	// agent explicitly asks for them. It is empty by default.
	AllowedEnv []string `yaml:"allowed_env,omitempty" json:"allowed_env,omitempty"`
	// BlockedPaths lists path globs that repository tools must refuse even
	// inside the workspace, for example "*/.env".
	BlockedPaths []string `yaml:"blocked_paths,omitempty" json:"blocked_paths,omitempty"`
}

// Sessions controls session persistence.
type Sessions struct {
	// Directory is the root of the session store. Supports "~".
	Directory string `yaml:"directory" json:"directory,omitempty"`
	// PersistMessages writes messages.jsonl. Unset means true.
	PersistMessages *bool `yaml:"persist_messages" json:"persist_messages,omitempty"`
	// PersistToolCalls writes tools.jsonl. Unset means true.
	PersistToolCalls *bool `yaml:"persist_tool_calls" json:"persist_tool_calls,omitempty"`
	// Retention caps how many sessions stay on disk. Zero means unlimited.
	Retention int `yaml:"retention,omitempty" json:"retention,omitempty"`
}

// ShouldPersistMessages reports whether messages.jsonl must be written.
func (s Sessions) ShouldPersistMessages() bool {
	return s.PersistMessages == nil || *s.PersistMessages
}

// ShouldPersistToolCalls reports whether tools.jsonl must be written.
func (s Sessions) ShouldPersistToolCalls() bool {
	return s.PersistToolCalls == nil || *s.PersistToolCalls
}

// Telemetry controls logging and metrics export.
type Telemetry struct {
	// Enabled turns on local metrics collection.
	Enabled bool `yaml:"enabled" json:"enabled,omitempty"`
	// LogLevel is debug, info, warn or error.
	LogLevel string `yaml:"log_level" json:"log_level,omitempty"`
	// LogFormat is text or json.
	LogFormat string `yaml:"log_format" json:"log_format,omitempty"`
	// OTLPEndpoint is reserved for the OpenTelemetry exporter.
	OTLPEndpoint string `yaml:"otlp_endpoint,omitempty" json:"otlp_endpoint,omitempty"`
}

// KnownProviderTypes returns every provider type this build understands.
func KnownProviderTypes() []string {
	return []string{
		ProviderTypeOpenAIResponses,
		ProviderTypeOpenAIChat,
		ProviderTypeAnthropic,
		ProviderTypeOpenAICompat,
		ProviderTypeOllama,
		ProviderTypeGemini,
		ProviderTypeBedrock,
	}
}

// PlannedProviderTypes returns provider types that are documented on the
// roadmap but not implemented yet. Validation accepts them with a warning so
// that a forward looking configuration file still loads.
func PlannedProviderTypes() []string {
	return []string{ProviderTypeGemini, ProviderTypeBedrock}
}

// IsKnownProviderType reports whether a provider type is understood.
func IsKnownProviderType(providerType string) bool {
	for _, known := range KnownProviderTypes() {
		if known == providerType {
			return true
		}
	}
	return false
}

// IsPlannedProviderType reports whether a provider type is reserved for a
// future release.
func IsPlannedProviderType(providerType string) bool {
	for _, planned := range PlannedProviderTypes() {
		if planned == providerType {
			return true
		}
	}
	return false
}

// ProviderNeedsBaseURL reports whether a provider type cannot fall back to a
// vendor default endpoint.
func ProviderNeedsBaseURL(providerType string) bool {
	switch providerType {
	case ProviderTypeOpenAICompat, ProviderTypeBedrock:
		return true
	default:
		return false
	}
}

// ProviderNeedsCredential reports whether a provider type normally requires an
// API key. Ollama and self-hosted compatible endpoints may run without one.
func ProviderNeedsCredential(providerType string) bool {
	switch providerType {
	case ProviderTypeOpenAIResponses, ProviderTypeOpenAIChat, ProviderTypeAnthropic,
		ProviderTypeGemini, ProviderTypeBedrock:
		return true
	default:
		return false
	}
}

// KnownToolNames lists the tools shipped by the built-in tool runtime. The list
// mirrors the memory of the specification; the runtime registry added in a
// later phase is the authoritative source and validation only warns on names
// that are outside this set.
func KnownToolNames() []string {
	return []string{
		"repo.read",
		"repo.list",
		"repo.search",
		"repo.stat",
		"git.diff",
		"git.show",
		"git.status",
		"git.log",
	}
}

// IsKnownToolName reports whether a tool name is part of the built-in runtime.
func IsKnownToolName(name string) bool {
	for _, known := range KnownToolNames() {
		if known == name {
			return true
		}
	}
	return false
}
