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

// Package config defines the PawAgents configuration schema together with the
// loading, defaulting and validation pipeline.
//
// The pipeline is deliberately split into independent steps so that each one
// can be tested and reported on its own:
//
//	Read     -> parse YAML strictly (unknown keys are errors)
//	Defaults -> fill every unset knob with a documented value
//	Normalize-> expand "~", collapse the short model form, fold legacy fields
//	Validate -> report errors and warnings as structured entries
//
// Only Read can fail; Validate always returns a report so that
// `pagent config validate` can show every problem at once instead of stopping
// at the first one.
package config

import (
	"fmt"
	"sort"
	"strings"
)

// Config is the root of the configuration document loaded from
// ~/.pawagents/config.yaml.
type Config struct {
	Version   int                  `yaml:"version" json:"version"`
	Providers map[string]*Provider `yaml:"providers" json:"providers,omitempty"`
	Models    map[string]*Model    `yaml:"models" json:"models,omitempty"`
	Agents    map[string]*Agent    `yaml:"agents" json:"agents,omitempty"`
	Security  Security             `yaml:"security" json:"security,omitempty"`
	Sessions  Sessions             `yaml:"sessions" json:"sessions,omitempty"`
	Telemetry Telemetry            `yaml:"telemetry" json:"telemetry,omitempty"`

	// Path is the file this configuration was read from. It is not part of
	// the on-disk schema and is never marshalled.
	Path string `yaml:"-" json:"-"`
}

// ProviderNames returns the provider names sorted alphabetically.
func (c *Config) ProviderNames() []string { return sortedKeys(c.Providers) }

// ModelNames returns the model alias names sorted alphabetically.
func (c *Config) ModelNames() []string { return sortedKeys(c.Models) }

// AgentNames returns the agent names sorted alphabetically.
func (c *Config) AgentNames() []string { return sortedKeys(c.Agents) }

// Provider returns a provider by name.
func (c *Config) Provider(name string) (*Provider, error) {
	provider, ok := c.Providers[name]
	if !ok {
		return nil, fmt.Errorf("provider %q is not defined", name)
	}
	return provider, nil
}

// Model returns a model alias by name.
func (c *Config) Model(name string) (*Model, error) {
	model, ok := c.Models[name]
	if !ok {
		return nil, fmt.Errorf("model %q is not defined", name)
	}
	return model, nil
}

// Agent returns an agent profile by name.
func (c *Config) Agent(name string) (*Agent, error) {
	agent, ok := c.Agents[name]
	if !ok {
		return nil, fmt.Errorf("agent %q is not defined", name)
	}
	return agent, nil
}

// ModelTargets resolves a model alias into its ordered provider targets.
func (c *Config) ModelTargets(name string) ([]ModelRef, *Model, error) {
	model, err := c.Model(name)
	if err != nil {
		return nil, nil, err
	}
	return model.Targets(), model, nil
}

// EnabledProviders returns the provider names that are not marked disabled.
func (c *Config) EnabledProviders() []string {
	out := make([]string, 0, len(c.Providers))
	for name, provider := range c.Providers {
		if provider != nil && !provider.Disabled {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Normalize applies the transformations that must happen before validation:
// path expansion, short-form collapse and legacy field folding.
func (c *Config) Normalize() {
	c.Version = normalizeVersion(c.Version)

	for _, provider := range c.Providers {
		if provider == nil {
			continue
		}
		provider.BaseURL = strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
		provider.APIKeyEnv = strings.TrimSpace(provider.APIKeyEnv)
		provider.TLS.CACertFile = ExpandPath(provider.TLS.CACertFile)
		provider.TLS.ClientCertFile = ExpandPath(provider.TLS.ClientCertFile)
		provider.TLS.ClientKeyFile = ExpandPath(provider.TLS.ClientKeyFile)
	}

	for _, model := range c.Models {
		if model == nil {
			continue
		}
		if model.Primary == nil && (model.Provider != "" || model.Model != "") {
			model.Primary = &ModelRef{
				Provider: strings.TrimSpace(model.Provider),
				Model:    strings.TrimSpace(model.Model),
			}
		}
		if model.Primary != nil {
			model.Primary.Provider = strings.TrimSpace(model.Primary.Provider)
			model.Primary.Model = strings.TrimSpace(model.Primary.Model)
			// The short form is folded into Primary, which becomes the single
			// canonical representation. Clearing the aliases keeps
			// `pagent config show` free of duplicated provider/model pairs.
			model.Provider = ""
			model.Model = ""
		}
		for i := range model.Fallback {
			model.Fallback[i].Provider = strings.TrimSpace(model.Fallback[i].Provider)
			model.Fallback[i].Model = strings.TrimSpace(model.Fallback[i].Model)
		}
	}

	for _, agent := range c.Agents {
		if agent == nil {
			continue
		}
		agent.Model = strings.TrimSpace(agent.Model)
		agent.Prompt = ExpandPath(strings.TrimSpace(agent.Prompt))
		if agent.OutputMode == "" {
			agent.OutputMode = OutputModeStructured
		}
		foldLegacyTimeout(agent)
		if agent.Permissions.IsZero() {
			agent.Permissions = Permissions{Filesystem: PermissionRead, Shell: PermissionDeny}
		}
		if agent.Permissions.Filesystem == "" {
			agent.Permissions.Filesystem = PermissionRead
		}
		if agent.Permissions.Shell == "" {
			agent.Permissions.Shell = PermissionDeny
		}
	}

	c.Security.WorkspaceRoot = ExpandPath(c.Security.WorkspaceRoot)
	c.Sessions.Directory = ExpandPath(c.Sessions.Directory)
}

// String renders a short human description used by `pagent config show`.
func (c *Config) String() string {
	return fmt.Sprintf("config{version=%d providers=%d models=%d agents=%d}",
		c.Version, len(c.Providers), len(c.Models), len(c.Agents))
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
