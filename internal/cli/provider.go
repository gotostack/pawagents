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
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/provider"

	// Register the provider implementations compiled into this binary.
	_ "github.com/pawagents/pawagents/internal/provider/all"
)

// Provider status values reported by `pagent provider list`. They are the
// shared provider package values so that every command describes a provider
// identically.
const (
	providerStatusReady       = provider.StatusReady
	providerStatusDisabled    = provider.StatusDisabled
	providerStatusPlanned     = provider.StatusPlanned
	providerStatusUnavailable = provider.StatusUnavailable
)

// providerEntry is the rendered description of a configured provider.
type providerEntry struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Endpoint   string   `json:"endpoint,omitempty"`
	Credential string   `json:"credential,omitempty"`
	Status     string   `json:"status"`
	Timeout    string   `json:"timeout,omitempty"`
	Models     []string `json:"models,omitempty"`
	Agents     []string `json:"agents,omitempty"`
}

// newProviderCmd implements the `pagent provider` command group.
func newProviderCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provider",
		Short: "Inspect the configured model providers",
		Long: `Inspect the model endpoints PawAgents is configured to talk to.

A provider is a type plus an endpoint plus a credential. Nothing here performs
a network call: the listing describes the configuration, and connectivity is
checked by a request.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(newProviderListCmd(app), newProviderShowCmd(app), newProviderTestCmd(app))

	return cmd
}

// newProviderListCmd implements `pagent provider list`.
func newProviderListCmd(app *App) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the configured providers",
		Long: `List every provider defined in the configuration.

The status column reports whether this build can use the provider: "ready"
means an implementation is compiled in, "planned" means the type is documented
on the roadmap only, "unavailable" means the type is known but not wired up in
this build, and "disabled" means the entry is switched off in the
configuration.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := app.Config()
			if err != nil {
				return err
			}

			entries := describeProviders(cfg)

			out := cmd.OutOrStdout()
			switch output {
			case "text":
				if len(entries) == 0 {
					fmt.Fprintln(out, "no providers configured; run `pagent config init` to start from a template")
					return nil
				}
				writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
				fmt.Fprintln(writer, "PROVIDER\tTYPE\tENDPOINT\tCREDENTIAL\tSTATUS")
				for _, entry := range entries {
					fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n",
						entry.Name, entry.Type, entry.Endpoint, entry.Credential, entry.Status)
				}
				return writer.Flush()
			case "json":
				encoder := json.NewEncoder(out)
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(entries); err != nil {
					return apperrors.Wrap(apperrors.KindInternal, "provider.list",
						"cannot encode the provider list", err)
				}
				return nil
			default:
				return apperrors.New(apperrors.KindInvalidArgument, "provider.list",
					"unsupported output format %q (want text or json)", output)
			}
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "text", "output format: text or json")

	return cmd
}

// newProviderShowCmd implements `pagent provider show <provider>`.
func newProviderShowCmd(app *App) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "show <provider>",
		Short: "Describe one provider and who uses it",
		Long: `Describe one provider: its type, endpoint, credential source and the models
and agents that reference it.

Listing the consumers answers the question that matters when a provider is
changed: what breaks if this endpoint goes away.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := app.Config()
			if err != nil {
				return err
			}

			name := args[0]
			if _, ok := cfg.Providers[name]; !ok {
				return apperrors.New(apperrors.KindNotFound, "provider.show",
					"provider %q is not defined in the configuration", name)
			}

			entry := describeProvider(cfg, name)

			out := cmd.OutOrStdout()
			switch output {
			case "text":
				fmt.Fprintf(out, "provider:   %s\n", entry.Name)
				fmt.Fprintf(out, "type:       %s\n", entry.Type)
				fmt.Fprintf(out, "endpoint:   %s\n", valueOrDash(entry.Endpoint))
				fmt.Fprintf(out, "credential: %s\n", valueOrDash(entry.Credential))
				fmt.Fprintf(out, "timeout:    %s\n", valueOrDash(entry.Timeout))
				fmt.Fprintf(out, "status:     %s\n", entry.Status)
				fmt.Fprintf(out, "models:     %s\n", listOrDash(entry.Models))
				fmt.Fprintf(out, "agents:     %s\n", listOrDash(entry.Agents))
				return nil
			case "json":
				encoder := json.NewEncoder(out)
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(entry); err != nil {
					return apperrors.Wrap(apperrors.KindInternal, "provider.show",
						"cannot encode the provider description", err)
				}
				return nil
			default:
				return apperrors.New(apperrors.KindInvalidArgument, "provider.show",
					"unsupported output format %q (want text or json)", output)
			}
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "text", "output format: text or json")

	return cmd
}

// describeProviders describes every configured provider, sorted by name.
func describeProviders(cfg *config.Config) []providerEntry {
	names := cfg.ProviderNames()
	entries := make([]providerEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, describeProvider(cfg, name))
	}
	return entries
}

// describeProvider describes one provider. It never contacts the endpoint, so
// it is safe to call from a diagnostic path.
func describeProvider(cfg *config.Config, name string) providerEntry {
	cfgProvider := cfg.Providers[name]
	entry := providerEntry{Name: name}

	if cfgProvider == nil {
		entry.Status = providerStatusUnavailable
		return entry
	}

	entry.Type = cfgProvider.Type
	entry.Timeout = cfgProvider.Timeout.String()

	// The transport factory is reused so that the endpoint and the credential
	// description are computed exactly as a request would compute them.
	if transport, err := provider.NewTransport(cfgProvider); err == nil {
		entry.Endpoint = transport.Endpoint()
		entry.Credential = transport.DescribeCredential()
		_ = transport.Close()
	} else {
		entry.Endpoint = cfgProvider.BaseURL
		entry.Credential = "invalid"
	}

	entry.Status = provider.Status(cfgProvider)
	entry.Models, entry.Agents = providerConsumers(cfg, name)

	return entry
}

// providerConsumers returns the model aliases bound to a provider and the
// agents bound to those aliases.
func providerConsumers(cfg *config.Config, name string) ([]string, []string) {
	models := make([]string, 0, len(cfg.Models))
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
			models = append(models, fmt.Sprintf("%s (%s)", alias, target.Model))
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
	return models, agents
}

func valueOrDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func listOrDash(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, ", ")
}
