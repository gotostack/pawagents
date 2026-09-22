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
	"os"
	"path/filepath"
	"strings"

	"github.com/pawagents/pawagents/internal/agent"
	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/security"
	"github.com/pawagents/pawagents/internal/tools"
	"github.com/pawagents/pawagents/internal/tools/builtin"
	"github.com/pawagents/pawagents/prompts"
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

	resolved, err := o.resolve(ctx, agentCfg.Model)
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

	profile := agent.Profile{
		Name:         strings.TrimSpace(request.Agent),
		Description:  agentCfg.Description,
		ModelAlias:   agentCfg.Model,
		SystemPrompt: systemPrompt,
		OutputMode:   outputModeOf(agentCfg, request.OutputMode),
		Tools:        grant.Names(),
		Grant:        grant,
	}

	runner, err := agent.NewRunner(agent.RunnerOptions{
		Profile:         profile,
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
		slog.String("agent", profile.Name),
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
		return nil, apperrors.New(apperrors.KindNotFound, "orchestrator.run",
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

// loadSystemPrompt reads the instruction text of an agent.
//
// A prompt file is loaded from disk, and if it is missing the bundled copy of
// the same name is used instead. That fallback matters on a machine where the
// configuration was copied between users: the agent keeps working with the
// prompt it was written for instead of running without any instruction at all.
func loadSystemPrompt(agentCfg *config.Agent) (string, error) {
	if inline := strings.TrimSpace(agentCfg.Instructions); inline != "" {
		return inline, nil
	}

	path := strings.TrimSpace(agentCfg.Prompt)
	if path == "" {
		return "", nil
	}

	data, err := os.ReadFile(path)
	if err == nil {
		return strings.TrimSpace(string(data)), nil
	}

	if bundled, bundledErr := prompts.Read(filepath.Base(path)); bundledErr == nil {
		return strings.TrimSpace(bundled), nil
	}

	return "", apperrors.Wrap(apperrors.KindConfig, "orchestrator.prompt",
		"cannot read the prompt file %s", err, path)
}

// temperatureOf reads the sampling temperature configured for an alias.
func temperatureOf(model *config.Model) *float64 {
	if model == nil || model.Temperature == nil {
		return nil
	}
	value := *model.Temperature
	return &value
}
