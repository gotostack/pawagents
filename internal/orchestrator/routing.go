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
	"errors"
	"log/slog"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/llm"
	"github.com/pawagents/pawagents/internal/provider"
)

// binding is a model alias resolved to a live provider and an effective
// capability set.
type binding struct {
	// Alias is the configured model alias.
	Alias string
	// ProviderName is the configured provider name.
	ProviderName string
	// ProviderType is the provider type identifier.
	ProviderType string
	// Provider is the live provider instance.
	Provider provider.Provider
	// Model is the concrete model name.
	Model string
	// ModelConfig is the model configuration, when the alias defines one.
	ModelConfig *config.Model
	// Capabilities is the effective capability set.
	Capabilities llm.ModelCapabilities
	// Attempts lists the targets that were tried before this one, so that a
	// fallback is visible in a log or an error message.
	Attempts []string
}

// resolve turns a model alias into a provider instance and its capabilities.
//
// Targets are tried in order, which is what makes a configured fallback work:
// a provider that cannot be built, or a model whose capabilities cannot be
// probed, moves the task to the next target instead of failing it. Only when
// every target fails does the error surface, and then it names all of them,
// because "deepseek failed" is far less useful than "deepseek and anthropic
// both failed, with these reasons".
func (o *Orchestrator) resolve(ctx context.Context, alias string) (*binding, error) {
	targets, modelCfg, err := o.cfg.ModelTargets(alias)
	if err != nil {
		return nil, apperrors.New(apperrors.KindConfig, "orchestrator.resolve",
			"model alias %q is not defined in the configuration", alias).
			WithDetails(map[string]any{"available": o.cfg.ModelNames()})
	}
	if len(targets) == 0 {
		return nil, apperrors.New(apperrors.KindConfig, "orchestrator.resolve",
			"model alias %q has no provider target", alias)
	}

	var problems []error
	attempts := make([]string, 0, len(targets))

	for _, target := range targets {
		attempts = append(attempts, describeTarget(target))

		instance, err := o.registry.Provider(ctx, target.Provider)
		if err != nil {
			problems = append(problems, err)
			o.logger.Debug("provider is unavailable",
				slog.String("agent_model", alias),
				slog.String("target", describeTarget(target)),
				slog.String("error", err.Error()))
			continue
		}

		// Capabilities are probed before the first token is spent: a model
		// that turns out not to support the agent's tools must fail here, not
		// half way through a conversation.
		probe, err := instance.Capabilities(ctx, target.Model)
		if err != nil {
			problems = append(problems, err)
			o.logger.Debug("capability probe failed",
				slog.String("agent_model", alias),
				slog.String("target", describeTarget(target)),
				slog.String("error", err.Error()))
			continue
		}

		providerType := instance.Type()
		capabilities := provider.Resolve(providerType, modelCfg, &probe)

		return &binding{
			Alias:        alias,
			ProviderName: instance.Name(),
			ProviderType: providerType,
			Provider:     instance,
			Model:        target.Model,
			ModelConfig:  modelCfg,
			Capabilities: capabilities,
			Attempts:     attempts[:len(attempts)-1],
		}, nil
	}

	return nil, apperrors.Wrap(apperrors.KindProvider, "orchestrator.resolve",
		"no provider target of %q could serve the task (tried %s)",
		errors.Join(problems...), alias, describeTargets(targets)).
		WithDetails(map[string]any{
			"model_alias": alias,
			"attempts":    attempts,
		})
}

// requirementsFor collects what an agent needs from a model.
//
// Structured output is deliberately not a requirement: the runtime asks for a
// provider enforced schema only when the model supports one, and otherwise
// instructs the model in the prompt. Requiring it would reject every local
// model that cannot enforce a schema while still answering in JSON.
func requirementsFor(agentCfg *config.Agent, maxOutputTokens int) llm.Requirements {
	requirements := llm.Requirements{
		SystemMessage:   agentCfg.Prompt != "" || agentCfg.Instructions != "",
		MaxOutputTokens: maxOutputTokens,
	}
	if len(agentCfg.Tools) > 0 {
		requirements.Tools = true
	}
	if agentCfg.MaxContextTokens > 0 {
		requirements.MinContextTokens = agentCfg.MaxContextTokens
	}
	return requirements
}

// validateAgent checks an agent configuration against the resolved model.
func validateAgent(agentCfg *config.Agent, resolved *binding) error {
	requirements := requirementsFor(agentCfg, resolved.Capabilities.MaxOutputTokens)
	return resolved.Capabilities.Validate(requirements, resolved.Model)
}

// describeTargets renders a target list for an error message.
func describeTargets(targets []config.ModelRef) string {
	out := ""
	for index, target := range targets {
		if index > 0 {
			out += ", "
		}
		out += describeTarget(target)
	}
	return out
}
