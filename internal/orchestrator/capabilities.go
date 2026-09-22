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

// RequirementsFor returns what an agent needs from a model.
//
// This is the single source of truth for capability requirements: the agent
// loop refuses to start when they are not met, `pagent agent show` reports
// them, and `pagent provider test` checks them. Deriving them twice is how a
// runtime and its documentation end up disagreeing.
//
// Structured output is deliberately not a requirement. The runtime asks for a
// provider enforced schema only when the model supports one and otherwise
// describes the envelope in the prompt, so requiring it would reject every
// local model that cannot enforce a schema while still answering in JSON.
func RequirementsFor(agentCfg *config.Agent) llm.Requirements {
	requirements := llm.Requirements{}
	if agentCfg == nil {
		return requirements
	}

	if len(agentCfg.Tools) > 0 {
		requirements.Tools = true
	}
	if agentCfg.Prompt != "" || agentCfg.Instructions != "" {
		requirements.SystemMessage = true
	}
	if agentCfg.MaxContextTokens > 0 {
		requirements.MinContextTokens = agentCfg.MaxContextTokens
	}

	return requirements
}

// RequirementsForAlias merges the requirements of every agent bound to a model
// alias, which is what a single provider check has to satisfy.
func RequirementsForAlias(cfg *config.Config, alias string) llm.Requirements {
	merged := llm.Requirements{}
	if cfg == nil {
		return merged
	}

	for _, name := range cfg.AgentNames() {
		agentCfg := cfg.Agents[name]
		if agentCfg == nil || agentCfg.Model != alias {
			continue
		}

		requirements := RequirementsFor(agentCfg)
		merged.Tools = merged.Tools || requirements.Tools
		merged.SystemMessage = merged.SystemMessage || requirements.SystemMessage
		if requirements.MinContextTokens > merged.MinContextTokens {
			merged.MinContextTokens = requirements.MinContextTokens
		}
	}

	return merged
}

// SkippedTarget records a provider target that was passed over, and why.
//
// A fallback that saves a task is worth seeing: a report that only names the
// target that answered hides the fact that the primary is broken.
type SkippedTarget struct {
	// Target is "provider/model".
	Target string `json:"target"`
	// Reason is the short explanation, for example a missing implementation or
	// an unreachable endpoint.
	Reason string `json:"reason,omitempty"`
}

// CapabilityReport explains whether a model can run an agent.
//
// It answers the question a user actually has when a run refuses to start:
// which requirement is not met, by which model, and what to change. Every field
// is reported even on success, so the same structure documents a working setup.
type CapabilityReport struct {
	// Agent is the agent the report is about.
	Agent string `json:"agent"`
	// ModelAlias is the configured model alias.
	ModelAlias string `json:"model"`
	// Provider and Model name the target that would serve the task.
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model_name,omitempty"`
	// ProviderType is the wire protocol of that provider.
	ProviderType string `json:"provider_type,omitempty"`
	// Status is the target status: ready, planned, unavailable or disabled.
	Status string `json:"status"`
	// Probed reports whether the capabilities came from the endpoint.
	Probed bool `json:"probed"`
	// OK reports whether the agent can run against this model.
	OK bool `json:"ok"`
	// Requirements is what the agent needs.
	Requirements llm.Requirements `json:"requirements"`
	// Capabilities is what the model provides.
	Capabilities *llm.ModelCapabilities `json:"capabilities,omitempty"`
	// Missing lists the unmet requirements by name.
	Missing []string `json:"missing,omitempty"`
	// Skipped lists the targets that were passed over before this one.
	Skipped []SkippedTarget `json:"skipped,omitempty"`
	// Error explains a failure that is not a missing capability, for example
	// an endpoint that did not answer.
	Error string `json:"error,omitempty"`
}

// CheckAgent reports whether an agent can run against its model.
//
// Without probe the answer comes from the configuration and the static
// capability table, so the check is offline and fast. With probe the endpoint
// is asked what the model supports, which is the only way to know whether a
// local model has tool calling.
//
// The report is returned even when the probe failed: a failed probe explains
// something useful, and the caller usually wants to print it before exiting.
func (o *Orchestrator) CheckAgent(ctx context.Context, name string, probe bool) (CapabilityReport, error) {
	agentCfg, err := o.agentConfig(name)
	if err != nil {
		return CapabilityReport{}, err
	}

	report := CapabilityReport{
		Agent:        strings.TrimSpace(name),
		ModelAlias:   agentCfg.Model,
		Status:       provider.StatusUnavailable,
		Requirements: RequirementsFor(agentCfg),
	}

	if probe {
		return o.probeAgent(ctx, report, agentCfg)
	}
	return o.describeAgent(ctx, report, agentCfg), nil
}

// describeAgent fills the report from the configuration alone.
func (o *Orchestrator) describeAgent(ctx context.Context, report CapabilityReport, agentCfg *config.Agent) CapabilityReport {
	entry, err := o.Model(agentCfg.Model)
	if err != nil {
		report.Error = apperrors.Summary(err)
		return report
	}

	report.Status = entry.Status

	// The report describes the target that would actually serve the task, so a
	// usable fallback is reported as usable. Saying "unavailable" because the
	// primary is broken would be wrong when a run would succeed through the
	// fallback.
	target, skipped, ok := firstReadyTarget(entry.Targets)
	report.Skipped = skipped

	if !ok {
		primary, _ := entry.Target()
		report.Provider = primary.Provider
		report.ProviderType = primary.ProviderType
		report.Model = primary.Model
		report.Capabilities = primary.Capabilities
		report.Error = primary.Error
		if report.Error == "" {
			report.Error = "no provider target of " + entry.Alias + " is usable in this build"
		}
		return report
	}

	report.Provider = target.Provider
	report.ProviderType = target.ProviderType
	report.Model = target.Model
	report.Capabilities = target.Capabilities

	if report.Capabilities != nil {
		report.Missing = report.Capabilities.Missing(report.Requirements)
		report.OK = len(report.Missing) == 0
		if !report.OK {
			report.Error = apperrors.Summary(
				report.Capabilities.Validate(report.Requirements, report.Model))
		}
	}

	_ = ctx
	return report
}

// firstReadyTarget returns the first target a run would use, together with the
// targets it would skip on the way.
func firstReadyTarget(targets []ModelTarget) (ModelTarget, []SkippedTarget, bool) {
	skipped := make([]SkippedTarget, 0, len(targets))

	for _, target := range targets {
		if target.Status == provider.StatusReady {
			return target, skipped, true
		}
		reason := target.Error
		if reason == "" {
			reason = "status " + target.Status
		}
		skipped = append(skipped, SkippedTarget{
			Target: target.Provider + "/" + target.Model,
			Reason: reason,
		})
	}

	if len(skipped) == 0 {
		skipped = nil
	}
	return ModelTarget{}, skipped, false
}

// probeAgent fills the report from the endpoint.
func (o *Orchestrator) probeAgent(ctx context.Context, report CapabilityReport, agentCfg *config.Agent) (CapabilityReport, error) {
	resolved, err := o.resolve(ctx, agentCfg.Model, resolveOptions{requireReachability: true})
	if err != nil {
		report.Error = apperrors.Summary(err)
		return report, err
	}

	report.Provider = resolved.ProviderName
	report.ProviderType = resolved.ProviderType
	report.Model = resolved.Model
	report.Probed = true
	report.Capabilities = &resolved.Capabilities
	report.Status = provider.StatusReady
	report.Skipped = resolved.Skipped

	report.Missing = resolved.Capabilities.Missing(report.Requirements)
	report.OK = len(report.Missing) == 0
	if !report.OK {
		report.Error = apperrors.Summary(
			resolved.Capabilities.Validate(report.Requirements, resolved.Model))
	}

	return report, nil
}

// CheckModel reports whether every agent bound to a model alias can run.
func (o *Orchestrator) CheckModel(ctx context.Context, alias string, probe bool) ([]CapabilityReport, error) {
	if _, err := o.Model(alias); err != nil {
		return nil, err
	}

	agents := o.agentsForModel(alias)
	reports := make([]CapabilityReport, 0, len(agents))

	for _, name := range agents {
		report, err := o.CheckAgent(ctx, name, probe)
		if err != nil && !probe {
			return nil, err
		}
		reports = append(reports, report)

		// A probe failure is reported in the report itself; the caller decides
		// whether an unreachable endpoint is fatal for the command.
		if err != nil {
			return reports, err
		}
	}

	return reports, nil
}
