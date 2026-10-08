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
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pawagents/pawagents/internal/agent"
	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/orchestrator"
)

// TimeoutSec bounds a delegation timeout. It exists so that a host cannot ask
// for a delegation that never ends: the value is a ceiling, not a default.
const maxTimeoutSec = 3600

// DelegateTaskInput is the delegation contract.
//
// It is deliberately not a single prompt. A delegated task needs a question, a
// place to look, the background the agent cannot discover for itself, the rules
// the answer must respect, and a budget; a host that can only send a prompt has
// no way to state any of that, and the agent then guesses.
type DelegateTaskInput struct {
	// Agent is the name of a configured agent, as reported by list_agents.
	Agent string `json:"agent" jsonschema:"the agent to delegate to, as reported by list_agents"`
	// Task is the question, stated as an instruction.
	Task string `json:"task" jsonschema:"the task, stated as a bounded instruction for the agent"`
	// Workspace is the directory the agent may read. It must stay inside the
	// directories the deployment allows. Empty uses the configured default.
	Workspace string `json:"workspace,omitempty" jsonschema:"the workspace directory the agent may read; must be an allowed directory"`
	// Background is context the agent cannot discover by reading files.
	Background string `json:"background,omitempty" jsonschema:"context the agent cannot discover for itself, such as the intent of a change"`
	// Files are starting points for the investigation.
	Files []string `json:"files,omitempty" jsonschema:"files the agent should start from, relative to the workspace"`
	// Constraints are rules the answer must respect.
	Constraints []string `json:"constraints,omitempty" jsonschema:"rules the answer must respect, for example an output language"`
	// OutputMode is text or structured. Empty uses the agent configuration.
	OutputMode string `json:"output_mode,omitempty" jsonschema:"text or structured; empty uses the agent default"`
	// Budget may narrow the agent budget. It can never widen it.
	Budget *DelegateBudget `json:"budget,omitempty" jsonschema:"optional limits that narrow the agent budget"`
}

// DelegateBudget narrows the budget of one delegation.
type DelegateBudget struct {
	// MaxRounds caps the model turns. Zero uses the agent value.
	MaxRounds int `json:"max_rounds,omitempty" jsonschema:"maximum model turns; zero uses the agent value"`
	// TimeoutSec caps the wall clock time. Zero uses the agent value.
	TimeoutSec int `json:"timeout_sec,omitempty" jsonschema:"maximum duration in seconds; zero uses the agent value"`
}

// DelegateTaskOutput is the outcome of a delegation.
//
// The uniform envelope is embedded rather than nested, so the payload a host
// reads is exactly the structured result documented for every agent, provider
// and model, with the classification of a failed task alongside it.
type DelegateTaskOutput struct {
	agent.Result

	// ErrorKind classifies a failed task, for example BudgetExceededError. It is
	// set only when the task did not complete.
	ErrorKind string `json:"error_kind,omitempty"`
}

// registerDelegateTool adds delegate_task to the server.
func (s *Server) registerDelegateTool() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name: ToolDelegateTask,
		Description: "Delegate one bounded task to a named agent running on its own model and " +
			"return a structured result: status, summary, findings, usage and the session id. " +
			"The agent is read-only and its findings are advisory. Verify critical findings " +
			"before modifying files. Give one task at a time and state the workspace it may read.",
		Annotations: readOnlyAnnotations("Delegate a task"),
	}, s.delegateTask)
}

// delegateTask implements the delegate_task tool.
//
// Three outcomes are possible and they are reported differently on purpose:
//
//   - the task ran and completed: the result envelope;
//   - the task ran and failed (a budget, a timeout, a provider failure): the
//     same envelope marked as an error, because the partial answer and the
//     session id are still worth having;
//   - the task never started (an unknown agent, a refused workspace): a
//     classified error and no envelope, because there is nothing to report.
func (s *Server) delegateTask(ctx context.Context, _ *mcp.CallToolRequest, input DelegateTaskInput) (*mcp.CallToolResult, DelegateTaskOutput, error) {
	request, err := s.buildRequest(input)
	if err != nil {
		return nil, DelegateTaskOutput{}, failure(err)
	}

	s.logger.Info("delegating a task",
		"agent", request.Agent,
		"workspace", request.Workspace,
		"max_rounds", request.MaxRounds)

	result, runErr := s.runtime.Run(ctx, request)
	if result == nil {
		// Nothing ran, so the failure is the whole answer.
		return nil, DelegateTaskOutput{}, failure(orInternal(runErr, "the delegation produced no result"))
	}

	output := DelegateTaskOutput{Result: *result}
	output.ErrorKind = string(apperrors.KindOf(runErr))
	if runErr == nil && !completedStatus(result.Status) {
		// A failure without a classified error still has a status that says what
		// happened, and a host branching on the kind must not see an empty one.
		output.ErrorKind = string(kindForStatus(result.Status))
	}
	output.Provider = result.Provider
	output.SessionID = result.SessionID

	s.logger.Info("delegation finished",
		"agent", result.Agent,
		"status", result.Status,
		"session", result.SessionID,
		"rounds", result.Usage.Rounds)

	if output.ErrorKind == "" {
		return nil, output, nil
	}

	// The envelope is the payload, but the call failed: the flag is what stops a
	// host from reading a budget failure as a finished analysis.
	failure := &mcp.CallToolResult{}
	failure.SetError(apperrors.New(apperrors.KindOf(runErr), "mcpserver.delegate_task",
		"agent %q reported %s", result.Agent, result.Status))
	return failure, output, nil
}

// buildRequest validates the delegation contract and maps it onto a runtime
// request.
//
// Validation happens here rather than in the runtime because the answers differ
// for a host: an unknown agent, an unsupported output mode and an oversized
// timeout are usage mistakes that must be refused before a task is started and
// a budget is spent.
func (s *Server) buildRequest(input DelegateTaskInput) (orchestrator.Request, error) {
	request := orchestrator.Request{
		Agent:       strings.TrimSpace(input.Agent),
		Task:        strings.TrimSpace(input.Task),
		Background:  input.Background,
		Files:       input.Files,
		Constraints: input.Constraints,
		Workspace:   strings.TrimSpace(input.Workspace),
		OutputMode:  strings.TrimSpace(input.OutputMode),
	}

	if request.Agent == "" {
		return request, apperrors.New(apperrors.KindInvalidArgument, "mcpserver.delegate_task",
			"an agent is required; call list_agents to see what is available")
	}
	if request.Task == "" {
		return request, apperrors.New(apperrors.KindInvalidArgument, "mcpserver.delegate_task",
			"a task is required")
	}

	// The agent has to exist before the budget of the delegation is applied to
	// it, so that a typo is reported as a typo.
	if _, err := s.runtime.Profile(request.Agent); err != nil {
		return request, err
	}

	switch request.OutputMode {
	case "", config.OutputModeText, config.OutputModeStructured:
	default:
		return request, apperrors.New(apperrors.KindInvalidArgument, "mcpserver.delegate_task",
			"unsupported output mode %q (want text or structured)", request.OutputMode)
	}

	if input.Budget != nil {
		if input.Budget.MaxRounds < 0 {
			return request, apperrors.New(apperrors.KindInvalidArgument, "mcpserver.delegate_task",
				"budget.max_rounds must not be negative")
		}
		if input.Budget.TimeoutSec < 0 {
			return request, apperrors.New(apperrors.KindInvalidArgument, "mcpserver.delegate_task",
				"budget.timeout_sec must not be negative")
		}
		if input.Budget.TimeoutSec > maxTimeoutSec {
			return request, apperrors.New(apperrors.KindInvalidArgument, "mcpserver.delegate_task",
				"budget.timeout_sec must not exceed %d seconds", maxTimeoutSec)
		}

		request.MaxRounds = input.Budget.MaxRounds
		if input.Budget.TimeoutSec > 0 {
			request.Timeout = time.Duration(input.Budget.TimeoutSec) * time.Second
		}
	}

	// The runtime validates the rest, including the workspace, against the
	// security configuration and the agent budget.
	if err := request.Validate(); err != nil {
		return request, err
	}
	return request, nil
}

// completedStatus reports whether a result status means the agent answered.
func completedStatus(status string) bool {
	return status == agent.StatusCompleted
}

// kindForStatus classifies a failure that arrived without an error.
func kindForStatus(status string) apperrors.Kind {
	switch status {
	case agent.StatusBudgetExceeded:
		return apperrors.KindBudgetExceeded
	case agent.StatusTimeout:
		return apperrors.KindTimeout
	case agent.StatusCancelled:
		return apperrors.KindCancelled
	case agent.StatusFailed:
		return apperrors.KindAgent
	default:
		return apperrors.KindInternal
	}
}

// orInternal keeps a failure message from being lost when the runtime returned
// neither a result nor a classified error.
func orInternal(err error, message string) error {
	if err == nil {
		return apperrors.New(apperrors.KindInternal, "mcpserver.delegate_task", "%s", message)
	}
	return err
}
