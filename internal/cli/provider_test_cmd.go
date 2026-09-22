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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/llm"
	"github.com/pawagents/pawagents/internal/orchestrator"
	"github.com/pawagents/pawagents/internal/provider"
)

// providerTestServer is the connectivity part of the report.
type providerTestServer struct {
	Reachable bool   `json:"reachable"`
	Version   string `json:"version,omitempty"`
	Detail    string `json:"detail,omitempty"`
	Error     string `json:"error,omitempty"`
}

// providerTestModel is the per model part of the report.
type providerTestModel struct {
	Alias        string                 `json:"alias"`
	Model        string                 `json:"model"`
	OK           bool                   `json:"ok"`
	Capabilities *llm.ModelCapabilities `json:"capabilities,omitempty"`
	Installed    *bool                  `json:"installed,omitempty"`
	Error        string                 `json:"error,omitempty"`
}

// providerTestReport is what `pagent provider test` produces.
type providerTestReport struct {
	Provider        string              `json:"provider"`
	Type            string              `json:"type"`
	Endpoint        string              `json:"endpoint,omitempty"`
	Server          *providerTestServer `json:"server,omitempty"`
	InstalledModels []string            `json:"installed_models,omitempty"`
	Models          []providerTestModel `json:"models"`
	Agents          []string            `json:"agents,omitempty"`
	Errors          int                 `json:"errors"`
}

// newProviderTestCmd implements `pagent provider test <provider>`.
func newProviderTestCmd(app *App) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "test <provider>",
		Short: "Check that a provider can actually serve its models",
		Long: `Check a provider end to end: is the endpoint reachable, is each configured
model installed, and does each model support what its agent needs.

The command contacts the endpoint. It is the fastest way to diagnose the
failures that only appear at run time: Ollama is not running, the model was
never pulled, or the model does not support tool calling while its agent grants
tools.

The exit status is 5 when any check fails.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := app.Config()
			if err != nil {
				return err
			}

			report, err := testProvider(cmd.Context(), app, cfg, args[0])
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			switch output {
			case "text":
				renderProviderTest(out, report)
			case "json":
				encoder := json.NewEncoder(out)
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(report); err != nil {
					return apperrors.Wrap(apperrors.KindInternal, "provider.test",
						"cannot encode the test report", err)
				}
			default:
				return apperrors.New(apperrors.KindInvalidArgument, "provider.test",
					"unsupported output format %q (want text or json)", output)
			}

			if report.Errors > 0 {
				return apperrors.New(apperrors.KindCapability, "provider.test",
					"%d check(s) failed for provider %q", report.Errors, report.Provider)
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "text", "output format: text or json")

	return cmd
}

// testProvider runs every check and returns the report. It returns an error
// only when the provider cannot be constructed at all; a failed check is
// reported inside the report.
func testProvider(ctx context.Context, app *App, cfg *config.Config, name string) (*providerTestReport, error) {
	cfgProvider, ok := cfg.Providers[name]
	if !ok {
		return nil, apperrors.New(apperrors.KindNotFound, "provider.test",
			"provider %q is not defined in the configuration", name)
	}

	report := &providerTestReport{
		Provider: name,
		Type:     cfgProvider.Type,
	}
	if transport, err := provider.NewTransport(cfgProvider); err == nil {
		report.Endpoint = transport.Endpoint()
		_ = transport.Close()
	}

	// The command runs with the caller's context so that an interrupt stops
	// the probes instead of hanging on a stalled endpoint.
	registry := provider.NewDefaultRegistry(cfg, app.Logger())
	defer func() { _ = registry.Close() }()

	instance, err := registry.Provider(ctx, name)
	if err != nil {
		report.Errors = 1
		report.Server = &providerTestServer{Reachable: false, Error: err.Error()}
		return report, nil
	}

	// Connectivity is probed first: every later check depends on it.
	if reporter, ok := instance.(provider.HealthReporter); ok {
		health, err := reporter.Health(ctx)
		server := &providerTestServer{
			Reachable: health.Reachable,
			Version:   health.Version,
			Detail:    health.Detail,
		}
		if err != nil {
			server.Error = err.Error()
			report.Errors++
		}
		report.Server = server
	}

	if lister, ok := instance.(provider.ModelLister); ok {
		if models, err := lister.ListModels(ctx); err == nil {
			report.InstalledModels = models
		}
	}

	aliases, agents := providerModelAliases(cfg, name)
	report.Agents = agents

	for _, alias := range aliases {
		targets, _, err := cfg.ModelTargets(alias)
		if err != nil {
			report.Models = append(report.Models, providerTestModel{
				Alias: alias,
				OK:    false,
				Error: err.Error(),
			})
			report.Errors++
			continue
		}

		entry := providerTestModel{Alias: alias, Model: targets[0].Model, OK: true}

		if report.InstalledModels != nil {
			installed := provider.MatchModel(report.InstalledModels, entry.Model)
			entry.Installed = &installed
			if !installed {
				entry.OK = false
			}
		}

		capabilities, err := instance.Capabilities(ctx, entry.Model)
		switch {
		case err != nil:
			entry.OK = false
			entry.Error = err.Error()
		default:
			// The requirement set is derived from the agents bound to the
			// alias, so the check matches what a run would demand.
			if err := capabilities.Validate(orchestrator.RequirementsForAlias(cfg, alias), entry.Model); err != nil {
				entry.OK = false
				entry.Error = err.Error()
			}
			entry.Capabilities = &capabilities
		}

		// A model is one check: a missing installation and a capability
		// refusal for the same alias are two symptoms of one problem.
		if !entry.OK {
			report.Errors++
		}

		report.Models = append(report.Models, entry)
	}

	return report, nil
}

// providerModelAliases returns the model aliases bound to a provider and the
// agents that use those aliases.
func providerModelAliases(cfg *config.Config, name string) ([]string, []string) {
	aliases := make([]string, 0, len(cfg.Models))
	agentSet := map[string]bool{}

	for _, alias := range cfg.ModelNames() {
		model := cfg.Models[alias]
		if model == nil {
			continue
		}
		for _, target := range model.Targets() {
			if target.Provider != name {
				continue
			}
			aliases = append(aliases, alias)
			for agentName, agent := range cfg.Agents {
				if agent != nil && agent.Model == alias {
					agentSet[agentName] = true
				}
			}
			break
		}
	}

	agents := make([]string, 0, len(agentSet))
	for agentName := range agentSet {
		agents = append(agents, agentName)
	}
	sort.Strings(agents)
	if len(agents) == 0 {
		agents = nil
	}
	return aliases, agents
}

// renderProviderTest writes the human readable report.
func renderProviderTest(out io.Writer, report *providerTestReport) {
	write := func(format string, args ...any) {
		fmt.Fprintf(out, format+"\n", args...)
	}

	write("provider: %s", report.Provider)
	write("type:     %s", report.Type)
	if report.Endpoint != "" {
		write("endpoint: %s", report.Endpoint)
	}
	write("")

	if report.Server != nil {
		write("Server")
		if report.Server.Reachable {
			detail := report.Server.Detail
			if detail == "" {
				detail = "reachable"
			}
			write("  ✓ %s", detail)
		} else {
			write("  ✗ not reachable")
			if report.Server.Error != "" {
				write("      %s", report.Server.Error)
			}
		}
		write("")
	}

	if report.InstalledModels != nil {
		write("Installed models: %d", len(report.InstalledModels))
		write("")
	}

	write("Models")
	if len(report.Models) == 0 {
		write("  ! no model alias points at this provider")
	} else {
		for _, model := range report.Models {
			status := "✓"
			if !model.OK {
				status = "✗"
			}
			write("  %s %s → %s", status, model.Alias, model.Model)
			if model.Capabilities != nil {
				write("      %s", describeCapabilities(*model.Capabilities))
			}
			if model.Installed != nil && !*model.Installed {
				write("      the endpoint does not list this model")
			}
			if model.Error != "" {
				write("      %s", strings.ReplaceAll(model.Error, "\n", "\n      "))
			}
		}
	}
	write("")

	if len(report.Agents) > 0 {
		write("Agents: %s", strings.Join(report.Agents, ", "))
		write("")
	}

	if report.Errors == 0 {
		write("result: ✓ every check passed")
		return
	}
	write("result: %d check(s) failed", report.Errors)
}

// describeCapabilities renders the capability flags on one line.
func describeCapabilities(capabilities llm.ModelCapabilities) string {
	flags := []string{
		flag("tool_calling", capabilities.ToolCalling),
		flag("streaming", capabilities.Streaming),
		flag("structured_output", capabilities.StructuredOutput),
		flag("vision", capabilities.Vision),
		flag("reasoning", capabilities.Reasoning),
	}
	if capabilities.MaxContextTokens > 0 {
		flags = append(flags, fmt.Sprintf("max_context_tokens=%d", capabilities.MaxContextTokens))
	}
	return strings.Join(flags, "  ")
}

func flag(name string, enabled bool) string {
	if enabled {
		return name
	}
	return name + ":no"
}
