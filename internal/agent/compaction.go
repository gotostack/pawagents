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

package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/pawagents/pawagents/internal/llm"
)

// Compaction limits.
const (
	// CompactionThreshold is the fraction of the context window at which
	// compaction starts. It leaves room for the answer and for the tool
	// definitions, which the token estimate does not count.
	CompactionThreshold = 0.7
	// DefaultElideThreshold is the size above which tool output is replaced by
	// a marker. Small results are kept because they are often the evidence a
	// finding cites.
	DefaultElideThreshold = 2000
	// DefaultKeepRecent is how many trailing messages compaction never
	// touches, so the model keeps the thread of what it just did.
	DefaultKeepRecent = 6
	// elisionMarker replaces content a compaction removed.
	elisionMarker = "[elided during context compaction]"
	// maxSummaryFiles bounds how many paths the summary lists.
	maxSummaryFiles = 12
)

// CompactionOptions describes one compaction decision.
type CompactionOptions struct {
	// Window is the usable context window in tokens.
	Window int
	// ElideThreshold is the size above which tool output is replaced.
	ElideThreshold int
	// KeepRecent is how many trailing messages are never touched.
	KeepRecent int
}

// CompactionReport describes what a compaction did.
//
// It is part of the session record, because a reader replaying a transcript has
// to know that earlier evidence was elided rather than never gathered.
type CompactionReport struct {
	// Elided counts the results that were replaced by a marker.
	Elided int `json:"elided"`
	// BeforeTokens and AfterTokens are character based estimates.
	BeforeTokens int `json:"before_tokens"`
	AfterTokens  int `json:"after_tokens"`
	// Summary is the note added to the system prompt.
	Summary string `json:"summary,omitempty"`
	// Changed reports whether the conversation was modified at all.
	Changed bool `json:"changed"`
}

// CompactionResult is a compacted conversation and its report.
type CompactionResult struct {
	Messages []llm.Message
	Report   CompactionReport
}

// Compactor reduces a conversation that no longer fits the context window.
//
// The interface exists so that a model based summariser can replace the
// built-in strategy without touching the loop: it receives the conversation and
// returns a shorter one plus a report, and it may fail, in which case the loop
// continues with the conversation it already had.
type Compactor interface {
	Compact(messages []llm.Message, options CompactionOptions) (CompactionResult, error)
}

// ElisionCompactor is the built-in strategy: it replaces large tool output from
// the middle of the conversation with a marker and describes what was removed.
//
// It never summarises with a model, because an extra generation per compaction
// would double the cost of a long task and could itself fail. Instead it reports
// exactly what the model needs in order to decide whether to look again: which
// tools ran, how often, and which paths were touched. Being deterministic is
// also what makes it reproducible in a session record.
type ElisionCompactor struct{}

// Compact implements Compactor.
func (ElisionCompactor) Compact(messages []llm.Message, options CompactionOptions) (CompactionResult, error) {
	keepRecent := options.KeepRecent
	if keepRecent <= 0 {
		keepRecent = DefaultKeepRecent
	}
	threshold := options.ElideThreshold
	if threshold <= 0 {
		threshold = DefaultElideThreshold
	}

	// The system message and the delegated task must survive: dropping either
	// would leave the model without instructions or without a question.
	if len(messages) <= keepRecent+2 {
		return CompactionResult{Messages: messages}, nil
	}

	lastIndex := len(messages) - keepRecent
	out := make([]llm.Message, len(messages))
	copy(out, messages)

	report := CompactionReport{BeforeTokens: estimateTokens(messages)}

	for index := 1; index < lastIndex; index++ {
		message := out[index]

		if message.ToolResult != nil && len(message.ToolResult.Content) > threshold {
			clone := message.Clone()
			clone.ToolResult.Content = describeElidedResult(message.ToolResult)
			clone.ToolResult.Metadata = nil
			out[index] = clone
			report.Elided++
			continue
		}

		// A long assistant message without tool calls is usually a verbose
		// explanation of a step. The model wrote it, so it does not need to be
		// handed back verbatim; the final answer is what matters. Messages that
		// do carry tool calls are left alone, because their arguments are the
		// record of what was actually done.
		if message.Role == llm.RoleAssistant && !message.HasToolCalls() &&
			len(message.Text()) > threshold {
			clone := message.Clone()
			clone.Content = []llm.ContentPart{llm.TextPart(elisionMarker)}
			out[index] = clone
			report.Elided++
		}
	}

	if report.Elided == 0 {
		return CompactionResult{Messages: messages}, nil
	}

	report.AfterTokens = estimateTokens(out)
	report.Changed = true
	report.Summary = buildCompactionSummary(out, report)

	return CompactionResult{Messages: out, Report: report}, nil
}

// describeElidedResult replaces one tool result with a marker that keeps the
// information the model needs in order to decide whether to call it again.
func describeElidedResult(result *llm.ToolResult) string {
	var b strings.Builder

	fmt.Fprintf(&b, "[earlier tool output elided: %s returned %s",
		toolLabel(result.Name), humanSize(len(result.Content)))
	if result.IsError {
		b.WriteString(", and reported a failure")
	}

	// The first line is kept because it is usually the most informative part of
	// a diff, a listing or a search hit.
	if head := firstLine(result.Content); head != "" {
		fmt.Fprintf(&b, "; it began with: %s", head)
	}
	if result.Truncated {
		b.WriteString("; it was truncated")
	}
	b.WriteString("; call the tool again with a narrower request if you need the rest]")

	return b.String()
}

// buildCompactionSummary describes the work done so far.
func buildCompactionSummary(messages []llm.Message, report CompactionReport) string {
	tools, paths := surveyConversation(messages)

	var b strings.Builder
	b.WriteString("## Compacted history\n\n")
	fmt.Fprintf(&b, "Earlier turns were compacted to stay inside the context window: "+
		"%d large tool result(s) were replaced by a short marker, reducing the conversation "+
		"from roughly %d to %d tokens.\n", report.Elided, report.BeforeTokens, report.AfterTokens)

	if len(tools) > 0 {
		fmt.Fprintf(&b, "\nTools used so far: %s.\n", strings.Join(tools, ", "))
	}
	if len(paths) > 0 {
		fmt.Fprintf(&b, "Paths seen so far: %s.\n", strings.Join(paths, ", "))
	}
	b.WriteString("\nThe evidence behind the elided results is not lost: call a tool " +
		"again with a narrower request when you need it, and do not guess at what you " +
		"can no longer see.")

	return b.String()
}

// surveyConversation collects which tools ran and which paths were touched.
func surveyConversation(messages []llm.Message) ([]string, []string) {
	counts := map[string]int{}
	pathSet := map[string]bool{}

	for _, message := range messages {
		for _, call := range message.ToolCalls {
			if name := strings.TrimSpace(call.Name); name != "" {
				counts[name]++
			}
			if path := argumentPath(call.Arguments); path != "" {
				pathSet[path] = true
			}
		}
	}

	tools := make([]string, 0, len(counts))
	for name, count := range counts {
		tools = append(tools, fmt.Sprintf("%s (%d)", name, count))
	}
	sort.Strings(tools)

	paths := make([]string, 0, len(pathSet))
	for path := range pathSet {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	if len(paths) > maxSummaryFiles {
		remaining := len(paths) - maxSummaryFiles
		paths = append(paths[:maxSummaryFiles], fmt.Sprintf("... and %d more", remaining))
	}

	return tools, paths
}

// argumentPath extracts a path or query from tool arguments, when there is one.
func argumentPath(arguments string) string {
	if strings.TrimSpace(arguments) == "" {
		return ""
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(arguments), &decoded); err != nil {
		return ""
	}
	for _, key := range []string{"path", "file", "directory", "query", "ref"} {
		if value, ok := decoded[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// firstLine returns the first meaningful line of a tool result.
func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if len(trimmed) > 160 {
			trimmed = trimmed[:160] + "..."
		}
		return trimmed
	}
	return ""
}

// toolLabel names a tool in a report, falling back to a generic label.
func toolLabel(name string) string {
	if strings.TrimSpace(name) == "" {
		return "a tool"
	}
	return name
}

// humanSize renders a byte count for a marker.
func humanSize(size int) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d bytes", size)
	}
	if size < unit*unit {
		return fmt.Sprintf("%.1f KiB", float64(size)/unit)
	}
	return fmt.Sprintf("%.1f MiB", float64(size)/(unit*unit))
}
