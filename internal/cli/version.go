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

	"github.com/spf13/cobra"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/version"
)

// newVersionCmd implements `pagent version`.
func newVersionCmd() *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print build information",
		Long: `Print the PawAgents build metadata.

The text output is meant for humans, the JSON output for scripts and issue
reports. Because the values are injected at build time, a binary installed from
a release archive reports the exact tag it was built from.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := version.Info()
			out := cmd.OutOrStdout()

			switch output {
			case "text":
				fmt.Fprintf(out, "%s %s\n", info.Binary, info.Version)
				fmt.Fprintf(out, "commit:    %s\n", info.Commit)
				fmt.Fprintf(out, "built:     %s\n", info.Date)
				fmt.Fprintf(out, "go:        %s\n", info.Go)
				fmt.Fprintf(out, "platform:  %s\n", info.Platform)
				return nil
			case "short":
				fmt.Fprintln(out, info.Version)
				return nil
			case "json":
				encoder := json.NewEncoder(out)
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(info); err != nil {
					return apperrors.Wrap(apperrors.KindInternal, "version.render",
						"cannot encode build information", err)
				}
				return nil
			default:
				return apperrors.New(apperrors.KindInvalidArgument, "version.output",
					"unsupported output format %q (want text, short or json)", output)
			}
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "text", "output format: text, short or json")

	return cmd
}
