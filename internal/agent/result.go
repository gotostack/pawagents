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
	"strings"
	"time"

	"github.com/pawagents/pawagents/internal/llm"
)

// Task statuses reported in a result.
const (
	// StatusCompleted means the model produced a final answer.
	StatusCompleted = "completed"
	// StatusFailed means the task ended with an error.
	StatusFailed = "failed"
	// StatusBudgetExceeded means a budget limit stopped the task.
	StatusBudgetExceeded = "budget_exceeded"
	// StatusTimeout means the task deadline passed.
	StatusTimeout = "timeout"
	// StatusCancelled means the caller cancelled the task.
	StatusCancelled = "cancelled"
)

// Finding is one reported problem.
//
// The field names match the structured result envelope documented in the
// README, and the shape is what a host agent needs to verify a claim: where it
// is, what proves it, what to do about it, and how sure the model is.
type Finding struct {
	Severity    string  `json:"severity,omitempty"`
	Category    string  `json:"category,omitempty"`
	File        string  `json:"file,omitempty"`
	Line        int     `json:"line,omitempty"`
	Title       string  `json:"title"`
	Description string  `json:"description,omitempty"`
	Evidence    string  `json:"evidence,omitempty"`
	Suggestion  string  `json:"suggestion,omitempty"`
	Confidence  float64 `json:"confidence,omitempty"`
}

// Result is the outcome of one delegated task.
//
// The envelope is identical for every agent, provider and model: a host must be
// able to consume a result without knowing which model produced it.
type Result struct {
	Status   string `json:"status"`
	Agent    string `json:"agent"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`

	Summary  string    `json:"summary,omitempty"`
	Findings []Finding `json:"findings,omitempty"`

	// Structured reports that the answer was parsed into the envelope above,
	// which is what makes Text redundant for display. It is reported rather
	// than inferred so that a host can tell a parsed result from a model that
	// answered in prose.
	Structured bool `json:"structured,omitempty"`

	// Text is the model answer as written. It is kept even when the answer was
	// parsed into findings, because a host may want to show the reasoning.
	Text string `json:"text,omitempty"`

	// Skipped lists the model targets that were passed over before this one,
	// with the reason. It is reported because falling back silently would let a
	// user believe a cloud model answered when a local one did, or the reverse.
	Skipped []string `json:"skipped,omitempty"`

	Usage Usage `json:"usage"`

	// SessionID is set once sessions are persisted.
	SessionID string `json:"session_id,omitempty"`

	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`

	// Error carries the failure message when Status is not completed.
	Error string `json:"error,omitempty"`
}

// Duration returns how long the task took.
func (r *Result) Duration() time.Duration {
	if r == nil || r.FinishedAt.IsZero() {
		return 0
	}
	return r.FinishedAt.Sub(r.StartedAt)
}

// envelope is the JSON shape the model is asked to produce.
type envelope struct {
	Summary  string    `json:"summary"`
	Findings []Finding `json:"findings"`
}

// parseEnvelope extracts the structured envelope from an answer.
//
// Models are unreliable about framing: they wrap JSON in prose, in a code
// fence, or in both. Rather than failing, the parser looks for the outermost
// braces and falls back to treating the whole answer as the summary, so a
// model that answered well but framed badly still produces a usable result.
func parseEnvelope(text string) (envelope, bool) {
	start := strings.IndexByte(text, '{')
	end := strings.LastIndexByte(text, '}')
	if start < 0 || end <= start {
		return envelope{}, false
	}

	candidate := text[start : end+1]
	var parsed envelope
	decoder := json.NewDecoder(strings.NewReader(candidate))
	if err := decoder.Decode(&parsed); err != nil {
		return envelope{}, false
	}
	if strings.TrimSpace(parsed.Summary) == "" && len(parsed.Findings) == 0 {
		return envelope{}, false
	}
	return parsed, true
}

// RenderText renders a result for a terminal, which is what `pagent run`
// prints. The rendering leads with the finding list because that is what the
// host agent reads first.
func (r *Result) RenderText() string {
	if r == nil {
		return ""
	}

	var b strings.Builder

	fmt.Fprintf(&b, "status:   %s\n", r.Status)
	fmt.Fprintf(&b, "agent:    %s\n", r.Agent)
	if r.Provider != "" || r.Model != "" {
		fmt.Fprintf(&b, "model:    %s/%s\n", r.Provider, r.Model)
	}
	fmt.Fprintf(&b, "duration: %s\n", r.Duration().Round(time.Millisecond))
	fmt.Fprintf(&b, "usage:    %d input tokens, %d output tokens, %d tool calls, %d rounds\n",
		r.Usage.InputTokens, r.Usage.OutputTokens, r.Usage.ToolCalls, r.Usage.Rounds)
	for _, skipped := range r.Skipped {
		fmt.Fprintf(&b, "skipped:  %s\n", skipped)
	}
	if r.SessionID != "" {
		fmt.Fprintf(&b, "session:  %s\n", r.SessionID)
	}
	if r.Error != "" {
		fmt.Fprintf(&b, "error:    %s\n", r.Error)
	}

	if r.Summary != "" {
		fmt.Fprintf(&b, "\nsummary\n%s\n", indent(r.Summary, "  "))
	}

	if len(r.Findings) > 0 {
		fmt.Fprintf(&b, "\nfindings (%d)\n", len(r.Findings))
		for index, finding := range r.Findings {
			fmt.Fprintf(&b, "\n%d. [%s] %s\n", index+1, severityLabel(finding.Severity), finding.Title)
			if finding.File != "" {
				location := finding.File
				if finding.Line > 0 {
					location = fmt.Sprintf("%s:%d", finding.File, finding.Line)
				}
				fmt.Fprintf(&b, "   where:      %s\n", location)
			}
			if finding.Category != "" {
				fmt.Fprintf(&b, "   category:   %s\n", finding.Category)
			}
			if finding.Description != "" {
				fmt.Fprintf(&b, "   detail:     %s\n", oneLine(finding.Description))
			}
			if finding.Evidence != "" {
				fmt.Fprintf(&b, "   evidence:   %s\n", oneLine(finding.Evidence))
			}
			if finding.Suggestion != "" {
				fmt.Fprintf(&b, "   suggestion: %s\n", oneLine(finding.Suggestion))
			}
			if finding.Confidence > 0 {
				fmt.Fprintf(&b, "   confidence: %.2f\n", finding.Confidence)
			}
		}
	}

	if r.Text != "" && !r.Structured {
		fmt.Fprintf(&b, "\nanswer\n%s\n", indent(r.Text, "  "))
	}

	return b.String()
}

// severityLabel normalises a severity for display.
func severityLabel(severity string) string {
	if strings.TrimSpace(severity) == "" {
		return "unspecified"
	}
	return strings.ToLower(strings.TrimSpace(severity))
}

// indent prefixes every line of a block.
func indent(text, prefix string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for index, line := range lines {
		lines[index] = prefix + line
	}
	return strings.Join(lines, "\n")
}

// oneLine collapses a multi-line field so that a terminal report stays scannable.
func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// UsageFromResponse converts provider token accounting into task usage.
func UsageFromResponse(usage llm.Usage) Usage {
	return Usage{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens}
}
