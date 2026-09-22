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
	"log/slog"
	"strings"

	"github.com/pawagents/pawagents/internal/agent"
	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/security"
	"github.com/pawagents/pawagents/internal/tools"
	"github.com/pawagents/pawagents/internal/tools/builtin"
)

// Run resolves a request, configures an agent and runs one task.
//
// The result is always returned when the agent loop started, even if the task
// failed, so that a host sees the status, the usage and the error together. A
// setup failure — an unknown agent, an unresolvable model, a capability the
// model does not have — returns an error and no result, because there is no
// task outcome to report.
func (o *Orchestrator) Run(ctx context.Context, request Request) (*agent.Result, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}

	agentCfg, err := o.agentConfig(request.Agent)
	if err != nil {
		return nil, err
	}

	// The profile is resolved first: an agent this release cannot execute, or
	// one granting a tool the runtime does not have, must fail before a
	// provider is contacted.
	profile, err := o.Profile(request.Agent)
	if err != nil {
		return nil, err
	}
	if !profile.Executable {
		return nil, apperrors.New(apperrors.KindCapability, "orchestrator.run",
			"agent %q is of type %q, and only %q agents run in this release",
			profile.Name, profile.Type, TypeSingle)
	}
	if unknown := profile.UnknownTools(); len(unknown) > 0 {
		return nil, apperrors.New(apperrors.KindCapability, "orchestrator.run",
			"agent %q grants unknown tool(s): %s",
			profile.Name, strings.Join(unknown, ", ")).
			WithDetails(map[string]any{"unknown": unknown, "available": builtin.ToolNames()})
	}

	resolved, err := o.resolve(ctx, agentCfg.Model, resolveOptions{})
	if err != nil {
		return nil, err
	}

	if err := validateAgent(agentCfg, resolved); err != nil {
		return nil, err
	}

	systemPrompt, err := loadSystemPrompt(agentCfg)
	if err != nil {
		return nil, err
	}

	budget := budgetFor(agentCfg, request, resolved.Capabilities)

	grant, err := security.NewGrantForToolset(agentCfg.Tools,
		agentCfg.Permissions.Allow, agentCfg.Permissions.Deny,
		agentCfg.Permissions.Filesystem, agentCfg.Permissions.Shell)
	if err != nil {
		return nil, err
	}

	executor, workspace, err := o.buildExecutor(grant, budget, request.Workspace)
	if err != nil {
		return nil, err
	}
	defer func() {
		if workspace != nil {
			_ = workspace.Close()
		}
	}()

	profile.OutputMode = outputModeOf(agentCfg, request.OutputMode)
	runtime := runtimeProfile(profile, systemPrompt, grant)

	runner, err := agent.NewRunner(agent.RunnerOptions{
		Profile:         runtime,
		Model:           resolved.Provider,
		ModelName:       resolved.Model,
		Capabilities:    resolved.Capabilities,
		Executor:        executor,
		Budget:          budget,
		MaxOutputTokens: outputTokenLimit(resolved.Capabilities),
		Temperature:     temperatureOf(resolved.ModelConfig),
		Logger:          o.logger,
	})
	if err != nil {
		return nil, err
	}

	o.logger.Debug("running agent",
		slog.String("agent", runtime.Name),
		slog.String("provider", resolved.ProviderName),
		slog.String("model", resolved.Model),
		slog.Int("max_rounds", budget.MaxRounds),
		slog.String("timeout", budget.Timeout.String()))

	task := agent.Task{
		Instruction: request.Task,
		Background:  request.Background,
		Files:       request.Files,
		Constraints: request.Constraints,
	}

	result, err := runner.Run(ctx, task)
	if result != nil {
		result.Provider = resolved.ProviderName
		result.Model = resolved.Model
	}
	return result, err
}

// agentConfig looks up an agent and reports a helpful error when it is missing.
func (o *Orchestrator) agentConfig(name string) (*config.Agent, error) {
	trimmed := strings.TrimSpace(name)
	agentCfg, ok := o.cfg.Agents[trimmed]
	if !ok || agentCfg == nil {
		return nil, apperrors.New(apperrors.KindNotFound, "orchestrator.agent",
			"agent %q is not defined in the configuration", trimmed).
			WithDetails(map[string]any{"available": o.cfg.AgentNames()})
	}
	return agentCfg, nil
}

// buildExecutor prepares the tool runtime for a task.
func (o *Orchestrator) buildExecutor(grant *security.Grant, budget agent.Budget, requested string) (*tools.Executor, *security.Workspace, error) {
	workspace, err := o.workspaceFor(requested)
	if err != nil {
		return nil, nil, err
	}

	// The environment cap and the budget cap are the same number: the tool
	// runtime truncates as early as possible, and the loop checks the result
	// again so that a tool that ignores the environment cannot blow up the
	// context window.
	environment, err := builtin.Environment(workspace, budget.MaxToolOutputBytes, o.logger)
	if err != nil {
		_ = workspace.Close()
		return nil, nil, err
	}

	registry := builtin.NewRegistry()
	return tools.NewExecutor(registry, grant, environment), workspace, nil
}

// temperatureOf reads the sampling temperature configured for an alias.
func temperatureOf(model *config.Model) *float64 {
	if model == nil || model.Temperature == nil {
		return nil
	}
	value := *model.Temperature
	return &value
}
