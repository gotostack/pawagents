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

package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/pawagents/pawagents/internal/agent"
	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/orchestrator"
	"github.com/pawagents/pawagents/internal/provider"

	// Register the provider implementations compiled into this binary.
	_ "github.com/pawagents/pawagents/internal/provider/all"
)

// newRunCmd implements `pagent run`.
//
// The command is the first place where a whole task is executed end to end: it
// resolves the configuration, runs one agent and prints the structured result.
// Everything it does beyond that is presentation, including the exit code,
// which is derived from the classified error so that a script can tell a
// budget failure from a denied tool.
func newRunCmd(app *App) *cobra.Command {
	var (
		agentName   string
		task        string
		workspace   string
		background  string
		files       []string
		constraints []string
		maxRounds   int
		timeout     time.Duration
		output      string
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Delegate one task to a configured agent",
		Long: `Run one task with a configured agent and print the structured result.

The agent reads the workspace with the tools its profile grants and reports
findings; it never modifies files. The result carries a status, a summary, the
findings and what the task consumed, so the caller can decide what to do next.

    pagent run --agent local-reviewer --task "Review my current git changes"

Pass "-" as the task to read the instruction from standard input, and use
--file and --constraint to give the agent starting points and rules.`,
		Example: `  # Review the current changes with the local Ollama agent
  pagent run --agent local-reviewer --task "Review current git changes"

  # Review one file and ask for machine readable output
  pagent run --agent local-reviewer \
      --task "Review this file" \
      --file internal/agent/loop.go \
      --constraint "Focus on concurrency" \
      --output json

  # A longer budget for a deep review
  pagent run --agent local-reviewer --task "Audit the tool runtime" \
      --max-rounds 40 --timeout 10m`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The output format is validated before the task starts: a typo in
			// a flag must not cost a provider call and a workspace scan.
			if output != "text" && output != "json" {
				return apperrors.New(apperrors.KindInvalidArgument, "cli.run",
					"unsupported output format %q (want text or json)", output)
			}

			instruction, err := resolveTask(cmd.InOrStdin(), task)
			if err != nil {
				return err
			}

			cfg, err := app.Config()
			if err != nil {
				return err
			}

			registry := provider.NewDefaultRegistry(cfg, app.Logger())
			defer func() { _ = registry.Close() }()

			orchestratorInstance, err := orchestrator.New(cfg, registry, app.Logger())
			if err != nil {
				return err
			}

			result, runErr := orchestratorInstance.Run(cmd.Context(), orchestrator.Request{
				Agent:       agentName,
				Task:        instruction,
				Background:  background,
				Files:       files,
				Constraints: constraints,
				Workspace:   workspace,
				MaxRounds:   maxRounds,
				Timeout:     timeout,
				OutputMode:  "",
			})

			if result == nil {
				return runErr
			}

			if err := writeResult(cmd.OutOrStdout(), result, output); err != nil {
				return err
			}

			// The report above already carries the failure, so the error
			// returned here only has to classify the exit status.
			if runErr != nil {
				return apperrors.New(failureKind(result.Status, runErr), "cli.run",
					"agent %q reported %s", result.Agent, result.Status)
			}
			return nil
		},
	}

	flags := cmd.Flags()
	flags.StringVarP(&agentName, "agent", "a", "", "agent to run (required)")
	flags.StringVarP(&task, "task", "t", "", "task instruction, or \"-\" for standard input")
	flags.StringVarP(&workspace, "workspace", "w", "", "workspace directory (default: the working directory)")
	flags.StringVar(&background, "background", "", "background the agent cannot discover for itself")
	flags.StringArrayVarP(&files, "file", "f", nil, "file the agent should start from (repeatable)")
	flags.StringArrayVarP(&constraints, "constraint", "c", nil, "rule the answer must respect (repeatable)")
	flags.IntVar(&maxRounds, "max-rounds", 0, "override the agent round budget")
	flags.DurationVar(&timeout, "timeout", 0, "override the agent timeout, for example 10m")
	flags.StringVarP(&output, "output", "o", "text", "output format: text or json")

	_ = cmd.MarkFlagRequired("agent")

	return cmd
}

// resolveTask returns the instruction, reading standard input for "-".
func resolveTask(stdin io.Reader, task string) (string, error) {
	if strings.TrimSpace(task) != "-" {
		if strings.TrimSpace(task) == "" {
			return "", apperrors.New(apperrors.KindInvalidArgument, "cli.run",
				"a task is required: pass --task \"...\" or --task - to read it from standard input")
		}
		return task, nil
	}

	data, err := io.ReadAll(stdin)
	if err != nil {
		return "", apperrors.Wrap(apperrors.KindInvalidArgument, "cli.run",
			"cannot read the task from standard input", err)
	}
	instruction := strings.TrimSpace(string(data))
	if instruction == "" {
		return "", apperrors.New(apperrors.KindInvalidArgument, "cli.run",
			"the task read from standard input is empty")
	}
	return instruction, nil
}

// writeResult renders a result in the requested format.
func writeResult(out io.Writer, result *agent.Result, output string) error {
	switch output {
	case "text":
		_, err := fmt.Fprint(out, result.RenderText())
		if err != nil {
			return apperrors.Wrap(apperrors.KindInternal, "cli.run",
				"cannot write the result", err)
		}
		return nil
	case "json":
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(result); err != nil {
			return apperrors.Wrap(apperrors.KindInternal, "cli.run",
				"cannot encode the result", err)
		}
		return nil
	default:
		return apperrors.New(apperrors.KindInvalidArgument, "cli.run",
			"unsupported output format %q (want text or json)", output)
	}
}

// failureKind maps a failed task to the error kind that produces the right exit
// status.
func failureKind(status string, cause error) apperrors.Kind {
	switch status {
	case agent.StatusBudgetExceeded:
		return apperrors.KindBudgetExceeded
	case agent.StatusTimeout:
		return apperrors.KindTimeout
	case agent.StatusCancelled:
		return apperrors.KindCancelled
	}

	// A plain failure keeps the cause's own classification: a denied tool must
	// exit with the permission status, not with a generic agent failure.
	if cause != nil {
		if kind := apperrors.KindOf(cause); kind != apperrors.KindInternal {
			return kind
		}
	}
	return apperrors.KindAgent
}
