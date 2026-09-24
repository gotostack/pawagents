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
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/orchestrator"
	"github.com/pawagents/pawagents/internal/session"
)

// newSessionCmd implements the `pagent session` command group.
//
// The commands are read only and never contact a provider: a recorded session
// has to be inspectable after the fact, on a machine that no longer has the
// model or the network access that produced it.
func newSessionCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Inspect recorded sessions",
		Long: `Inspect the sessions recorded on this machine.

Every delegated task is written to the sessions directory as a small directory:
metadata.json holds the identity and the outcome, result.json the structured
result, and messages.jsonl and tools.jsonl the transcript and the tool calls.
Reading a session therefore needs nothing but the files, which is why these
commands work offline.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(newSessionListCmd(app), newSessionShowCmd(app))

	return cmd
}

// newSessionListCmd implements `pagent session list`.
func newSessionListCmd(app *App) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List recorded sessions",
		Long: `List the recorded sessions, newest first.

A session whose task is still running, or that was interrupted before it could
be finished, is listed with the running status: the metadata document is
written when the session starts precisely so that a crashed run stays visible
instead of disappearing.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := app.Config()
			if err != nil {
				return err
			}

			store, err := newSessionStore(cfg, app)
			if err != nil {
				return err
			}

			records, err := store.List()
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			switch output {
			case "text":
				if len(records) == 0 {
					fmt.Fprintf(out, "no sessions recorded in %s\n", store.Directory())
					return nil
				}
				writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
				fmt.Fprintln(writer, "SESSION\tAGENT\tMODEL\tSTATUS\tSTARTED\tDURATION\tROUNDS\tTOOLS")
				for _, record := range records {
					meta := record.Meta
					fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\n",
						meta.SessionID,
						meta.Agent,
						valueOrDash(meta.ModelAlias),
						meta.Status,
						formatTime(meta.StartedAt),
						formatDuration(meta.Duration()),
						meta.Usage.Rounds,
						meta.ToolCalls)
				}
				return writer.Flush()
			case "json":
				metas := make([]session.Meta, 0, len(records))
				for _, record := range records {
					metas = append(metas, record.Meta)
				}
				return encodeJSON(out, "session.list", metas)
			default:
				return apperrors.New(apperrors.KindInvalidArgument, "session.list",
					"unsupported output format %q (want text or json)", output)
			}
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "text", "output format: text or json")

	return cmd
}

// newSessionShowCmd implements `pagent session show`.
func newSessionShowCmd(app *App) *cobra.Command {
	var output string
	var messages bool

	cmd := &cobra.Command{
		Use:   "show <session>",
		Short: "Describe one recorded session",
		Long: `Describe one session: what it ran, what it cost and what it returned.

The result section is the same structured envelope that ` + "`pagent run`" + ` printed
when the task finished, so a session is a faithful record of what the host
agent received. Pass --messages to include the recorded transcript, which is
what a person reads to understand why the model answered the way it did.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := app.Config()
			if err != nil {
				return err
			}

			store, err := newSessionStore(cfg, app)
			if err != nil {
				return err
			}

			record, err := store.Open(args[0])
			if err != nil {
				return err
			}

			var transcript []session.TranscriptRecord
			var toolCalls []session.ToolCallRecord
			if messages {
				if transcript, err = store.Transcript(record.ID); err != nil {
					return err
				}
				if toolCalls, err = store.ToolCalls(record.ID); err != nil {
					return err
				}
			}

			out := cmd.OutOrStdout()
			switch output {
			case "text":
				fmt.Fprint(out, renderSession(record, transcript, toolCalls))
				return nil
			case "json":
				return encodeJSON(out, "session.show", sessionDetail{
					Record:     record,
					Transcript: transcript,
					ToolCalls:  toolCalls,
				})
			default:
				return apperrors.New(apperrors.KindInvalidArgument, "session.show",
					"unsupported output format %q (want text or json)", output)
			}
		},
	}

	flags := cmd.Flags()
	flags.StringVarP(&output, "output", "o", "text", "output format: text or json")
	flags.BoolVarP(&messages, "messages", "m", false, "include the recorded transcript")

	return cmd
}

// sessionDetail is the JSON shape of `pagent session show`.
//
// The record is embedded rather than nested so that the output is the session
// document with the transcript attached to it, not a session inside an
// envelope that a reader has to unwrap.
type sessionDetail struct {
	*session.Record
	Transcript []session.TranscriptRecord `json:"transcript,omitempty"`
	ToolCalls  []session.ToolCallRecord   `json:"tool_calls,omitempty"`
}

// newSessionStore builds the session store for the read-only session commands.
//
// It repeats the mapping the orchestrator performs so that inspecting a
// session never builds a provider registry: the point of these commands is to
// work without any of the machinery that produced the session.
func newSessionStore(cfg *config.Config, app *App) (*session.Store, error) {
	return orchestrator.NewSessionStore(cfg, app.Logger())
}

// renderSession renders one session for a terminal.
func renderSession(record *session.Record, transcript []session.TranscriptRecord, toolCalls []session.ToolCallRecord) string {
	meta := record.Meta

	var b strings.Builder
	fmt.Fprintf(&b, "session:   %s\n", meta.SessionID)
	fmt.Fprintf(&b, "directory: %s\n", record.Directory)
	fmt.Fprintf(&b, "agent:     %s\n", meta.Agent)
	if meta.Provider != "" || meta.Model != "" {
		fmt.Fprintf(&b, "model:     %s/%s\n", meta.Provider, meta.Model)
	}
	if meta.ModelAlias != "" {
		fmt.Fprintf(&b, "alias:     %s\n", meta.ModelAlias)
	}
	fmt.Fprintf(&b, "status:    %s\n", meta.Status)
	if meta.Workspace != "" {
		fmt.Fprintf(&b, "workspace: %s\n", meta.Workspace)
	}
	fmt.Fprintf(&b, "started:   %s\n", formatTime(meta.StartedAt))
	fmt.Fprintf(&b, "duration:  %s\n", formatDuration(meta.Duration()))
	fmt.Fprintf(&b, "usage:     %d input tokens, %d output tokens, %d rounds\n",
		meta.Usage.InputTokens, meta.Usage.OutputTokens, meta.Usage.Rounds)
	fmt.Fprintf(&b, "recorded:  %d messages, %d tool calls, %d compactions\n",
		meta.Messages, meta.ToolCalls, meta.Compactions)
	if len(meta.Tools) > 0 {
		fmt.Fprintf(&b, "tools:     %s\n", strings.Join(meta.Tools, ", "))
	}
	if meta.Version != "" {
		fmt.Fprintf(&b, "version:   %s\n", meta.Version)
	}
	if meta.Error != "" {
		fmt.Fprintf(&b, "error:     %s\n", meta.Error)
	}

	if record.Result != nil {
		fmt.Fprintf(&b, "\nresult\n%s", indent(record.Result.RenderText(), "  "))
	}

	if len(transcript) > 0 {
		fmt.Fprintf(&b, "\ntranscript (%d)\n", len(transcript))
		for _, entry := range transcript {
			fmt.Fprintf(&b, "%4d  %s\n", entry.Seq, entry.Describe())
		}
	}

	if len(toolCalls) > 0 {
		fmt.Fprintf(&b, "\ntool calls (%d)\n", len(toolCalls))
		for _, call := range toolCalls {
			status := "ok"
			switch {
			case call.ErrorKind != "":
				status = call.ErrorKind
			case call.IsError:
				status = "error"
			case call.Truncated:
				status = "truncated"
			}
			fmt.Fprintf(&b, "%4d  %-12s %-10s %6d bytes  %s\n",
				call.Seq, call.Tool, status, call.Bytes, formatDuration(time.Duration(call.DurationMS)*time.Millisecond))
		}
	}

	return b.String()
}

// formatTime renders a timestamp in local time, or a dash when it is unset.
func formatTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.Local().Format("2006-01-02 15:04:05")
}

// formatDuration renders a duration compactly, or a dash when it is unknown.
func formatDuration(value time.Duration) string {
	if value <= 0 {
		return "-"
	}
	return value.Round(time.Millisecond).String()
}

// indent prefixes every non-empty line of text with prefix.
func indent(text, prefix string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	var b strings.Builder
	for _, line := range lines {
		if line == "" {
			b.WriteByte('\n')
			continue
		}
		b.WriteString(prefix)
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
