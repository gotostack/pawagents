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
	"time"

	"github.com/pawagents/pawagents/internal/agent"
	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/security"
	"github.com/pawagents/pawagents/internal/session"
	"github.com/pawagents/pawagents/internal/tools"
	"github.com/pawagents/pawagents/internal/tools/builtin"
	"github.com/pawagents/pawagents/internal/version"
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
	workspacePath := ""
	if workspace != nil {
		workspacePath = workspace.Path()
	}
	defer func() {
		if workspace != nil {
			_ = workspace.Close()
		}
	}()

	profile.OutputMode = outputModeOf(agentCfg, request.OutputMode)
	runtime := runtimeProfile(profile, systemPrompt, grant)

	// The session starts before the first model call, so that a task which is
	// interrupted still leaves its agent, model and workspace on disk. A nil
	// session must stay a nil interface: a typed nil would panic the first time
	// the loop records something.
	record := o.startSession(profile, runtime, budget, workspacePath)
	var recorder agent.Recorder
	if record != nil {
		recorder = record
	}

	runner, err := agent.NewRunner(agent.RunnerOptions{
		Profile:         runtime,
		Model:           resolved.Provider,
		ModelName:       resolved.Model,
		Capabilities:    resolved.Capabilities,
		Executor:        executor,
		Budget:          budget,
		MaxOutputTokens: outputTokenLimit(resolved.Capabilities),
		Temperature:     temperatureOf(resolved.ModelConfig),
		Recorder:        recorder,
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
		result.Skipped = describeSkipped(resolved.Skipped)
		// The identifier is reported to the host, so that the caller can hand
		// it back to `pagent session show` without searching the store.
		if record != nil {
			result.SessionID = record.ID()
		}
	}

	o.finishSession(record, result)
	return result, err
}

// startSession opens the record of a task.
//
// A store that cannot be written is reported and the task runs anyway: losing
// an audit trail is bad, but refusing to answer because a disk is full is
// worse, and the warning says exactly what happened.
func (o *Orchestrator) startSession(
	profile AgentProfile,
	runtime agent.Profile,
	budget agent.Budget,
	workspace string,
) *session.Session {
	if o.sessions == nil {
		return nil
	}

	record, err := o.sessions.Start(session.Meta{
		Agent:      runtime.Name,
		ModelAlias: profile.ModelAlias,
		Workspace:  workspace,
		OutputMode: runtime.OutputMode,
		Status:     session.StatusRunning,
		StartedAt:  time.Now(),
		Version:    version.Info().Version,
		Tools:      runtime.Tools,
		MaxRounds:  budget.MaxRounds,
	})
	if err != nil {
		o.logger.Warn("the task will not be recorded",
			slog.String("agent", runtime.Name),
			slog.String("error", err.Error()))
		return nil
	}

	o.logger.Debug("recording the task",
		slog.String("agent", runtime.Name),
		slog.String("session", record.ID()),
		slog.String("directory", record.Directory()))
	return record
}

// finishSession closes the record with the outcome of the task.
func (o *Orchestrator) finishSession(record *session.Session, result *agent.Result) {
	if record == nil {
		return
	}

	if result != nil {
		record.SetModel(result.Provider, result.Model)
	}
	if err := record.Finish(result); err != nil {
		o.logger.Warn("the session record is incomplete",
			slog.String("session", record.ID()),
			slog.String("error", err.Error()))
	}
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
