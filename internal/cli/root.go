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

	"github.com/spf13/cobra"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/version"
)

// Execute runs the CLI and returns the process exit status. Every failure is
// classified, rendered on stderr and mapped to a documented exit code.
func Execute(ctx context.Context) int {
	app := NewApp()
	return runRoot(ctx, app, newRootCmd(app))
}

// runRoot executes a command tree and renders the outcome.
//
// It exists so that the tests drive exactly the code path the binary drives:
// a test that built its own error handling would not catch a mistake in this
// one.
func runRoot(ctx context.Context, app *App, root *cobra.Command) int {
	root.SetOut(app.Out())
	root.SetErr(app.Err())

	if err := root.ExecuteContext(ctx); err != nil {
		err = classifyUsage(err)
		app.PrintError(err)
		return apperrors.ExitCode(err)
	}
	return apperrors.ExitOK
}

// classifyUsage turns an unclassified command line failure into a usage error.
//
// Everything the runtime raises carries a kind, so an error without one can
// only have come from the command line layer itself: an unknown command, a
// missing argument, an unparsable flag. Those are usage mistakes and must exit
// with the documented usage code instead of the unclassified one, which is why
// the classification happens here rather than in every command.
func classifyUsage(err error) error {
	if err == nil || apperrors.KindOf(err) != apperrors.KindInternal {
		return err
	}
	return apperrors.New(apperrors.KindInvalidArgument, "cli.usage", "%s", err)
}

// newRootCmd builds the command tree.
func newRootCmd(app *App) *cobra.Command {
	root := &cobra.Command{
		Use:   version.Binary,
		Short: "Universal external subagent runtime for coding agents",
		Long: `PawAgents is a universal external subagent runtime.

Coding agents such as OpenAI Codex and Claude Code connect to PawAgents over
MCP and delegate a bounded task to a named subagent. PawAgents owns the agent
loop, the tool runtime and the provider layer, so a host agent never needs to
know which model, provider or endpoint runs the task.

    Codex  ─┐
            ├─ MCP ─→ pagent runtime ─→ LLM Router ─→ provider
    Claude ─┘                                  ├── OpenAI
                                               ├── Anthropic
                                               ├── OpenAI-compatible
                                               └── Ollama (local)

Subagents are read-only by default: they analyse a workspace with repository and
git tools and report findings, while the host agent stays responsible for
modifying files.`,
		Example: `  # Show the effective configuration
  pagent config show

  # Print build information
  pagent version`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.Info().Version,
		Args:          cobra.NoArgs,
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			app.setupLogging()
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	root.SetVersionTemplate(version.Binary + " version {{.Version}}\n")

	flags := root.PersistentFlags()
	flags.StringVar(&app.configPath, "config", "",
		"configuration file (default ~/.pawagents/config.yaml, env PAWAGENTS_CONFIG)")
	flags.StringVar(&app.logLevel, "log-level", "",
		"log level: debug, info, warn, error (env PAWAGENTS_LOG_LEVEL)")
	flags.StringVar(&app.logFormat, "log-format", "",
		"log format: text or json (env PAWAGENTS_LOG_FORMAT)")
	flags.BoolVarP(&app.verbose, "verbose", "v", false, "shorthand for --log-level debug")

	root.AddCommand(
		newVersionCmd(),
		newConfigCmd(app),
		newProviderCmd(app),
		newModelCmd(app),
		newAgentCmd(app),
		newRunCmd(app),
		newSessionCmd(app),
	)

	return root
}
