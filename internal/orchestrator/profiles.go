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
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pawagents/pawagents/internal/agent"
	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/security"
	"github.com/pawagents/pawagents/internal/tools/builtin"
	"github.com/pawagents/pawagents/prompts"
)

// Agent types. Only a single agent is executed by this release; the team type
// exists so that a configuration written for the multi agent roadmap loads and
// is reported as not runnable instead of being silently ignored.
const (
	// TypeSingle is one agent, one model, one task.
	TypeSingle = config.AgentTypeSingle
	// TypeTeam is a group of agents, reserved for a later release.
	TypeTeam = config.AgentTypeTeam
)

// AgentTypes lists every agent type the configuration accepts.
func AgentTypes() []string { return []string{TypeSingle, TypeTeam} }

// Executable reports whether this release can run an agent type.
func Executable(agentType string) bool {
	trimmed := strings.TrimSpace(agentType)
	return trimmed == "" || trimmed == TypeSingle
}

// PromptSource kinds.
const (
	// PromptFile means the instruction text was read from a file.
	PromptFile = "file"
	// PromptBundled means the configured file was missing and the copy
	// embedded in the binary was used.
	PromptBundled = "bundled"
	// PromptInline means the instructions were written in the configuration.
	PromptInline = "inline"
	// PromptNone means the agent has no instructions at all.
	PromptNone = "none"
)

// PromptSource describes where the instruction text of an agent comes from.
//
// It is reported because the three cases behave differently for an operator:
// a bundled prompt means the agent is running with text the user did not read,
// and an empty prompt means the model is answering without any guidance.
type PromptSource struct {
	Kind  string `json:"kind"`
	Path  string `json:"path,omitempty"`
	Bytes int    `json:"bytes,omitempty"`
}

// ToolGrant is one tool an agent may use.
type ToolGrant struct {
	// Name is the tool name as written in the configuration.
	Name string `json:"name"`
	// Known reports whether the built-in runtime provides it. An unknown name
	// fails a run, so it is surfaced by `pagent agent show` instead.
	Known bool `json:"known"`
}

// BudgetView is the resolved budget of an agent.
type BudgetView struct {
	MaxRounds          int    `json:"max_rounds"`
	MaxToolCalls       int    `json:"max_tool_calls"`
	MaxInputTokens     int    `json:"max_input_tokens,omitempty"`
	MaxOutputTokens    int    `json:"max_output_tokens,omitempty"`
	MaxContextTokens   int    `json:"max_context_tokens,omitempty"`
	MaxToolOutputBytes int    `json:"max_tool_output_bytes"`
	Timeout            string `json:"timeout"`
}

// PermissionsView is the resolved permission set of an agent.
type PermissionsView struct {
	Filesystem string   `json:"filesystem"`
	Shell      string   `json:"shell"`
	Allow      []string `json:"allow,omitempty"`
	Deny       []string `json:"deny,omitempty"`
}

// AgentProfile is the full description of a configured agent.
//
// Everything here is derived from the configuration and the filesystem only:
// describing a profile never contacts a provider, so it is safe on a diagnostic
// path. Capability checking is a separate step, in CapabilityReport.
type AgentProfile struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Type is the configured agent type.
	Type string `json:"type"`
	// Executable reports whether this release can run the agent.
	Executable bool `json:"executable"`
	// ModelAlias is the configured model alias.
	ModelAlias string `json:"model"`
	// Targets are the provider targets of the alias, primary first.
	Targets     []config.ModelRef `json:"targets,omitempty"`
	Prompt      PromptSource      `json:"prompt"`
	Tools       []ToolGrant       `json:"tools,omitempty"`
	OutputMode  string            `json:"output_mode"`
	Budget      BudgetView        `json:"budget"`
	Permissions PermissionsView   `json:"permissions"`
	// Warnings are diagnostics attached by the caller, for example the
	// validation warnings of the configuration.
	Warnings []string `json:"warnings,omitempty"`
}

// Known reports whether every granted tool exists in the built-in runtime.
func (p AgentProfile) Known() bool {
	for _, tool := range p.Tools {
		if !tool.Known {
			return false
		}
	}
	return true
}

// UnknownTools returns the granted tool names the runtime does not provide.
func (p AgentProfile) UnknownTools() []string {
	var out []string
	for _, tool := range p.Tools {
		if !tool.Known {
			out = append(out, tool.Name)
		}
	}
	return out
}

// Profiles describes every configured agent, sorted by name.
func (o *Orchestrator) Profiles() []AgentProfile {
	names := o.cfg.AgentNames()
	out := make([]AgentProfile, 0, len(names))
	for _, name := range names {
		profile, err := o.Profile(name)
		if err != nil {
			continue
		}
		out = append(out, profile)
	}
	return out
}

// Profile describes one configured agent.
func (o *Orchestrator) Profile(name string) (AgentProfile, error) {
	agentCfg, err := o.agentConfig(name)
	if err != nil {
		return AgentProfile{}, err
	}

	_, source, err := resolveSystemPrompt(agentCfg)
	if err != nil {
		// A missing prompt is reported by validation as a warning; describing
		// the agent must still work, so the source is reported as empty.
		source = PromptSource{Kind: PromptNone, Path: agentCfg.Prompt}
	}

	profile := AgentProfile{
		Name:        strings.TrimSpace(name),
		Description: agentCfg.Description,
		Type:        agentTypeOf(agentCfg),
		Executable:  Executable(agentTypeOf(agentCfg)),
		ModelAlias:  agentCfg.Model,
		Prompt:      source,
		Tools:       grantedTools(agentCfg.Tools),
		OutputMode:  outputModeOf(agentCfg, ""),
		Budget:      describeBudget(agentCfg),
		Permissions: PermissionsView{
			Filesystem: defaultString(agentCfg.Permissions.Filesystem, security.FilesystemRead),
			Shell:      defaultString(agentCfg.Permissions.Shell, security.ShellDeny),
			Allow:      agentCfg.Permissions.Allow,
			Deny:       agentCfg.Permissions.Deny,
		},
	}

	if targets, _, err := o.cfg.ModelTargets(agentCfg.Model); err == nil {
		profile.Targets = targets
	}

	return profile, nil
}

// agentTypeOf returns the configured agent type, defaulting to single.
func agentTypeOf(agentCfg *config.Agent) string {
	if agentCfg == nil {
		return TypeSingle
	}
	trimmed := strings.TrimSpace(agentCfg.Type)
	if trimmed == "" {
		return TypeSingle
	}
	return trimmed
}

// describeBudget resolves the budget an agent would run with.
//
// The static numbers are reported as configured and defaulted; the context
// window may still be clamped to what the model actually offers, which is what
// CapabilityReport reports.
func describeBudget(agentCfg *config.Agent) BudgetView {
	toolOutputBytes := agentCfg.MaxToolOutputBytes
	if toolOutputBytes <= 0 {
		toolOutputBytes = config.DefaultMaxToolOutputBytes
	}

	view := BudgetView{
		MaxRounds:          agentCfg.MaxRounds,
		MaxToolCalls:       agentCfg.MaxToolCalls,
		MaxInputTokens:     agentCfg.MaxInputTokens,
		MaxOutputTokens:    agentCfg.MaxOutputTokens,
		MaxContextTokens:   agentCfg.MaxContextTokens,
		MaxToolOutputBytes: toolOutputBytes,
	}
	if timeout := agentCfg.Timeout.Duration(); timeout > 0 {
		view.Timeout = timeout.String()
	}
	return view
}

// grantedTools describes the tool grant of an agent, sorted by name.
func grantedTools(names []string) []ToolGrant {
	if len(names) == 0 {
		return nil
	}

	known := make(map[string]bool)
	for _, name := range builtin.ToolNames() {
		known[name] = true
	}

	out := make([]ToolGrant, 0, len(names))
	for _, name := range names {
		out = append(out, ToolGrant{Name: name, Known: known[name]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// resolveSystemPrompt reads the instruction text of an agent and reports where
// it came from.
//
// A prompt file is loaded from disk, and if it is missing the bundled copy of
// the same name is used instead. That fallback matters on a machine where the
// configuration was copied between users: the agent keeps working with the
// prompt it was written for instead of running without any instruction at all.
func resolveSystemPrompt(agentCfg *config.Agent) (string, PromptSource, error) {
	if agentCfg == nil {
		return "", PromptSource{Kind: PromptNone}, nil
	}

	if inline := strings.TrimSpace(agentCfg.Instructions); inline != "" {
		return inline, PromptSource{Kind: PromptInline, Bytes: len(inline)}, nil
	}

	path := strings.TrimSpace(agentCfg.Prompt)
	if path == "" {
		return "", PromptSource{Kind: PromptNone}, nil
	}

	if data, err := os.ReadFile(path); err == nil {
		text := strings.TrimSpace(string(data))
		return text, PromptSource{Kind: PromptFile, Path: path, Bytes: len(text)}, nil
	}

	if bundled, err := prompts.Read(filepath.Base(path)); err == nil {
		text := strings.TrimSpace(bundled)
		return text, PromptSource{Kind: PromptBundled, Path: path, Bytes: len(text)}, nil
	}

	return "", PromptSource{Kind: PromptNone, Path: path}, apperrors.New(
		apperrors.KindConfig, "orchestrator.prompt",
		"the prompt file %s does not exist and no built-in prompt of that name is bundled", path)
}

// loadSystemPrompt returns the instruction text of an agent, or an error when
// the configured prompt resolves to nothing.
func loadSystemPrompt(agentCfg *config.Agent) (string, error) {
	text, _, err := resolveSystemPrompt(agentCfg)
	return text, err
}

// defaultString returns a value or a fallback when it is empty.
func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// runtimeProfile converts a description into the runtime view of the agent.
func runtimeProfile(profile AgentProfile, systemPrompt string, grant *security.Grant) agent.Profile {
	return agent.Profile{
		Name:         profile.Name,
		Description:  profile.Description,
		ModelAlias:   profile.ModelAlias,
		SystemPrompt: systemPrompt,
		OutputMode:   profile.OutputMode,
		Tools:        grant.Names(),
		Grant:        grant,
	}
}
