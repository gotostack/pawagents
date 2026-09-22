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
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/orchestrator"
	"github.com/pawagents/pawagents/internal/provider"

	// Register the provider implementations compiled into this binary.
	_ "github.com/pawagents/pawagents/internal/provider/all"
)

// newModelCmd implements the `pagent model` command group.
func newModelCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "model",
		Short: "Inspect the configured model aliases",
		Long: `Inspect the model registry: which alias points at which provider and model.

An alias is the indirection that keeps agents portable. An agent names
"local-coder", the alias names a provider and a concrete model, and changing
the alias moves every agent that uses it from a cloud model to a local one
without touching a single agent profile.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(newModelListCmd(app), newModelShowCmd(app))

	return cmd
}

// newModelListCmd implements `pagent model list`.
func newModelListCmd(app *App) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the configured model aliases",
		Long: `List every model alias defined in the configuration.

The STATUS column is the status of the first usable target: "ready" means this
build can serve requests through the alias, and anything else means the provider
is disabled, planned, missing from this build, or undefined.`,
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

			entries := orchestratorInstance.Models()

			out := cmd.OutOrStdout()
			switch output {
			case "text":
				if len(entries) == 0 {
					fmt.Fprintln(out, "no models configured; add a models block to the configuration")
					return nil
				}
				writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
				fmt.Fprintln(writer, "ALIAS\tTARGET\tFALLBACK\tSTATUS\tAGENTS")
				for _, entry := range entries {
					target, ok := entry.Target()
					if !ok {
						fmt.Fprintf(writer, "%s\t-\t-\t%s\t%s\n",
							entry.Alias, entry.Status, listOrDash(entry.Agents))
						continue
					}
					fmt.Fprintf(writer, "%s\t%s\t%d\t%s\t%s\n",
						entry.Alias,
						target.Provider+"/"+target.Model,
						len(entry.Targets)-1,
						entry.Status,
						listOrDash(entry.Agents))
				}
				return writer.Flush()
			case "json":
				return encodeJSON(out, "model.list", entries)
			default:
				return apperrors.New(apperrors.KindInvalidArgument, "model.list",
					"unsupported output format %q (want text or json)", output)
			}
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "text", "output format: text or json")

	return cmd
}

// newModelShowCmd implements `pagent model show`.
func newModelShowCmd(app *App) *cobra.Command {
	var output string
	var probe bool

	cmd := &cobra.Command{
		Use:   "show <alias>",
		Short: "Describe one model alias and its targets",
		Long: `Describe one model alias: every provider target in order, the capabilities each
one provides, the agents bound to it and whether those agents can run.

Fallbacks are reported individually because that is how the router treats them:
the primary is tried first, and a target that cannot be built or probed moves
the task to the next one instead of failing it.

With --probe every usable target is asked what its model supports. The command
exits with a provider error only when no target could be probed, because an
alias that still has a working fallback is usable.`,
		Example: `  pagent model show local-coder
  pagent model show local-coder --probe --output json`,
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

			entry, err := orchestratorInstance.Model(args[0])
			if err != nil {
				return err
			}

			if probe {
				entry = orchestratorInstance.ProbeModel(cmd.Context(), entry)
			}

			reports, err := orchestratorInstance.CheckModel(cmd.Context(), entry.Alias, false)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			switch output {
			case "text":
				if err := renderModelEntry(out, entry, reports, probe); err != nil {
					return err
				}
			case "json":
				payload := modelDescription{Model: entry, Agents: reports}
				if err := encodeJSON(out, "model.show", payload); err != nil {
					return err
				}
			default:
				return apperrors.New(apperrors.KindInvalidArgument, "model.show",
					"unsupported output format %q (want text or json)", output)
			}

			if probe && !anyTargetProbed(entry) {
				return apperrors.New(apperrors.KindProvider, "model.show",
					"no provider target of %q could be probed", entry.Alias)
			}
			return nil
		},
	}

	flags := cmd.Flags()
	flags.StringVarP(&output, "output", "o", "text", "output format: text or json")
	flags.BoolVar(&probe, "probe", false, "ask each provider what its model supports")

	return cmd
}

// modelDescription is the JSON payload of `pagent model show`.
type modelDescription struct {
	Model  orchestrator.ModelEntry         `json:"model"`
	Agents []orchestrator.CapabilityReport `json:"agents,omitempty"`
}

// renderModelEntry writes the human readable description.
func renderModelEntry(out io.Writer, entry orchestrator.ModelEntry, reports []orchestrator.CapabilityReport, probe bool) error {
	write := func(format string, args ...any) {
		fmt.Fprintf(out, format+"\n", args...)
	}

	write("alias:       %s", entry.Alias)
	write("status:      %s", entry.Status)
	if entry.Temperature != nil {
		write("temperature: %.2f", *entry.Temperature)
	}
	write("agents:      %s", listOrDash(entry.Agents))

	write("")
	write("Targets")
	for index, target := range entry.Targets {
		role := "primary"
		if index > 0 {
			role = fmt.Sprintf("fallback %d", index)
		}

		marker := "✓"
		if target.Status != provider.StatusReady || target.Error != "" {
			marker = "✗"
		}
		write("  %s %s: %s/%s", marker, role, target.Provider, target.Model)
		write("      type:   %s", valueOrDash(target.ProviderType))
		write("      status: %s", target.Status)
		if target.Capabilities != nil {
			source := "static"
			if target.Probed {
				source = "probed"
			}
			write("      capabilities (%s): %s", source, describeCapabilities(*target.Capabilities))
		}
		if target.Error != "" {
			write("      error:  %s", target.Error)
		}
	}

	write("")
	write("Agents")
	if len(reports) == 0 {
		write("  no agent uses this alias")
	} else {
		writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, report := range reports {
			marker := "✓"
			detail := "ready"
			if !report.OK {
				marker = "✗"
				switch {
				case len(report.Missing) > 0:
					detail = "missing " + strings.Join(report.Missing, ", ")
				case report.Error != "":
					detail = report.Error
				default:
					detail = "cannot run"
				}
			}
			fmt.Fprintf(writer, "  %s %s\t%s\n", marker, report.Agent, detail)
		}
		if err := writer.Flush(); err != nil {
			return apperrors.Wrap(apperrors.KindInternal, "model.show",
				"cannot write the agent list", err)
		}
	}

	if !probe {
		write("")
		write("capabilities are static; pass --probe to ask the endpoints")
	}

	return nil
}

// anyTargetProbed reports whether at least one target answered a probe.
func anyTargetProbed(entry orchestrator.ModelEntry) bool {
	for _, target := range entry.Targets {
		if target.Probed {
			return true
		}
	}
	return false
}
