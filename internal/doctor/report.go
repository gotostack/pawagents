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

// Package doctor answers one question: why is this installation not working?
//
// It exists because the failures it reports are otherwise hard to reach. A host
// that connects to the MCP server and sees no usable agent, a model alias whose
// provider has no credential in the environment, a local model that is not
// installed, a workspace that is not a repository: each of those surfaces later
// as a task failure inside a coding agent, where the cause is invisible.
//
// Every check is offline except the probes of local model servers, which is
// where the interesting failures live and which cost nothing when they fail.
package doctor

import (
	"fmt"
	"strings"

	"github.com/pawagents/pawagents/internal/apperrors"
)

// Status is the outcome of one check.
type Status string

// Check statuses. They are part of the JSON output, so the strings are stable.
const (
	// StatusOK means the check passed.
	StatusOK Status = "ok"
	// StatusWarn means the check found something the user should know about but
	// that does not stop a delegation.
	StatusWarn Status = "warn"
	// StatusFail means the check found a problem that stops a delegation.
	StatusFail Status = "fail"
)

// Symbols used by the text rendering.
const (
	symbolOK   = "✓"
	symbolWarn = "!"
	symbolFail = "✗"
)

// Check is one line of the report.
type Check struct {
	// Status is the outcome.
	Status Status `json:"status"`
	// Label is what was checked, for example a path or an agent name.
	Label string `json:"label"`
	// Detail explains the outcome, or names the fix.
	Detail string `json:"detail,omitempty"`
	// Kind classifies a failure so that the process exit status can carry it.
	// It is empty for a check that passed.
	Kind apperrors.Kind `json:"kind,omitempty"`
}

// Section groups the checks of one area, in the order the report prints them.
type Section struct {
	// Name is the heading, for example "Providers".
	Name string `json:"name"`
	// Checks are the lines of the section.
	Checks []Check `json:"checks"`
}

// Summary counts the checks by outcome.
type Summary struct {
	OK   int `json:"ok"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
}

// Report is the full diagnostic run.
type Report struct {
	// Version is the build that produced the report.
	Version string `json:"version,omitempty"`
	// Config is the configuration file that was used, when one was loaded.
	Config string `json:"config,omitempty"`
	// Sections are the checks, grouped by area.
	Sections []Section `json:"sections"`
	// Summary counts the checks by outcome.
	Summary Summary `json:"summary"`
}

// add appends a section and its checks.
func (r *Report) add(name string, checks []Check) {
	if len(checks) == 0 {
		return
	}
	r.Sections = append(r.Sections, Section{Name: name, Checks: checks})
}

// Summarize counts the checks of every section. It is called once, after the
// checks have run, so that a section builder cannot forget it.
func (r *Report) Summarize() {
	r.Summary = Summary{}
	for _, section := range r.Sections {
		for _, check := range section.Checks {
			switch check.Status {
			case StatusOK:
				r.Summary.OK++
			case StatusWarn:
				r.Summary.Warn++
			case StatusFail:
				r.Summary.Fail++
			}
		}
	}
}

// Failed reports whether any check failed.
func (r *Report) Failed() bool { return r.Summary.Fail > 0 }

// ExitKind classifies the run for the process exit status: the kind of the
// first failed check, in report order.
//
// Report order is the order the checks run in, so the status reflects the first
// thing that is wrong with the installation rather than the last one. A report
// without failures is not an error.
func (r *Report) ExitKind() apperrors.Kind {
	for _, section := range r.Sections {
		for _, check := range section.Checks {
			if check.Status == StatusFail {
				if check.Kind != "" {
					return check.Kind
				}
				return apperrors.KindInternal
			}
		}
	}
	return ""
}

// RenderText renders the report for a terminal.
func (r *Report) RenderText() string {
	var b strings.Builder
	b.WriteString("PawAgents diagnostics\n")

	for _, section := range r.Sections {
		fmt.Fprintf(&b, "\n%s\n", section.Name)
		for _, check := range section.Checks {
			fmt.Fprintf(&b, "  %s %s\n", symbolFor(check.Status), check.Label)
			if check.Detail != "" {
				for _, line := range strings.Split(check.Detail, "\n") {
					fmt.Fprintf(&b, "      %s\n", line)
				}
			}
		}
	}

	fmt.Fprintf(&b, "\n%s\n", r.SummaryText())
	return b.String()
}

// SummaryText is the closing line of the text rendering.
func (r *Report) SummaryText() string {
	switch {
	case r.Summary.Fail > 0:
		return fmt.Sprintf("%d passed, %d warning(s), %d failed: fix the %s lines before delegating.",
			r.Summary.OK, r.Summary.Warn, r.Summary.Fail, symbolFail)
	case r.Summary.Warn > 0:
		return fmt.Sprintf("%d passed, %d warning(s): delegations work, the %s lines are worth a look.",
			r.Summary.OK, r.Summary.Warn, symbolWarn)
	default:
		return fmt.Sprintf("all %d checks passed.", r.Summary.OK)
	}
}

// symbolFor returns the marker of a status.
func symbolFor(status Status) string {
	switch status {
	case StatusWarn:
		return symbolWarn
	case StatusFail:
		return symbolFail
	default:
		return symbolOK
	}
}

// pass builds a passing check.
func pass(label, detail string) Check {
	return Check{Status: StatusOK, Label: label, Detail: detail}
}

// warn builds a check the user should look at.
func warn(label, detail string, kind apperrors.Kind) Check {
	return Check{Status: StatusWarn, Label: label, Detail: detail, Kind: kind}
}

// fail builds a failing check.
func fail(label, detail string, kind apperrors.Kind) Check {
	return Check{Status: StatusFail, Label: label, Detail: detail, Kind: kind}
}
