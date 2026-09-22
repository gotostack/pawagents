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

	"gopkg.in/yaml.v3"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/security"
)

// Redacted returns a deep copy of the configuration with every credential
// replaced by a placeholder. `pagent config show` uses it so that a screen
// share or a bug report never leaks an inline API key or a custom
// Authorization header.
//
// The redaction patterns come from security.redact_env, which means an operator
// can extend them for a custom gateway.
func (c *Config) Redacted() *Config {
	if c == nil {
		return nil
	}
	patterns := c.Security.RedactEnv

	out := &Config{
		Version:   c.Version,
		Path:      c.Path,
		Security:  cloneSecurity(c.Security),
		Sessions:  cloneSessions(c.Sessions),
		Telemetry: c.Telemetry,
	}

	out.Providers = make(map[string]*Provider, len(c.Providers))
	for name, provider := range c.Providers {
		if provider == nil {
			out.Providers[name] = nil
			continue
		}
		clone := *provider
		if clone.APIKey != "" {
			clone.APIKey = security.RedactedPlaceholder
		}
		clone.Headers = security.RedactStringMap(provider.Headers, patterns)
		clone.ExtraBody = security.RedactAnyMap(provider.ExtraBody, patterns)
		out.Providers[name] = &clone
	}

	out.Models = make(map[string]*Model, len(c.Models))
	for name, model := range c.Models {
		if model == nil {
			out.Models[name] = nil
			continue
		}
		clone := *model
		clone.Extra = security.RedactAnyMap(model.Extra, patterns)
		if model.Primary != nil {
			primary := *model.Primary
			clone.Primary = &primary
		}
		if len(model.Fallback) > 0 {
			clone.Fallback = append([]ModelRef(nil), model.Fallback...)
		}
		if model.Capabilities != nil {
			capabilities := *model.Capabilities
			clone.Capabilities = &capabilities
		}
		out.Models[name] = &clone
	}

	out.Agents = make(map[string]*Agent, len(c.Agents))
	for name, agent := range c.Agents {
		if agent == nil {
			out.Agents[name] = nil
			continue
		}
		clone := *agent
		clone.Tools = append([]string(nil), agent.Tools...)
		clone.Permissions = clonePermissions(agent.Permissions)
		clone.Members = append([]string(nil), agent.Members...)
		out.Agents[name] = &clone
	}

	return out
}

// YAMLDocument renders the configuration as YAML.
func (c *Config) YAMLDocument() ([]byte, error) {
	if c == nil {
		return nil, apperrors.New(apperrors.KindConfig, "config.render", "configuration is nil")
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return nil, apperrors.Wrap(apperrors.KindConfig, "config.render",
			"cannot render the configuration as YAML", err)
	}
	return data, nil
}

// JSONDocument renders the configuration as indented JSON.
func (c *Config) JSONDocument() ([]byte, error) {
	if c == nil {
		return nil, apperrors.New(apperrors.KindConfig, "config.render", "configuration is nil")
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, apperrors.Wrap(apperrors.KindConfig, "config.render",
			"cannot render the configuration as JSON", err)
	}
	return append(data, '\n'), nil
}

func cloneSecurity(in Security) Security {
	out := in
	out.RedactEnv = append([]string(nil), in.RedactEnv...)
	out.AllowedEnv = append([]string(nil), in.AllowedEnv...)
	out.BlockedPaths = append([]string(nil), in.BlockedPaths...)
	return out
}

func cloneSessions(in Sessions) Sessions {
	out := in
	if in.PersistMessages != nil {
		value := *in.PersistMessages
		out.PersistMessages = &value
	}
	if in.PersistToolCalls != nil {
		value := *in.PersistToolCalls
		out.PersistToolCalls = &value
	}
	return out
}

func clonePermissions(in Permissions) Permissions {
	out := in
	out.Allow = append([]string(nil), in.Allow...)
	out.Deny = append([]string(nil), in.Deny...)
	return out
}
