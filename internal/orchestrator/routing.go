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
	// Skipped lists the targets that were tried and passed over before this
	// one, so that a fallback is visible in a log, a report or an error.
	Skipped []SkippedTarget
}

// resolveOptions tunes how a model alias is resolved.
type resolveOptions struct {
	// requireReachability asks every target whether it can be reached before
	// its capabilities are accepted.
	//
	// It is set by an explicit probe, where an endpoint that does not answer is
	// the answer, and never by a run: a task must not pay a health round trip
	// and must not be refused because an endpoint that serves generations did
	// not implement a separate probe.
	requireReachability bool
}

// resolve turns a model alias into a provider instance and its capabilities.
//
// Targets are tried in order, which is what makes a configured fallback work:
// a provider that cannot be built, or a model whose capabilities cannot be
// probed, moves the task to the next target instead of failing it. Only when
// every target fails does the error surface, and then it names all of them,
// because "deepseek failed" is far less useful than "deepseek and anthropic
// both failed, with these reasons".
func (o *Orchestrator) resolve(ctx context.Context, alias string, options resolveOptions) (*binding, error) {
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
	skipped := make([]SkippedTarget, 0, len(targets))

	for _, target := range targets {
		attempts = append(attempts, describeTarget(target))

		instance, err := o.registry.Provider(ctx, target.Provider)
		if err != nil {
			problems = append(problems, err)
			skipped = append(skipped, skippedTarget(target, err))
			o.logger.Debug("provider is unavailable",
				slog.String("agent_model", alias),
				slog.String("target", describeTarget(target)),
				slog.String("error", err.Error()))
			continue
		}

		if options.requireReachability {
			if err := probeReachability(ctx, instance); err != nil {
				problems = append(problems, err)
				skipped = append(skipped, skippedTarget(target, err))
				o.logger.Debug("endpoint is not reachable",
					slog.String("agent_model", alias),
					slog.String("target", describeTarget(target)),
					slog.String("error", err.Error()))
				continue
			}
		}

		// Capabilities are probed before the first token is spent: a model
		// that turns out not to support the agent's tools must fail here, not
		// half way through a conversation.
		probe, err := instance.Capabilities(ctx, target.Model)
		if err != nil {
			problems = append(problems, err)
			skipped = append(skipped, skippedTarget(target, err))
			o.logger.Debug("capability probe failed",
				slog.String("agent_model", alias),
				slog.String("target", describeTarget(target)),
				slog.String("error", err.Error()))
			continue
		}

		providerType := instance.Type()
		capabilities := provider.Resolve(providerType, modelCfg, &probe)

		// A task that runs on a fallback is worth saying out loud: the answer
		// came from a different model than the one the agent names first.
		if len(skipped) > 0 {
			o.logger.Warn("using a fallback model target",
				slog.String("model_alias", alias),
				slog.String("target", describeTarget(target)),
				slog.Any("skipped", skipped))
		}

		return &binding{
			Alias:        alias,
			ProviderName: instance.Name(),
			ProviderType: providerType,
			Provider:     instance,
			Model:        target.Model,
			ModelConfig:  modelCfg,
			Capabilities: capabilities,
			Skipped:      skipped,
		}, nil
	}

	return nil, apperrors.Wrap(aggregateKind(problems), "orchestrator.resolve",
		"no provider target of %q could serve the task (tried %s)",
		errors.Join(problems...), alias, describeTargets(targets)).
		WithDetails(map[string]any{
			"model_alias": alias,
			"attempts":    attempts,
		})
}

// aggregateKind classifies a set of target failures.
//
// When every target failed the same way the specific kind is kept, because it
// drives the exit status a host branches on: an expired credential must still
// be an authentication error, and a missing model must still be a not-found
// error, even though the router as a whole failed to find a target. A mixed
// set of failures has no more precise description than "no provider could
// serve this".
func aggregateKind(problems []error) apperrors.Kind {
	kind := apperrors.KindProvider
	for index, problem := range problems {
		current := apperrors.KindOf(problem)
		if index == 0 {
			kind = current
			continue
		}
		if current != kind {
			return apperrors.KindProvider
		}
	}

	if kind == "" || kind == apperrors.KindInternal {
		return apperrors.KindProvider
	}
	return kind
}

// validateAgent checks an agent configuration against the resolved model.
func validateAgent(agentCfg *config.Agent, resolved *binding) error {
	requirements := RequirementsFor(agentCfg)
	return resolved.Capabilities.Validate(requirements, resolved.Model)
}

// skippedTarget records a target that was passed over and why.
func skippedTarget(target config.ModelRef, err error) SkippedTarget {
	reason := "unknown failure"
	if err != nil {
		reason = apperrors.Summary(err)
	}
	return SkippedTarget{Target: describeTarget(target), Reason: reason}
}

// describeSkipped renders skipped targets for a result envelope.
func describeSkipped(skipped []SkippedTarget) []string {
	if len(skipped) == 0 {
		return nil
	}

	out := make([]string, 0, len(skipped))
	for _, target := range skipped {
		if target.Reason == "" {
			out = append(out, target.Target)
			continue
		}
		out = append(out, target.Target+" ("+target.Reason+")")
	}
	return out
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

// probeReachability asks a provider whether it can be reached.
//
// The capability contract deliberately lets a provider degrade to its static
// table when an endpoint does not answer, because a capability answer is more
// useful to a run than a refusal. An explicit probe is the opposite case: the
// user asked what the endpoint supports, so an endpoint that does not answer
// has to be reported as a failure rather than dressed up as a static answer.
func probeReachability(ctx context.Context, instance provider.Provider) error {
	if reporter, ok := instance.(provider.HealthReporter); ok {
		health, err := reporter.Health(ctx)
		if err != nil {
			return err
		}
		if !health.Reachable {
			return apperrors.New(apperrors.KindProvider, "orchestrator.probe",
				"the endpoint of provider %q did not answer: %s", instance.Name(), health.Detail)
		}
		return nil
	}

	// Without a health probe the model listing is the only reachability signal
	// a provider offers, and asking for it twice is free: it is cached.
	if lister, ok := instance.(provider.ModelLister); ok {
		if _, err := lister.ListModels(ctx); err != nil {
			return err
		}
	}
	return nil
}
