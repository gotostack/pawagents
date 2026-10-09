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
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/doctor"
	"github.com/pawagents/pawagents/internal/orchestrator"
	"github.com/pawagents/pawagents/internal/provider"
	"github.com/pawagents/pawagents/internal/version"

	// Register the provider implementations compiled into this binary.
	_ "github.com/pawagents/pawagents/internal/provider/all"
)

// newDoctorCmd implements `pagent doctor`.
//
// The command answers the question a user has when a coding agent cannot
// delegate: which part of this installation is wrong. It is deliberately
// forgiving about its own failures, because an installation that cannot load
// its configuration is exactly the case it has to report.
func newDoctorCmd(app *App) *cobra.Command {
	var output string
	var probe bool
	var workspace string

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose the installation and report what would fail",
		Long: `Check the parts a delegation depends on and report each one.

The checks cover the configuration, the providers, the local model servers and
the models they are asked for, the agents, the workspace a delegation would
read, and the MCP server a coding agent connects to. Every check that can be
answered from the configuration is offline; a local model server is probed
because that is where the interesting failures live and the call never leaves
the machine.

The exit status is the classification of the first failed check, so a script can
tell a missing credential from an unusable workspace. Warnings do not fail the
command.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, validation, _, loadErr := app.LoadConfig()

			options := doctor.Options{
				Config:      cfg,
				ConfigPath:  configPath(app),
				ConfigError: loadErr,
				Validation:  validation,
				Workspace:   workspace,
				Probe:       probe,
				Version:     version.Info().Version,
				Logger:      app.Logger(),
			}

			// The runtime and the endpoints share one registry, so a probe and
			// an agent check talk to the same client and the report describes
			// one installation rather than two.
			if cfg != nil && loadErr == nil {
				registry := provider.NewDefaultRegistry(cfg, app.Logger())
				defer func() { _ = registry.Close() }()

				options.Endpoints = registry
				if instance, err := orchestrator.New(cfg, registry, app.Logger()); err == nil {
					options.Runtime = instance
				} else {
					app.Logger().Warn("cannot inspect the agents", slog.String("error", err.Error()))
				}
			}

			report := doctor.Run(cmd.Context(), options)

			out := cmd.OutOrStdout()
			switch output {
			case "text":
				if _, err := out.Write([]byte(report.RenderText())); err != nil {
					return apperrors.Wrap(apperrors.KindInternal, "doctor",
						"cannot write the diagnostic report", err)
				}
			case "json":
				if err := encodeJSON(out, "doctor", report); err != nil {
					return err
				}
			default:
				return apperrors.New(apperrors.KindInvalidArgument, "doctor",
					"unsupported output format %q (want text or json)", output)
			}

			// The report is the output; the exit status only carries the
			// classification so that a script can branch on a category.
			if kind := report.ExitKind(); kind != "" {
				return apperrors.New(kind, "doctor",
					"%d check(s) failed", report.Summary.Fail)
			}
			return nil
		},
	}

	flags := cmd.Flags()
	flags.StringVarP(&output, "output", "o", "text", "output format: text or json")
	flags.BoolVar(&probe, "probe", false, "ask remote endpoints too, not only local model servers")
	flags.StringVarP(&workspace, "workspace", "w", "", "workspace to check (default: the configured root, then the current directory)")

	return cmd
}

// configPath reports the configuration file the report should name.
//
// The path is resolved even when the file is missing: a report that cannot say
// which file it looked for is not a diagnosis.
func configPath(app *App) string {
	path, err := config.ResolvePath(app.configPath)
	if err != nil {
		return app.configPath
	}
	return path
}
