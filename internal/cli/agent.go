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
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/llm"
	"github.com/pawagents/pawagents/internal/orchestrator"
	"github.com/pawagents/pawagents/internal/provider"

	// Register the provider implementations compiled into this binary.
	_ "github.com/pawagents/pawagents/internal/provider/all"
)

// newAgentCmd implements the `pagent agent` command group.
func newAgentCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Inspect the configured agent profiles",
		Long: `Inspect the agent profiles a host can delegate to.

An agent profile is the reusable half of a delegation: what the agent is for,
which model alias it uses, which tools it may call, what it may spend and how
long it may run. The profile is decoupled from the model, so switching an alias
from a cloud model to a local one never changes the agent.

Nothing here contacts a provider unless --probe is passed; the capability
report is derived from the configuration and the static capability table.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(newAgentListCmd(app), newAgentShowCmd(app))

	return cmd
}

// newAgentListCmd implements `pagent agent list`.
func newAgentListCmd(app *App) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the configured agents",
		Long: `List every agent defined in the configuration.

The MODEL column is the configured model alias and TARGET the provider and
model it currently resolves to, so a single table answers "which agent runs on
what", which is the question that matters when switching a local model in.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := app.Config()
			if err != nil {
				return err
			}

			orchestratorInstance, err := newOrchestrator(app, cfg)
			if err != nil {
				return err
			}

			profiles := orchestratorInstance.Profiles()

			out := cmd.OutOrStdout()
			switch output {
			case "text":
				if len(profiles) == 0 {
					fmt.Fprintln(out, "no agents configured; add an agents block to the configuration")
					return nil
				}
				writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
				fmt.Fprintln(writer, "AGENT\tTYPE\tMODEL\tTARGET\tTOOLS\tOUTPUT\tROUNDS\tTIMEOUT")
				for _, profile := range profiles {
					fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%d\t%s\t%d\t%s\n",
						profile.Name,
						profile.Type,
						profile.ModelAlias,
						describeTarget(profile),
						len(profile.Tools),
						profile.OutputMode,
						profile.Budget.MaxRounds,
						valueOrDash(profile.Budget.Timeout))
				}
				return writer.Flush()
			case "json":
				if err := encodeJSON(out, "agent.list", profiles); err != nil {
					return err
				}
				return nil
			default:
				return apperrors.New(apperrors.KindInvalidArgument, "agent.list",
					"unsupported output format %q (want text or json)", output)
			}
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "text", "output format: text or json")

	return cmd
}

// newAgentShowCmd implements `pagent agent show`.
func newAgentShowCmd(app *App) *cobra.Command {
	var output string
	var probe bool

	cmd := &cobra.Command{
		Use:   "show <agent>",
		Short: "Describe one agent and whether it can run",
		Long: `Describe one agent: its prompt, its tool grant, its budget, its permissions
and whether its model can do the job.

The capability section is the point of the command. It compares what the agent
needs (tool calling when it grants tools, a system message when it has a
prompt, a context window large enough for its budget) with what its model
provides, and names every missing capability instead of letting a run fail
half way through.

With --probe the endpoint is asked what the model actually supports, which is
the only way to know whether a local model has tool calling. Without it the
report uses the static capability table and stays offline.`,
		Example: `  pagent agent show local-reviewer
  pagent agent show local-reviewer --probe --output json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := app.Config()
			if err != nil {
				return err
			}

			orchestratorInstance, err := newOrchestrator(app, cfg)
			if err != nil {
				return err
			}

			profile, err := orchestratorInstance.Profile(args[0])
			if err != nil {
				return err
			}
			profile.Warnings = validationWarnings(app, "agents."+profile.Name)

			report, checkErr := orchestratorInstance.CheckAgent(cmd.Context(), args[0], probe)
			report.Agent = profile.Name

			out := cmd.OutOrStdout()
			switch output {
			case "text":
				if err := renderAgentProfile(out, profile, report, probe); err != nil {
					return err
				}
			case "json":
				payload := agentDescription{Profile: profile, Capability: report}
				if err := encodeJSON(out, "agent.show", payload); err != nil {
					return err
				}
			default:
				return apperrors.New(apperrors.KindInvalidArgument, "agent.show",
					"unsupported output format %q (want text or json)", output)
			}

			// A probe that could not reach the endpoint is a real failure: the
			// report above already explains it, so the error only carries the
			// classification and the exit status.
			if checkErr != nil && probe {
				return apperrors.Wrap(apperrors.KindOf(checkErr), "agent.show",
					"cannot probe agent %q", checkErr, profile.Name)
			}
			return nil
		},
	}

	flags := cmd.Flags()
	flags.StringVarP(&output, "output", "o", "text", "output format: text or json")
	flags.BoolVar(&probe, "probe", false, "ask the provider what the model supports")

	return cmd
}

// agentDescription is the JSON payload of `pagent agent show`.
type agentDescription struct {
	Profile    orchestrator.AgentProfile     `json:"profile"`
	Capability orchestrator.CapabilityReport `json:"capability"`
}

// renderAgentProfile writes the human readable description.
func renderAgentProfile(out io.Writer, profile orchestrator.AgentProfile, report orchestrator.CapabilityReport, probe bool) error {
	write := func(format string, args ...any) {
		fmt.Fprintf(out, format+"\n", args...)
	}

	write("agent:       %s", profile.Name)
	if profile.Description != "" {
		write("description: %s", oneLineText(profile.Description))
	}
	write("type:        %s", profile.Type)
	if !profile.Executable {
		write("             not executable in this release; only %q agents run",
			orchestrator.TypeSingle)
	}

	write("")
	write("Model")
	write("  alias:     %s", profile.ModelAlias)
	for index, target := range profile.Targets {
		label := "  primary:   "
		if index > 0 {
			label = "  fallback:  "
		}
		write("%s%s/%s", label, target.Provider, target.Model)
	}
	if len(profile.Targets) == 0 {
		write("  ! the alias is not defined in the configuration")
	}

	write("")
	write("Prompt")
	switch profile.Prompt.Kind {
	case orchestrator.PromptFile:
		write("  file:      %s (%d bytes)", profile.Prompt.Path, profile.Prompt.Bytes)
	case orchestrator.PromptBundled:
		write("  bundled:   %s is missing, using the copy built into the binary (%d bytes)",
			profile.Prompt.Path, profile.Prompt.Bytes)
	case orchestrator.PromptInline:
		write("  inline:    %d bytes from the instructions field", profile.Prompt.Bytes)
	default:
		write("  ! no instructions; the model answers without guidance")
	}

	write("")
	write("Tools")
	if len(profile.Tools) == 0 {
		write("  none; the agent answers from the conversation alone")
	} else {
		writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, tool := range profile.Tools {
			status := "known"
			if !tool.Known {
				status = "UNKNOWN"
			}
			fmt.Fprintf(writer, "  %s\t%s\n", tool.Name, status)
		}
		if err := writer.Flush(); err != nil {
			return apperrors.Wrap(apperrors.KindInternal, "agent.show",
				"cannot write the tool grant", err)
		}
		write("  permissions: filesystem=%s shell=%s",
			profile.Permissions.Filesystem, profile.Permissions.Shell)
		if len(profile.Permissions.Deny) > 0 {
			write("  denied:      %s", strings.Join(profile.Permissions.Deny, ", "))
		}
	}

	write("")
	write("Budget")
	write("  rounds:     %d", profile.Budget.MaxRounds)
	write("  tool calls: %d", profile.Budget.MaxToolCalls)
	write("  tool output: %s per call", humanBytes(profile.Budget.MaxToolOutputBytes))
	if profile.Budget.MaxInputTokens > 0 {
		write("  input:      %d tokens", profile.Budget.MaxInputTokens)
	}
	if profile.Budget.MaxOutputTokens > 0 {
		write("  output:     %d tokens", profile.Budget.MaxOutputTokens)
	}
	if profile.Budget.MaxContextTokens > 0 {
		write("  context:    %d tokens", profile.Budget.MaxContextTokens)
	}
	write("  timeout:    %s", valueOrDash(profile.Budget.Timeout))
	write("  output mode: %s", profile.OutputMode)

	write("")
	renderCapabilityReport(write, report, probe)

	if len(profile.Warnings) > 0 {
		write("")
		write("Warnings")
		for _, warning := range profile.Warnings {
			write("  ! %s", warning)
		}
	}

	return nil
}

// renderCapabilityReport writes the capability section of `agent show`.
func renderCapabilityReport(write func(string, ...any), report orchestrator.CapabilityReport, probe bool) {
	write("Capabilities")

	if report.Provider != "" {
		label := "static"
		if report.Probed {
			label = "probed"
		}
		write("  target:     %s/%s (%s)", report.Provider, report.Model, label)
	}
	for _, skipped := range report.Skipped {
		write("  skipped:    %s (%s)", skipped.Target, skipped.Reason)
	}

	if report.Capabilities != nil {
		write("  model:      %s", describeCapabilities(*report.Capabilities))
	}
	write("  needs:      %s", describeRequirements(report.Requirements))

	switch {
	case report.OK:
		write("  result:     ✓ the model satisfies every requirement")
	case report.Error != "" && len(report.Missing) == 0:
		write("  result:     ✗ %s", report.Error)
	default:
		write("  result:     ✗ missing: %s", strings.Join(report.Missing, ", "))
		if report.Error != "" {
			write("              %s", report.Error)
		}
	}

	if !probe {
		write("  note:       static capabilities; pass --probe to ask the endpoint")
	}
}

// describeRequirements renders a requirement set as one line.
func describeRequirements(requirements llm.Requirements) string {
	parts := make([]string, 0, 3)
	if requirements.Tools {
		parts = append(parts, "tool_calling")
	}
	if requirements.SystemMessage {
		parts = append(parts, "system_message")
	}
	if requirements.MinContextTokens > 0 {
		parts = append(parts, fmt.Sprintf("context>=%d", requirements.MinContextTokens))
	}
	if len(parts) == 0 {
		return "nothing beyond a chat completion"
	}
	return strings.Join(parts, ", ")
}

// newOrchestrator builds an orchestrator over the loaded configuration.
func newOrchestrator(app *App, cfg *config.Config) (*orchestrator.Orchestrator, error) {
	registry := provider.NewDefaultRegistry(cfg, app.Logger())
	instance, err := orchestrator.New(cfg, registry, app.Logger())
	if err != nil {
		_ = registry.Close()
		return nil, err
	}
	return instance, nil
}

// validationWarnings returns the validation warnings attached to a path prefix.
func validationWarnings(app *App, prefix string) []string {
	report := app.Validation()
	if report == nil {
		return nil
	}

	var out []string
	for _, warning := range report.Warnings {
		if strings.HasPrefix(warning, prefix) {
			out = append(out, strings.TrimSpace(strings.TrimPrefix(warning, prefix+":")))
		}
	}
	return out
}

// oneLineText collapses whitespace so that a multi-line description fits one
// line of a report.
func oneLineText(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// humanBytes renders a byte count for a human.
func humanBytes(size int) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	value := float64(size)
	for _, suffix := range []string{"KiB", "MiB", "GiB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.0f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.0f GiB", value)
}

// encodeJSON writes an indented JSON payload.
func encodeJSON(out io.Writer, op string, payload any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(payload); err != nil {
		return apperrors.Wrap(apperrors.KindInternal, op, "cannot encode the report", err)
	}
	return nil
}

// describeTarget renders the primary target of a profile for a table cell.
func describeTarget(profile orchestrator.AgentProfile) string {
	if len(profile.Targets) == 0 {
		return "-"
	}
	target := profile.Targets[0]
	rendered := target.Provider + "/" + target.Model
	if len(profile.Targets) > 1 {
		rendered += fmt.Sprintf(" (+%d)", len(profile.Targets)-1)
	}
	return rendered
}
