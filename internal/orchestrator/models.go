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
	"strings"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/llm"
	"github.com/pawagents/pawagents/internal/provider"
)

// ModelTarget is one provider target of a model alias.
//
// A target is described even when it cannot be used, because the reason is what
// an operator needs: a typo in a provider name, a provider type this build does
// not implement, or an endpoint that is switched off are three different fixes.
type ModelTarget struct {
	// Provider is the configured provider name.
	Provider string `json:"provider"`
	// ProviderType is the wire protocol of that provider.
	ProviderType string `json:"provider_type,omitempty"`
	// Model is the concrete model name the provider serves.
	Model string `json:"model"`
	// Fallback marks a target that is only tried when the primary fails.
	Fallback bool `json:"fallback,omitempty"`
	// Status is the shared provider status: ready, planned, unavailable or
	// disabled.
	Status string `json:"status"`
	// Capabilities are the effective static capabilities. They come from the
	// static table for the provider type, corrected by the alias configuration;
	// a probe replaces them with what the endpoint reports.
	Capabilities *llm.ModelCapabilities `json:"capabilities,omitempty"`
	// Probed reports whether Capabilities came from the endpoint.
	Probed bool `json:"probed,omitempty"`
	// Error explains why the target cannot be used, when it cannot.
	Error string `json:"error,omitempty"`
}

// ModelEntry describes one configured model alias.
type ModelEntry struct {
	// Alias is the configured model alias.
	Alias string `json:"alias"`
	// Targets are the provider targets, primary first.
	Targets []ModelTarget `json:"targets"`
	// Agents are the agents bound to this alias.
	Agents []string `json:"agents,omitempty"`
	// Temperature overrides the provider default when set.
	Temperature *float64 `json:"temperature,omitempty"`
	// Status is the status of the first usable target, or of the primary.
	Status string `json:"status"`
	// Ready reports whether at least one target can serve a request.
	Ready bool `json:"ready"`
}

// Target returns the primary target.
func (e ModelEntry) Target() (ModelTarget, bool) {
	if len(e.Targets) == 0 {
		return ModelTarget{}, false
	}
	return e.Targets[0], true
}

// Models describes every configured model alias, sorted by name.
func (o *Orchestrator) Models() []ModelEntry {
	names := o.cfg.ModelNames()
	out := make([]ModelEntry, 0, len(names))
	for _, name := range names {
		entry, err := o.Model(name)
		if err != nil {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// Model describes one configured model alias without contacting a provider.
func (o *Orchestrator) Model(alias string) (ModelEntry, error) {
	trimmed := strings.TrimSpace(alias)
	modelCfg, ok := o.cfg.Models[trimmed]
	if !ok || modelCfg == nil {
		return ModelEntry{}, apperrors.New(apperrors.KindNotFound, "orchestrator.model",
			"model alias %q is not defined in the configuration", trimmed).
			WithDetails(map[string]any{"available": o.cfg.ModelNames()})
	}

	entry := ModelEntry{
		Alias:       trimmed,
		Temperature: modelCfg.Temperature,
		Agents:      o.agentsForModel(trimmed),
		Status:      provider.StatusUnavailable,
	}

	for index, target := range modelCfg.Targets() {
		described := ModelTarget{
			Provider: target.Provider,
			Model:    target.Model,
			Fallback: index > 0,
		}

		cfgProvider, exists := o.cfg.Providers[target.Provider]
		switch {
		case !exists:
			described.Status = provider.StatusUnavailable
			described.Error = "provider " + target.Provider + " is not defined"
		default:
			described.ProviderType = cfgProvider.Type
			described.Status = o.registry.Status(cfgProvider)
			if described.Status != provider.StatusReady {
				described.Error = describeUnavailableStatus(cfgProvider, described.Status)
			}
			capabilities := provider.Resolve(cfgProvider.Type, modelCfg, nil)
			described.Capabilities = &capabilities
		}

		if index == 0 || (!entry.Ready && described.Status == provider.StatusReady) {
			entry.Status = described.Status
		}
		if described.Status == provider.StatusReady {
			entry.Ready = true
		}

		entry.Targets = append(entry.Targets, described)
	}

	return entry, nil
}

// ProbeModel fills every target with what the endpoint reports.
//
// Targets are probed independently and a failure is recorded per target rather
// than returned: an alias whose primary is down but whose fallback answers is
// still usable, and collapsing that into one error would hide the good target.
//
// The entry is copied before it is filled in. A struct is passed by value but
// its slices are not, and a probe that quietly rewrote the caller's entry — or
// the entry another caller is still holding — would be a bug that only shows up
// as confusing state somewhere else.
func (o *Orchestrator) ProbeModel(ctx context.Context, entry ModelEntry) ModelEntry {
	entry.Targets = append([]ModelTarget(nil), entry.Targets...)

	// A nil model configuration is a supported input: the alias may have been
	// described by a caller that holds no configuration, and Resolve documents
	// that it applies whatever the alias declares when one is present.
	modelCfg := o.cfg.Models[entry.Alias]

	for index, target := range entry.Targets {
		if target.Status != provider.StatusReady {
			continue
		}

		instance, err := o.registry.Provider(ctx, target.Provider)
		if err != nil {
			entry.Targets[index].Error = err.Error()
			continue
		}

		if err := probeReachability(ctx, instance); err != nil {
			entry.Targets[index].Error = err.Error()
			continue
		}

		probe, err := instance.Capabilities(ctx, target.Model)
		if err != nil {
			entry.Targets[index].Error = err.Error()
			continue
		}

		capabilities := provider.Resolve(instance.Type(), modelCfg, &probe)
		entry.Targets[index].Capabilities = &capabilities
		entry.Targets[index].Probed = true
		entry.Targets[index].Error = ""
	}

	return entry
}

// agentsForModel returns the agents bound to a model alias, sorted.
func (o *Orchestrator) agentsForModel(alias string) []string {
	var out []string
	for _, name := range o.cfg.AgentNames() {
		agentCfg := o.cfg.Agents[name]
		if agentCfg != nil && agentCfg.Model == alias {
			out = append(out, name)
		}
	}
	return out
}

// describeUnavailableStatus explains a status that is not ready.
func describeUnavailableStatus(cfgProvider *config.Provider, status string) string {
	switch status {
	case provider.StatusDisabled:
		return "the provider is disabled in the configuration"
	case provider.StatusPlanned:
		return "provider type " + cfgProvider.Type + " is planned but not implemented yet"
	default:
		return "no implementation of " + cfgProvider.Type + " is compiled into this build"
	}
}
