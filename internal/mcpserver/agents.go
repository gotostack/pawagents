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

package mcpserver

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/orchestrator"
)

// AgentSummary is one entry of the list_agents payload.
//
// The field names are part of the contract a host reads: name, description and
// model are the three a host needs to decide whether to delegate, and the rest
// saves a second round trip for the common questions.
type AgentSummary struct {
	// Name is the agent name to pass to get_agent and delegate_task.
	Name string `json:"name"`
	// Description is the one line purpose of the agent.
	Description string `json:"description,omitempty"`
	// Model is the configured model alias, not a provider model name: the alias
	// is stable, the model behind it is not.
	Model string `json:"model"`
	// Type is single or team. Only a single agent runs in this release.
	Type string `json:"type"`
	// Executable reports whether this release can run the agent.
	Executable bool `json:"executable"`
	// Tools lists the tools the agent is granted.
	Tools []string `json:"tools,omitempty"`
	// OutputMode is text or structured.
	OutputMode string `json:"output_mode"`
	// Ready reports whether the model behind the alias can run the agent, judged
	// from the configuration without contacting the provider.
	Ready bool `json:"ready"`
	// Confirmed reports whether Ready rests on evidence: the endpoint was asked,
	// or the provider contract documents the capability. An unconfirmed verdict
	// comes from a conservative default, so a false Ready means "unknown", not
	// "unsuitable", and the runtime verifies it when a task starts.
	Confirmed bool `json:"confirmed"`
	// Why explains a false Ready in one line.
	Why string `json:"why,omitempty"`
}

// AgentTarget is one provider target of an agent model alias.
type AgentTarget struct {
	// Target is "provider/model".
	Target string `json:"target"`
	// ProviderType is the wire protocol of the provider.
	ProviderType string `json:"provider_type,omitempty"`
	// Fallback reports whether the target is only used when an earlier one
	// fails.
	Fallback bool `json:"fallback,omitempty"`
}

// AgentBudget is the resolved budget of an agent.
//
// It is reported because a host cannot invent a deadline it does not know: the
// round and timeout limits belong to the agent, and a delegation may only
// narrow them.
type AgentBudget struct {
	MaxRounds    int    `json:"max_rounds"`
	MaxToolCalls int    `json:"max_tool_calls"`
	Timeout      string `json:"timeout"`
}

// AgentPermissions is the resolved permission set of an agent.
type AgentPermissions struct {
	Filesystem string   `json:"filesystem"`
	Shell      string   `json:"shell"`
	Allow      []string `json:"allow,omitempty"`
	Deny       []string `json:"deny,omitempty"`
}

// AgentDetail is the get_agent payload.
type AgentDetail struct {
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Model        string   `json:"model"`
	Type         string   `json:"type"`
	Executable   bool     `json:"executable"`
	Tools        []string `json:"tools"`
	UnknownTools []string `json:"unknown_tools,omitempty"`
	OutputMode   string   `json:"output_mode"`

	// Targets are the provider targets of the alias, primary first.
	Targets []AgentTarget `json:"targets,omitempty"`

	Budget      AgentBudget      `json:"budget"`
	Permissions AgentPermissions `json:"permissions"`

	// Prompt describes where the instruction text comes from: file, bundled,
	// inline or none.
	Prompt string `json:"prompt"`

	// Ready reports whether the agent can run, and Confirmed whether that
	// verdict is backed by evidence rather than by a conservative default. A
	// ready but unconfirmed agent may still run: only the endpoint knows what a
	// local model supports, so the runtime asks it when a task starts.
	Ready     bool `json:"ready"`
	Confirmed bool `json:"confirmed"`
	// Why explains a false Ready in one line.
	Why string `json:"why,omitempty"`

	// Missing lists the capabilities the model does not provide.
	Missing []string `json:"missing,omitempty"`
	// Skipped lists the targets that were passed over, with the reason.
	Skipped []orchestrator.SkippedTarget `json:"skipped,omitempty"`

	// Warnings are configuration diagnostics attached to the agent.
	Warnings []string `json:"warnings,omitempty"`
}

// ListAgentsOutput is the list_agents payload.
//
// The agents are wrapped in an object rather than returned as a bare array
// because the root of a structured output schema has to be a JSON object: a Go
// slice produces a root type of ["null","array"], and a host that validates
// the schema it is handed rejects the tool. Claude Code does exactly that and
// reports "Invalid input (at tools.N.outputSchema.type)", which drops the whole
// server's tool list.
type ListAgentsOutput struct {
	Agents []AgentSummary `json:"agents"`
}

// listAgentsInput is intentionally empty: listing agents takes no arguments, so
// that a host can always ask what is available.
type listAgentsInput struct{}

// getAgentInput names the agent to describe.
type getAgentInput struct {
	Name string `json:"name" jsonschema:"the agent name as reported by list_agents"`
}

// registerAgentTools adds list_agents and get_agent to the server.
func (s *Server) registerAgentTools() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name: ToolListAgents,
		Description: "List the agent profiles this server can delegate to, with the model " +
			"alias, the tool grant and a readiness verdict. ready means the runtime expects " +
			"the agent to run; confirmed means that verdict rests on the provider contract or " +
			"a probe, so ready false with confirmed false means the capabilities are unknown " +
			"rather than missing, and the agent may still run.",
		Annotations: readOnlyAnnotations("List agents"),
	}, s.listAgents)

	mcp.AddTool(s.server, &mcp.Tool{
		Name: ToolGetAgent,
		Description: "Describe one agent in detail: its model targets, tool grant, budget, " +
			"permissions and whether its model can satisfy the agent requirements. Use it " +
			"before delegating when a run must not fail for a configuration reason.",
		Annotations: readOnlyAnnotations("Describe an agent"),
	}, s.getAgent)
}

// listAgents implements the list_agents tool.
func (s *Server) listAgents(ctx context.Context, _ *mcp.CallToolRequest, _ listAgentsInput) (*mcp.CallToolResult, ListAgentsOutput, error) {
	profiles := s.runtime.Profiles()

	summaries := make([]AgentSummary, 0, len(profiles))
	for _, profile := range profiles {
		summaries = append(summaries, s.summarize(ctx, profile))
	}
	return nil, ListAgentsOutput{Agents: summaries}, nil
}

// getAgent implements the get_agent tool.
func (s *Server) getAgent(ctx context.Context, _ *mcp.CallToolRequest, input getAgentInput) (*mcp.CallToolResult, AgentDetail, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, AgentDetail{}, failure(apperrors.New(apperrors.KindInvalidArgument, "mcpserver.get_agent",
			"an agent name is required"))
	}

	profile, err := s.runtime.Profile(name)
	if err != nil {
		return nil, AgentDetail{}, failure(err)
	}

	// The report is built without a probe: a host that asks what an agent can do
	// should not have to wait for, or depend on, a model endpoint answering.
	report, err := s.runtime.CheckAgent(ctx, profile.Name, false)
	if err != nil {
		return nil, AgentDetail{}, failure(err)
	}
	report.Agent = profile.Name

	return nil, s.describeAgent(profile, report), nil
}

// summarize builds the list_agents entry of a profile.
//
// The readiness of every agent is resolved here even though the listing could
// omit it: the check is offline, it costs one configuration lookup per agent,
// and a host that delegates to an agent which cannot run wastes a whole task.
func (s *Server) summarize(ctx context.Context, profile orchestrator.AgentProfile) AgentSummary {
	summary := AgentSummary{
		Name:        profile.Name,
		Description: profile.Description,
		Model:       profile.ModelAlias,
		Type:        profile.Type,
		Executable:  profile.Executable,
		Tools:       toolNames(profile),
		OutputMode:  profile.OutputMode,
	}

	report, err := s.runtime.CheckAgent(ctx, profile.Name, false)
	switch {
	case err != nil:
		summary.Why = apperrors.Summary(err)
	case !profile.Executable:
		summary.Why = "only single agents run in this release"
	case !report.OK:
		summary.Confirmed, summary.Why = verdict(report)
	default:
		summary.Ready = true
		summary.Confirmed, summary.Why = verdict(report)
	}
	return summary
}

// verdict reports how much a capability report can be trusted, and explains it.
//
// The distinction matters to a host: "this model cannot call tools" and "only
// the endpoint knows whether this model can call tools" call for opposite
// decisions, and a host that cannot tell them apart will refuse to delegate to
// local models that would have worked.
func verdict(report orchestrator.CapabilityReport) (bool, string) {
	if report.Confirmed() {
		return true, explainMissing(report)
	}
	if report.OK {
		// Usable, but the verdict is a conservative default rather than a
		// finding, which Confirmed reports on its own.
		return false, ""
	}
	return false, "only the endpoint knows whether this model provides " +
		strings.Join(report.Missing, ", ") + "; the runtime asks it when a task runs"
}

// explainMissing describes an unfulfilled requirement of an authoritative
// verdict.
func explainMissing(report orchestrator.CapabilityReport) string {
	if report.OK {
		return ""
	}
	if len(report.Missing) > 0 {
		return "the model does not provide " + strings.Join(report.Missing, ", ")
	}
	if report.Error != "" {
		return report.Error
	}
	return "the agent cannot run"
}

// describeAgent builds the get_agent payload.
func (s *Server) describeAgent(profile orchestrator.AgentProfile, report orchestrator.CapabilityReport) AgentDetail {
	ready := report.OK && profile.Executable
	confirmed, why := verdict(report)
	if !profile.Executable {
		why = "only single agents run in this release"
	} else if !ready && why == "" {
		why = describeNotReady(report)
	}

	detail := AgentDetail{
		Name:         profile.Name,
		Description:  profile.Description,
		Model:        profile.ModelAlias,
		Type:         profile.Type,
		Executable:   profile.Executable,
		Tools:        toolNames(profile),
		UnknownTools: profile.UnknownTools(),
		OutputMode:   profile.OutputMode,
		Targets:      s.agentTargets(profile),
		Budget: AgentBudget{
			MaxRounds:    profile.Budget.MaxRounds,
			MaxToolCalls: profile.Budget.MaxToolCalls,
			Timeout:      profile.Budget.Timeout,
		},
		Permissions: AgentPermissions{
			Filesystem: profile.Permissions.Filesystem,
			Shell:      profile.Permissions.Shell,
			Allow:      profile.Permissions.Allow,
			Deny:       profile.Permissions.Deny,
		},
		Prompt:    profile.Prompt.Kind,
		Missing:   report.Missing,
		Skipped:   report.Skipped,
		Warnings:  profile.Warnings,
		Ready:     report.OK && profile.Executable,
		Confirmed: confirmed,
		Why:       why,
	}
	return detail
}

// describeNotReady explains a target that cannot serve the task at all.
func describeNotReady(report orchestrator.CapabilityReport) string {
	if report.Error != "" {
		return report.Error
	}
	if report.Status != "" {
		return "no usable target: " + report.Status
	}
	return "the agent cannot run"
}

// toolNames returns the granted tool names in configuration order.
func toolNames(profile orchestrator.AgentProfile) []string {
	names := make([]string, 0, len(profile.Tools))
	for _, tool := range profile.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// agentTargets converts the model targets of a profile.
//
// The order is the resolution order of the alias, primary first, which is what
// a host needs to understand a fallback.
func (s *Server) agentTargets(profile orchestrator.AgentProfile) []AgentTarget {
	targets := make([]AgentTarget, 0, len(profile.Targets))
	for index, target := range profile.Targets {
		targets = append(targets, AgentTarget{
			Target:       target.String(),
			ProviderType: s.providerType(target.Provider),
			Fallback:     index > 0,
		})
	}
	return targets
}

// providerType reports the wire protocol of a configured provider, which is
// what tells a host whether a target is a local model or a remote endpoint.
func (s *Server) providerType(name string) string {
	if s.cfg == nil {
		return ""
	}
	provider, ok := s.cfg.Providers[name]
	if !ok || provider == nil {
		return ""
	}
	return provider.Type
}
