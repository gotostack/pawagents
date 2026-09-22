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
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/prompts"
)

// newConfigCmd implements the `pagent config` command group.
func newConfigCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect, validate and create the configuration",
		Long: `Work with the PawAgents configuration file.

The effective configuration is built in four steps: read the YAML file, apply
the built-in defaults, normalise paths and values, then validate. Only the first
step can fail hard; validation always reports every problem it finds so that one
run is enough to fix a configuration.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(
		newConfigShowCmd(app),
		newConfigValidateCmd(app),
		newConfigInitCmd(app),
		newConfigPathCmd(app),
	)

	return cmd
}

// newConfigShowCmd implements `pagent config show`.
func newConfigShowCmd(app *App) *cobra.Command {
	var output string
	var showSecrets bool

	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print the effective configuration",
		Long: `Print the effective configuration: the file contents merged with the
built-in defaults, after path expansion.

Credentials are replaced by ` + "`[REDACTED]`" + ` unless --show-secrets is given, so
the output is safe to paste into an issue.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, _, usedDefaults, err := app.LoadConfig()
			if err != nil {
				return err
			}

			render := cfg
			if !showSecrets {
				render = cfg.Redacted()
			}

			out := cmd.OutOrStdout()
			switch output {
			case "yaml":
				if usedDefaults {
					fmt.Fprintf(out, "# no configuration file found at %s\n", cfg.Path)
					fmt.Fprint(out, "# showing built-in defaults\n\n")
				}
				data, err := render.YAMLDocument()
				if err != nil {
					return err
				}
				_, err = out.Write(data)
				return err
			case "json":
				if usedDefaults {
					fmt.Fprintf(app.Err(), "note: no configuration file found at %s; "+
						"showing built-in defaults\n", cfg.Path)
				}
				data, err := render.JSONDocument()
				if err != nil {
					return err
				}
				_, err = out.Write(data)
				return err
			default:
				return apperrors.New(apperrors.KindInvalidArgument, "config.show",
					"unsupported output format %q (want yaml or json)", output)
			}
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "yaml", "output format: yaml or json")
	cmd.Flags().BoolVar(&showSecrets, "show-secrets", false,
		"include credentials instead of redacting them")

	return cmd
}

// newConfigValidateCmd implements `pagent config validate`.
func newConfigValidateCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate the configuration and report every problem",
		Long: `Validate the effective configuration.

Errors make the configuration unusable: an unknown provider type, a model that
references a missing provider, an agent that requests shell access. Warnings
describe a configuration that still runs: a missing API key environment
variable, a prompt file that has not been created yet, a roadmap feature that is
accepted but not implemented.

The command exits with status 3 when any error is found.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, result, usedDefaults, err := app.LoadConfig()
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			path := cfg.Path
			if usedDefaults {
				fmt.Fprintf(out, "configuration: no file at %s, validating built-in defaults\n", path)
			} else {
				fmt.Fprintf(out, "configuration: %s\n", path)
			}
			fmt.Fprintf(out, "result: %s\n", result.Summary())
			fmt.Fprint(out, result.Report())

			return result.Err()
		},
	}

	return cmd
}

// newConfigInitCmd implements `pagent config init`.
func newConfigInitCmd(app *App) *cobra.Command {
	var force bool
	var skipPrompts bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a starter configuration file",
		Long: `Write a starter configuration file to ~/.pawagents/config.yaml and
materialise the built-in agent prompts next to it.

The generated file validates as-is. Cloud providers whose API key environment
variable is unset produce warnings, not errors, so validation passes on a
machine that only has a local Ollama server.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := config.ResolvePath(app.configPath)
			if err != nil {
				return err
			}
			home, err := config.Home()
			if err != nil {
				return err
			}

			if err := config.WriteFile(path, config.DefaultYAML(home), force); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ wrote %s\n", path)

			if !skipPrompts {
				written, err := prompts.WriteDefaults(filepath.Join(home, "prompts"), force)
				if err != nil {
					return apperrors.Wrap(apperrors.KindConfig, "config.init",
						"cannot write the prompt files", err)
				}
				if len(written) == 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "✓ prompt files already present in %s\n",
						filepath.Join(home, "prompts"))
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "✓ wrote %d prompt file(s) in %s\n",
						len(written), filepath.Join(home, "prompts"))
				}
			}

			fmt.Fprint(cmd.OutOrStdout(), "\nnext steps:\n")
			fmt.Fprint(cmd.OutOrStdout(), "  pagent config validate\n")
			fmt.Fprint(cmd.OutOrStdout(), "  pagent config show\n")

			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing files")
	cmd.Flags().BoolVar(&skipPrompts, "skip-prompts", false,
		"only write the configuration file, not the prompt files")

	return cmd
}

// newConfigPathCmd implements `pagent config path`.
func newConfigPathCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "path",
		Short: "Print the configuration file path",
		Long: `Print the configuration file path that every other command would use,
honouring --config and PAWAGENTS_CONFIG.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := config.ResolvePath(app.configPath)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), path)
			return nil
		},
	}

	return cmd
}
