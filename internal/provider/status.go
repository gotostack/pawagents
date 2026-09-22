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

package provider

import "github.com/pawagents/pawagents/internal/config"

// Status values describe whether a configured provider can be used by this
// build. They are part of the CLI and MCP output, so the strings are stable.
const (
	// StatusReady means an implementation is compiled into this build.
	StatusReady = "ready"
	// StatusDisabled means the entry is switched off in the configuration.
	StatusDisabled = "disabled"
	// StatusPlanned means the type is documented on the roadmap only.
	StatusPlanned = "planned"
	// StatusUnavailable means the type is known but no implementation is
	// compiled into this build yet.
	StatusUnavailable = "unavailable"
)

// SupportedTypes returns the provider types compiled into this build.
func SupportedTypes() map[string]bool {
	factories := DefaultFactories()
	out := make(map[string]bool, len(factories))
	for providerType := range factories {
		out[providerType] = true
	}
	return out
}

// Status reports whether the default factory set can use a provider
// configuration. Commands that hold a registry should ask the registry
// instead, so that an injected or restricted factory set is described
// accurately.
func Status(cfgProvider *config.Provider) string {
	return statusWith(cfgProvider, SupportedTypes())
}

// Supports reports whether this registry has an implementation for a provider
// type.
func (r *Registry) Supports(providerType string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.factories[providerType]
	return ok
}

// Status reports whether this registry can use a provider configuration.
func (r *Registry) Status(cfgProvider *config.Provider) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	supported := make(map[string]bool, len(r.factories))
	for providerType := range r.factories {
		supported[providerType] = true
	}
	return statusWith(cfgProvider, supported)
}

// statusWith classifies a provider configuration against a factory set.
func statusWith(cfgProvider *config.Provider, supported map[string]bool) string {
	if cfgProvider == nil {
		return StatusUnavailable
	}
	if cfgProvider.Disabled {
		return StatusDisabled
	}
	if supported[cfgProvider.Type] {
		return StatusReady
	}
	if config.IsPlannedProviderType(cfgProvider.Type) {
		return StatusPlanned
	}
	return StatusUnavailable
}

// IsReady reports whether a provider configuration can serve requests.
func IsReady(cfgProvider *config.Provider) bool {
	return Status(cfgProvider) == StatusReady
}
