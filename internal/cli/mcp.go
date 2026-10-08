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
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/mcpserver"
	"github.com/pawagents/pawagents/internal/orchestrator"
	"github.com/pawagents/pawagents/internal/provider"
	"github.com/pawagents/pawagents/internal/version"
)

// newMCPCmd implements the `pagent mcp` command group.
//
// The group exists because a host connects over a transport, and the transport
// is a deployment decision: stdio today, HTTP later. Keeping the transport a
// subcommand of `serve` means the tools are defined once and stay identical no
// matter how a host reaches them.
func newMCPCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve PawAgents to a coding agent over MCP",
		Long: `Serve the PawAgents runtime over the Model Context Protocol.

A coding agent such as Codex or Claude Code connects to this server, lists the
configured agents and delegates a bounded task to one of them. The tools are
the same for every host:

  list_agents    list the agents and whether they can run
  get_agent      describe one agent in detail
  delegate_task  run one task and return a structured result

Subagents are read-only: they analyse a workspace and report, and the host
agent remains responsible for applying any change.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(newMCPServeCmd(app))

	return cmd
}

// newMCPServeCmd implements `pagent mcp serve`.
func newMCPServeCmd(app *App) *cobra.Command {
	var stdio bool

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve MCP on a transport",
		Long: `Serve the MCP tools on a transport.

The stdio transport is the one a coding agent starts for itself:

  codex mcp add pawagents -- pagent mcp serve --stdio
  claude mcp add pawagents -- pagent mcp serve --stdio

Standard output carries the protocol, so it must stay free of anything else;
diagnostics are written to standard error. An HTTP transport is reserved for a
later release, which is why the transport is a flag rather than a separate
command: adding it will not change the tools a host sees.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !stdio {
				return apperrors.New(apperrors.KindInvalidArgument, "mcp.serve",
					"no transport selected: pass --stdio (the HTTP transport is reserved for a later release)")
			}

			cfg, err := app.Config()
			if err != nil {
				return err
			}

			server, closeRegistry, err := newMCPServer(app, cfg)
			if err != nil {
				return err
			}
			defer closeRegistry()

			return server.Run(cmd.Context(), &mcp.StdioTransport{})
		},
	}

	cmd.Flags().BoolVar(&stdio, "stdio", false,
		"serve the protocol on standard input and output (required)")

	return cmd
}

// newMCPServer builds the MCP server over the configured runtime.
//
// The provider registry is created here and closed by the returned function,
// because building a registry never contacts an endpoint: a host that only
// lists agents pays nothing for it, and a delegation resolves its credential
// when it is actually made.
func newMCPServer(app *App, cfg *config.Config) (*mcpserver.Server, func(), error) {
	registry := provider.NewDefaultRegistry(cfg, app.Logger())
	runtime, err := orchestrator.New(cfg, registry, app.Logger())
	if err != nil {
		_ = registry.Close()
		return nil, func() {}, err
	}

	closeRegistry := func() { _ = registry.Close() }

	server, err := mcpserver.New(mcpserver.Options{
		Runtime: runtime,
		Config:  cfg,
		Logger:  app.Logger(),
		Version: version.Info().Version,
	})
	if err != nil {
		closeRegistry()
		return nil, func() {}, err
	}

	return server, closeRegistry, nil
}
