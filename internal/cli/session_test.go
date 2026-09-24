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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pawagents/pawagents/internal/agent"
	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/llm"
	"github.com/pawagents/pawagents/internal/session"
)

// writeSessionConfig installs a configuration whose session store lives in a
// temporary directory, and returns that directory. The default location would
// be the user home, which a test must never write to.
func writeSessionConfig(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfigPath, "")

	directory := filepath.Join(home, "sessions")
	contents := fmt.Sprintf(`version: 1
sessions:
  directory: %s
`, directory)
	if err := os.WriteFile(filepath.Join(home, config.ConfigFileName), []byte(contents), 0o600); err != nil {
		t.Fatalf("cannot write the test configuration: %v", err)
	}
	return directory
}

// seedSession writes one finished session, so that the list and show commands
// have something to read without contacting a provider.
func seedSession(t *testing.T, directory string, options ...func(*session.Session)) *session.Session {
	t.Helper()

	store, err := session.NewStore(session.Options{
		Directory:        directory,
		PersistMessages:  true,
		PersistToolCalls: true,
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	record, err := store.Start(session.Meta{
		Agent:      "local-reviewer",
		Provider:   "local-ollama",
		Model:      "qwen3-coder",
		ModelAlias: "local-coder",
		Workspace:  "/tmp/workspace",
		Status:     session.StatusRunning,
		StartedAt:  time.Now(),
		Tools:      []string{"repo.read", "git.diff"},
		MaxRounds:  12,
		Version:    "test",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	for _, option := range options {
		option(record)
	}

	result := &agent.Result{
		Status:   agent.StatusCompleted,
		Agent:    "local-reviewer",
		Provider: "local-ollama",
		Model:    "qwen3-coder",
		Summary:  "One problem was found in the loop.",
		Findings: []agent.Finding{{
			Severity: "high",
			File:     "internal/agent/loop.go",
			Line:     42,
			Title:    "Rounds are not counted",
		}},
		Usage:     agent.Usage{InputTokens: 1200, OutputTokens: 300, Rounds: 3},
		StartedAt: record.Meta().StartedAt,
	}
	if err := record.Finish(result); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	return record
}

func TestSessionListWithNoSessions(t *testing.T) {
	directory := writeSessionConfig(t)

	stdout, stderr, code := run(t, "session", "list")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "no sessions recorded") {
		t.Fatalf("output = %q, want a notice that nothing was recorded", stdout)
	}
	if !strings.Contains(stdout, directory) {
		t.Fatalf("output = %q, want the directory it looked in", stdout)
	}

	// An empty store is still a valid command, including in JSON.
	stdout, stderr, code = run(t, "session", "list", "--output", "json")
	if code != apperrors.ExitOK {
		t.Fatalf("json exit code = %d, stderr = %s", code, stderr)
	}
	if strings.TrimSpace(stdout) != "[]" {
		t.Fatalf("json output = %q, want an empty array", stdout)
	}
}

func TestSessionListTextOutput(t *testing.T) {
	directory := writeSessionConfig(t)

	seedSession(t, directory)
	time.Sleep(5 * time.Millisecond)
	newest := seedSession(t, directory)

	stdout, stderr, code := run(t, "session", "list")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}

	for _, column := range []string{"SESSION", "AGENT", "MODEL", "STATUS", "STARTED", "DURATION", "ROUNDS", "TOOLS"} {
		if !strings.Contains(stdout, column) {
			t.Fatalf("the listing is missing the %q column:\n%s", column, stdout)
		}
	}

	rows := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(rows) != 3 {
		t.Fatalf("the listing has %d lines, want a header and two rows:\n%s", len(rows), stdout)
	}
	if !strings.HasPrefix(strings.TrimSpace(rows[1]), newest.ID()) {
		t.Fatalf("the newest session is not listed first:\n%s", stdout)
	}
	for _, want := range []string{"local-reviewer", "local-coder", "completed", "3"} {
		if !strings.Contains(rows[1], want) {
			t.Fatalf("the first row is missing %q:\n%s", want, rows[1])
		}
	}
}

func TestSessionListJSONOutput(t *testing.T) {
	directory := writeSessionConfig(t)

	seedSession(t, directory)
	time.Sleep(5 * time.Millisecond)
	newest := seedSession(t, directory)

	stdout, stderr, code := run(t, "session", "list", "-o", "json")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}

	var metas []session.Meta
	if err := json.Unmarshal([]byte(stdout), &metas); err != nil {
		t.Fatalf("cannot decode the listing: %v\n%s", err, stdout)
	}
	if len(metas) != 2 {
		t.Fatalf("the listing has %d entries, want 2", len(metas))
	}
	if metas[0].SessionID != newest.ID() {
		t.Fatalf("first entry = %q, want %q", metas[0].SessionID, newest.ID())
	}
	if metas[0].Agent != "local-reviewer" || !metas[0].Finished() {
		t.Fatalf("unexpected entry: %+v", metas[0])
	}
}

func TestSessionShowTextOutput(t *testing.T) {
	directory := writeSessionConfig(t)
	record := seedSession(t, directory, func(record *session.Session) {
		record.Message(llm.NewSystemMessage("You review Go code."))
		record.Message(llm.NewUserMessage("Review internal/agent/loop.go"))
		record.ToolCall(
			llm.ToolCall{ID: "call_1", Name: "repo.read", Arguments: `{"path":"internal/agent/loop.go"}`},
			llm.ToolResult{Content: "package agent", Metadata: map[string]any{"bytes": int(13)}},
		)
	})

	stdout, stderr, code := run(t, "session", "show", record.ID())
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}

	for _, want := range []string{
		"session:   " + record.ID(),
		"agent:     local-reviewer",
		"model:     local-ollama/qwen3-coder",
		"alias:     local-coder",
		"status:    completed",
		"recorded:  2 messages, 1 tool calls, 0 compactions",
		"result",
		"One problem was found in the loop.",
		"Rounds are not counted",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("the report is missing %q:\n%s", want, stdout)
		}
	}

	// The transcript costs a file read, so it is opt-in.
	if strings.Contains(stdout, "transcript") {
		t.Fatalf("the transcript was printed without --messages:\n%s", stdout)
	}

	stdout, stderr, code = run(t, "session", "show", record.ID(), "--messages")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	for _, want := range []string{
		"transcript (2)",
		"Review internal/agent/loop.go",
		"tool calls (1)",
		"repo.read",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("the transcript is missing %q:\n%s", want, stdout)
		}
	}
}

func TestSessionShowJSONOutput(t *testing.T) {
	directory := writeSessionConfig(t)
	record := seedSession(t, directory, func(record *session.Session) {
		record.Message(llm.NewUserMessage("Review the loop"))
	})

	stdout, stderr, code := run(t, "session", "show", record.ID(), "-o", "json")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	if strings.Contains(stdout, "transcript") {
		t.Fatalf("the transcript was encoded without --messages:\n%s", stdout)
	}

	var detail sessionDetail
	if err := json.Unmarshal([]byte(stdout), &detail); err != nil {
		t.Fatalf("cannot decode the session: %v\n%s", err, stdout)
	}
	if detail.Record == nil || detail.ID != record.ID() {
		t.Fatalf("unexpected session document: %+v", detail)
	}
	if detail.Meta.Agent != "local-reviewer" || detail.Meta.Usage.InputTokens != 1200 {
		t.Fatalf("unexpected metadata: %+v", detail.Meta)
	}
	if detail.Result == nil || detail.Result.Summary != "One problem was found in the loop." {
		t.Fatalf("unexpected result: %+v", detail.Result)
	}
	if len(detail.Result.Findings) != 1 || detail.Result.Findings[0].Line != 42 {
		t.Fatalf("the findings did not round trip: %+v", detail.Result.Findings)
	}

	stdout, stderr, code = run(t, "session", "show", record.ID(), "-o", "json", "--messages")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), &detail); err != nil {
		t.Fatalf("cannot decode the session with its transcript: %v\n%s", err, stdout)
	}
	if len(detail.Transcript) != 1 || len(detail.Transcript[0].Content) != 1 {
		t.Fatalf("the transcript did not round trip: %+v", detail.Transcript)
	}
	if detail.Transcript[0].Content[0].Text != "Review the loop" {
		t.Fatalf("transcript text = %q", detail.Transcript[0].Content[0].Text)
	}
}

func TestSessionShowReportsAnUnknownSession(t *testing.T) {
	writeSessionConfig(t)

	stdout, _, code := run(t, "session", "show", "agt_0000000000000000")
	if code != apperrors.ExitNotFound {
		t.Fatalf("exit code = %d, want %d\n%s", code, apperrors.ExitNotFound, stdout)
	}
}

func TestSessionCommandsRejectBadInput(t *testing.T) {
	writeSessionConfig(t)

	cases := []struct {
		name string
		args []string
	}{
		{"list with a bad output format", []string{"session", "list", "--output", "yaml"}},
		{"show with a bad output format", []string{"session", "show", "agt_1", "--output", "yaml"}},
		{"show without an identifier", []string{"session", "show"}},
		{"show with a path instead of an identifier", []string{"session", "show", "../../etc/passwd"}},
		{"show with too many identifiers", []string{"session", "show", "agt_1", "agt_2"}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, _, code := run(t, testCase.args...)
			if code != apperrors.ExitUsage && code != apperrors.ExitNotFound {
				t.Fatalf("exit code = %d, want a usage or not-found status", code)
			}
		})
	}
}

func TestRunRecordsASessionThatSessionShowCanRead(t *testing.T) {
	server := newSSEServer(t, textAnswer(`{"summary":"Nothing to report.","findings":[]}`))
	workspace := writeRunConfig(t, server.server.URL, `  reviewer:
    model: test-model
    instructions: Review code carefully.
    output_mode: structured`)

	stdout, stderr, code := run(t, "run", "--agent", "reviewer", "--task", "Review",
		"--workspace", workspace)
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}

	sessionLine := ""
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "session:") {
			sessionLine = strings.TrimSpace(strings.TrimPrefix(line, "session:"))
		}
	}
	if !strings.HasPrefix(sessionLine, session.IDPrefix) {
		t.Fatalf("the report does not name the session it recorded:\n%s", stdout)
	}

	// The session has to be complete on disk. tools.jsonl is created lazily,
	// on the first tool call, so a task that called no tool leaves no file.
	directory := filepath.Join(os.Getenv(config.EnvHome), "sessions", sessionLine)
	for _, name := range []string{session.MetadataFile, session.ResultFile, session.MessagesFile} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Fatalf("%s is missing from the recorded session: %v", name, err)
		}
	}

	// Metadata is written when the session starts, so it has to describe a
	// finished task once the run is over.
	metadata, err := os.ReadFile(filepath.Join(directory, session.MetadataFile))
	if err != nil {
		t.Fatalf("cannot read the metadata: %v", err)
	}
	var meta session.Meta
	if err := json.Unmarshal(metadata, &meta); err != nil {
		t.Fatalf("the metadata is not valid JSON: %v", err)
	}
	if meta.Status != agent.StatusCompleted || !meta.Finished() {
		t.Fatalf("the metadata was not updated after the run: %+v", meta)
	}
	if meta.Agent != "reviewer" || meta.Provider != "local" || meta.Model != "fake-model" {
		t.Fatalf("the metadata lost the identity of the run: %+v", meta)
	}

	listing, stderr, code := run(t, "session", "list")
	if code != apperrors.ExitOK {
		t.Fatalf("session list exit code = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(listing, sessionLine) {
		t.Fatalf("the recorded session is not listed:\n%s", listing)
	}

	shown, stderr, code := run(t, "session", "show", sessionLine, "--messages")
	if code != apperrors.ExitOK {
		t.Fatalf("session show exit code = %d, stderr = %s", code, stderr)
	}
	for _, want := range []string{"status:    completed", "Nothing to report.", "Review"} {
		if !strings.Contains(shown, want) {
			t.Fatalf("the recorded session is missing %q:\n%s", want, shown)
		}
	}
}
