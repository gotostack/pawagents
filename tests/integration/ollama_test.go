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

//go:build integration

// Package integration runs the acceptance chains against real services.
//
// Every test here needs something outside the process: an Ollama server, a
// built binary or a host CLI. Each one skips with the reason it cannot run, so
// running the package without the gate reports honestly instead of passing.
package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawagents/pawagents/internal/agent"
	"github.com/pawagents/pawagents/tests/internal/harness"
)

// TestProviderTestPassesAgainstTheRealServer is the first step of the MVP
// acceptance test: the configured endpoint really serves the configured models.
func TestProviderTestPassesAgainstTheRealServer(t *testing.T) {
	installed := harness.RequireOllama(t)
	model := harness.Model(t, installed)

	home := harness.OllamaConfig(t, model)
	binary := harness.Binary(t)

	stdout, stderr, code := harness.Run(t, binary, harness.Env(home),
		"provider", "test", "local-ollama", "--output", "json")
	if code != 0 {
		t.Fatalf("provider test exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	var report struct {
		Server struct {
			Reachable bool `json:"reachable"`
		} `json:"server"`
		Models []struct {
			Alias     string `json:"alias"`
			Model     string `json:"model"`
			OK        bool   `json:"ok"`
			Installed *bool  `json:"installed"`
		} `json:"models"`
		Errors int `json:"errors"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("the report is not valid JSON: %v\n%s", err, stdout)
	}

	if !report.Server.Reachable {
		t.Fatalf("the server is reported as unreachable: %s", stdout)
	}
	if len(report.Models) != 1 {
		t.Fatalf("models = %+v, want the single configured alias", report.Models)
	}
	entry := report.Models[0]
	if entry.Alias != "local-coder" || entry.Model != model {
		t.Fatalf("the report checked %s/%s, want local-coder/%s", entry.Alias, entry.Model, model)
	}
	if !entry.OK || report.Errors != 0 {
		t.Fatalf("the provider test failed: %s", stdout)
	}
}

// TestReviewRunUsesTheToolsAndReportsAgainstTheRepository is the second step of
// the acceptance test: an agent reviews a real uncommitted change, calls the
// read-only tools, and the findings it reports name files that exist.
//
// The assertion on the file is the part that makes this a review rather than a
// format check: a finding that points at a path the repository does not contain
// is a hallucination, and a hallucinated location is exactly what a host agent
// must not be handed.
func TestReviewRunUsesTheToolsAndReportsAgainstTheRepository(t *testing.T) {
	installed := harness.RequireOllama(t)
	model := harness.Model(t, installed)

	home := harness.OllamaConfig(t, model)
	binary := harness.Binary(t)
	repo := harness.GitRepo(t)

	task := "Review the uncommitted change in this repository. " +
		"Start with git.diff, then read the file it touches with repo.read. " +
		"Report at most three findings and name the file each one is about."

	stdout, stderr, code := harness.Run(t, binary, harness.Env(home),
		"run", "--agent", "local-reviewer", "--workspace", repo,
		"--task", task, "--output", "json")
	if code != 0 {
		t.Fatalf("run exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	var result agent.Result
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("the result is not valid JSON: %v\n%s", err, stdout)
	}

	if result.Status != agent.StatusCompleted {
		t.Fatalf("status = %q, want %q (error: %s)", result.Status, agent.StatusCompleted, result.Error)
	}
	if result.Agent != "local-reviewer" {
		t.Fatalf("agent = %q, want local-reviewer", result.Agent)
	}
	if result.Model == "" || result.Provider == "" {
		t.Fatalf("the result does not name the model that answered: %+v", result)
	}
	if !strings.Contains(result.Model, model) {
		t.Fatalf("model = %q, want the configured %q", result.Model, model)
	}
	if result.SessionID == "" {
		t.Fatal("the run did not record a session, so the host cannot ask for it again")
	}
	if result.Usage.InputTokens == 0 {
		t.Fatal("the result reports no input tokens")
	}

	// The chain includes the tool runtime: a review that never called a tool
	// cannot have read the change.
	tools := readToolCalls(t, home, result.SessionID)
	if len(tools) == 0 {
		t.Fatalf("the agent called no tool, so it cannot have read the diff\n%s", stdout)
	}
	read := false
	for _, name := range tools {
		if name == "git.diff" || name == "repo.read" {
			read = true
		}
	}
	if !read {
		t.Fatalf("the agent called %v, none of which reads the change", tools)
	}

	// Every location has to be real.
	for _, finding := range result.Findings {
		if finding.File == "" {
			continue
		}
		path := filepath.Join(repo, filepath.FromSlash(finding.File))
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("the finding names %q, which does not exist in the repository: %v",
				finding.File, err)
		}
	}
}

// readToolCalls reads the recorded tool calls of a session.
func readToolCalls(t *testing.T, home, sessionID string) []string {
	t.Helper()

	path := filepath.Join(home, "sessions", sessionID, "tools.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the session recorded no tool calls (%v)", err)
	}

	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// The record is the on-disk contract of tools.jsonl, read here the way
		// a host would read it: one JSON object per line.
		var record struct {
			Tool string `json:"tool"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("the tool record is not valid JSON: %v\n%s", err, line)
		}
		names = append(names, record.Tool)
	}
	return names
}

// TestReportedSessionsAreReadableBackThroughTheCLI closes the loop a host
// cares about: the session id in the result resolves to a readable transcript.
func TestReportedSessionsAreReadableBackThroughTheCLI(t *testing.T) {
	installed := harness.RequireOllama(t)
	model := harness.Model(t, installed)

	home := harness.OllamaConfig(t, model)
	binary := harness.Binary(t)
	repo := harness.GitRepo(t)

	stdout, stderr, code := harness.Run(t, binary, harness.Env(home),
		"run", "--agent", "local-reviewer", "--workspace", repo,
		"--task", "Read ratelimit.go with repo.read and summarise it in one sentence.",
		"--output", "json")
	if code != 0 {
		t.Fatalf("run exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	var result agent.Result
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("the result is not valid JSON: %v", err)
	}
	if result.SessionID == "" {
		t.Fatal("the result has no session id")
	}

	list, listErr, listCode := harness.Run(t, binary, harness.Env(home), "session", "list")
	if listCode != 0 {
		t.Fatalf("session list exited %d\n%s", listCode, listErr)
	}
	if !strings.Contains(list, result.SessionID) {
		t.Fatalf("session list does not mention %s:\n%s", result.SessionID, list)
	}

	show, showErr, showCode := harness.Run(t, binary, harness.Env(home),
		"session", "show", result.SessionID, "--messages")
	if showCode != 0 {
		t.Fatalf("session show exited %d\n%s", showCode, showErr)
	}
	for _, want := range []string{"local-reviewer", "repo.read"} {
		if !strings.Contains(show, want) {
			t.Fatalf("the transcript does not mention %q:\n%s", want, show)
		}
	}
}

// TestAMissingLocalModelIsReportedAsSuch checks the failure side of the chain:
// a configuration that asks for a model the server does not have must fail
// with the action that fixes it, not with a generic error.
func TestAMissingLocalModelIsReportedAsSuch(t *testing.T) {
	installed := harness.RequireOllama(t)

	// A name that is not installed, but is a plausible model name.
	missing := "pawagents-not-installed:1b"
	for _, name := range installed {
		if name == missing {
			t.Skipf("%q happens to be installed on %s", missing, harness.Endpoint())
		}
	}

	home := harness.OllamaConfig(t, missing)
	binary := harness.Binary(t)

	stdout, stderr, code := harness.Run(t, binary, harness.Env(home),
		"provider", "test", "local-ollama")
	if code == 0 {
		t.Fatalf("provider test passed for a model that is not installed:\n%s", stdout)
	}
	combined := stdout + stderr
	if !strings.Contains(combined, "ollama pull") {
		t.Fatalf("the failure does not say how to fix it:\n%s", combined)
	}
}
