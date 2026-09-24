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

package session

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pawagents/pawagents/internal/agent"
	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/llm"
)

// newTestStore builds a store rooted in a temporary directory.
func newTestStore(t *testing.T, options Options) *Store {
	t.Helper()

	if options.Directory == "" {
		options.Directory = t.TempDir()
	}
	if options.Logger == nil {
		options.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelError,
		}))
	}

	store, err := NewStore(options)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

// startTestSession opens a session with a plausible metadata document.
func startTestSession(t *testing.T, store *Store) *Session {
	t.Helper()

	session, err := store.Start(Meta{
		Agent:      "local-reviewer",
		Provider:   "local-ollama",
		Model:      "qwen3.8:27b-mlx",
		ModelAlias: "local-coder",
		Workspace:  "/tmp/workspace",
		Status:     StatusRunning,
		StartedAt:  time.Now(),
		Tools:      []string{"repo.read", "git.diff"},
		MaxRounds:  12,
		Version:    "test",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return session
}

func TestNewIDIsPrefixedAndUnique(t *testing.T) {
	seen := make(map[string]bool, 64)
	for i := 0; i < 64; i++ {
		id, err := NewID()
		if err != nil {
			t.Fatalf("NewID: %v", err)
		}
		if !strings.HasPrefix(id, IDPrefix) {
			t.Fatalf("NewID returned %q, want the %q prefix", id, IDPrefix)
		}
		if strings.ContainsAny(id, `/\`) {
			t.Fatalf("NewID returned %q, which is not usable as a directory name", id)
		}
		if seen[id] {
			t.Fatalf("NewID repeated %q", id)
		}
		seen[id] = true
	}
}

func TestNewStoreRequiresADirectory(t *testing.T) {
	_, err := NewStore(Options{})
	if err == nil {
		t.Fatal("NewStore accepted an empty directory")
	}
	if kind := apperrors.KindOf(err); kind != apperrors.KindConfig {
		t.Fatalf("kind = %s, want %s", kind, apperrors.KindConfig)
	}
}

func TestStoreBuildTouchesNoDisk(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "sessions")
	store := newTestStore(t, Options{Directory: directory})

	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("building a store created %s", directory)
	}
	if store.Directory() != directory {
		t.Fatalf("Directory() = %q, want %q", store.Directory(), directory)
	}
}

func TestStartWritesMetadataForARunningTask(t *testing.T) {
	store := newTestStore(t, Options{PersistMessages: true, PersistToolCalls: true})
	session := startTestSession(t, store)

	path := filepath.Join(session.Directory(), MetadataFile)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("metadata was not written: %v", err)
	}

	meta, err := readMeta(path)
	if err != nil {
		t.Fatalf("readMeta: %v", err)
	}
	if meta.SessionID != session.ID() {
		t.Fatalf("SessionID = %q, want %q", meta.SessionID, session.ID())
	}
	if meta.Status != StatusRunning {
		t.Fatalf("Status = %q, want %q", meta.Status, StatusRunning)
	}
	if meta.Finished() {
		t.Fatal("a freshly started session reports itself as finished")
	}
	if meta.Agent != "local-reviewer" || meta.ModelAlias != "local-coder" {
		t.Fatalf("metadata lost the agent identity: %+v", meta)
	}
}

func TestMessageAndToolCallAreAppendedAsJSONLines(t *testing.T) {
	store := newTestStore(t, Options{PersistMessages: true, PersistToolCalls: true})
	session := startTestSession(t, store)

	session.Message(llm.NewSystemMessage("you review Go code"))
	session.Message(llm.NewUserMessage("review internal/agent/loop.go"))
	session.Message(llm.Message{
		Role: llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{
			ID:        "call_1",
			Name:      "repo.read",
			Arguments: `{"path":"internal/agent/loop.go"}`,
		}},
	})
	session.ToolCall(
		llm.ToolCall{ID: "call_1", Name: "repo.read"},
		llm.ToolResult{
			ToolCallID: "call_1",
			Name:       "repo.read",
			Content:    "package agent",
			// The executor reports the real size and duration before truncation,
			// so the record has to read exactly these value types.
			Metadata: map[string]any{
				"duration_ms": int64(12),
				"bytes":       int(13),
			},
		},
	)

	transcript, err := store.Transcript(session.ID())
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if len(transcript) != 3 {
		t.Fatalf("transcript has %d records, want 3", len(transcript))
	}
	for index, record := range transcript {
		if record.Seq != index+1 {
			t.Fatalf("record %d has seq %d", index, record.Seq)
		}
		if record.Kind != KindMessage {
			t.Fatalf("record %d has kind %q, want %q", index, record.Kind, KindMessage)
		}
	}
	if transcript[1].Role != string(llm.RoleUser) {
		t.Fatalf("second record role = %q, want %q", transcript[1].Role, llm.RoleUser)
	}
	if got := transcript[1].Content[0].Text; got != "review internal/agent/loop.go" {
		t.Fatalf("second record text = %q", got)
	}
	if len(transcript[2].ToolCalls) != 1 || transcript[2].ToolCalls[0].Name != "repo.read" {
		t.Fatalf("the tool call was not recorded: %+v", transcript[2])
	}

	calls, err := store.ToolCalls(session.ID())
	if err != nil {
		t.Fatalf("ToolCalls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("recorded %d tool calls, want 1", len(calls))
	}
	call := calls[0]
	if call.Tool != "repo.read" || call.Bytes != 13 || call.DurationMS != 12 {
		t.Fatalf("tool call lost its metadata: %+v", call)
	}
	if call.ErrorKind != "" {
		t.Fatalf("a successful call carries error_kind %q", call.ErrorKind)
	}
}

func TestToolCallRecordsTheErrorKind(t *testing.T) {
	store := newTestStore(t, Options{PersistToolCalls: true})
	session := startTestSession(t, store)

	session.ToolCall(
		llm.ToolCall{ID: "call_1", Name: "repo.read"},
		llm.ToolResult{
			ToolCallID: "call_1",
			Name:       "repo.read",
			Content:    "outside the workspace",
			IsError:    true,
			Metadata: map[string]any{
				"error_kind": string(apperrors.KindPermission),
			},
		},
	)

	calls, err := store.ToolCalls(session.ID())
	if err != nil {
		t.Fatalf("ToolCalls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("recorded %d tool calls, want 1", len(calls))
	}
	if !calls[0].IsError || calls[0].ErrorKind != string(apperrors.KindPermission) {
		t.Fatalf("the failure was not recorded: %+v", calls[0])
	}
}

func TestPersistenceCanBeSwitchedOff(t *testing.T) {
	store := newTestStore(t, Options{})
	session := startTestSession(t, store)

	session.Message(llm.NewUserMessage("hello"))
	session.ToolCall(llm.ToolCall{ID: "call_1", Name: "repo.read"}, llm.ToolResult{})
	session.Compaction(agent.CompactionReport{Elided: 2, Changed: true})

	for _, name := range []string{MessagesFile, ToolsFile} {
		path := filepath.Join(session.Directory(), name)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s exists although persistence is off", path)
		}
	}
}

func TestCompactionIsRecordedInTheTranscript(t *testing.T) {
	store := newTestStore(t, Options{PersistMessages: true})
	session := startTestSession(t, store)

	session.Compaction(agent.CompactionReport{
		Elided:       3,
		BeforeTokens: 9000,
		AfterTokens:  2500,
		Summary:      "## Compacted history",
		Changed:      true,
	})

	transcript, err := store.Transcript(session.ID())
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if len(transcript) != 1 {
		t.Fatalf("transcript has %d records, want 1", len(transcript))
	}
	record := transcript[0]
	if record.Kind != KindCompaction || record.Elided != 3 || record.AfterTokens != 2500 {
		t.Fatalf("the compaction was not recorded: %+v", record)
	}
	if !strings.Contains(record.Describe(), "elided 3") {
		t.Fatalf("Describe() = %q", record.Describe())
	}
}

func TestImageBytesAreNotCopiedIntoTheSession(t *testing.T) {
	store := newTestStore(t, Options{PersistMessages: true})
	session := startTestSession(t, store)

	session.Message(llm.Message{
		Role: llm.RoleUser,
		Content: []llm.ContentPart{
			llm.ImageDataPart("image/png", []byte("binary-image")),
			llm.TextPart("what is wrong with this screenshot"),
		},
	})

	raw, err := os.ReadFile(filepath.Join(session.Directory(), MessagesFile))
	if err != nil {
		t.Fatalf("reading the transcript: %v", err)
	}
	if strings.Contains(string(raw), "binary-image") {
		t.Fatal("the session copied the image bytes")
	}

	transcript, err := store.Transcript(session.ID())
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if len(transcript) != 1 || len(transcript[0].Content) != 2 {
		t.Fatalf("unexpected transcript: %+v", transcript)
	}
	if transcript[0].Content[0].MIMEType != "image/png" || transcript[0].Content[0].Bytes != 12 {
		t.Fatalf("the image reference was not recorded: %+v", transcript[0].Content[0])
	}
}

func TestFinishWritesTheResultAndUpdatesTheMetadata(t *testing.T) {
	store := newTestStore(t, Options{PersistMessages: true})
	session := startTestSession(t, store)

	started := time.Now().Add(-2 * time.Second)
	session.Message(llm.NewUserMessage("review the loop"))

	result := &agent.Result{
		Status:    agent.StatusCompleted,
		Agent:     "local-reviewer",
		Provider:  "local-ollama",
		Model:     "qwen3.8:27b-mlx",
		Summary:   "one issue",
		Usage:     agent.Usage{InputTokens: 1200, OutputTokens: 300, ToolCalls: 4, Rounds: 3},
		StartedAt: started,
	}
	if err := session.Finish(result); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	record, err := store.Open(session.ID())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if record.Meta.Status != agent.StatusCompleted {
		t.Fatalf("Status = %q, want %q", record.Meta.Status, agent.StatusCompleted)
	}
	if !record.Meta.Finished() {
		t.Fatal("a finished session still reports itself as running")
	}
	if record.Meta.FinishedAt == nil {
		t.Fatal("FinishedAt was not recorded")
	}
	if record.Meta.Usage.InputTokens != 1200 || record.Meta.Usage.Rounds != 3 {
		t.Fatalf("usage was not recorded: %+v", record.Meta.Usage)
	}
	if record.Meta.Duration() <= 0 {
		t.Fatal("the duration was not derived")
	}
	if record.Result == nil {
		t.Fatal("result.json was not written")
	}
	if record.Result.Summary != "one issue" || record.Result.Usage.OutputTokens != 300 {
		t.Fatalf("result round trip lost data: %+v", record.Result)
	}

	// The result has to precede the metadata update, so that metadata always
	// describes a session whose result is already on disk.
	if _, err := os.Stat(filepath.Join(record.Directory, ResultFile)); err != nil {
		t.Fatalf("result.json is missing: %v", err)
	}
}

func TestFinishRecordsAFailure(t *testing.T) {
	store := newTestStore(t, Options{})
	session := startTestSession(t, store)

	result := &agent.Result{
		Status: agent.StatusFailed,
		Agent:  "local-reviewer",
		Error:  "the provider refused the request",
	}
	if err := session.Finish(result); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	record, err := store.Open(session.ID())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if record.Meta.Error != "the provider refused the request" {
		t.Fatalf("Error = %q", record.Meta.Error)
	}
}

func TestOpenRejectsIdentifiersThatEscapeTheStore(t *testing.T) {
	store := newTestStore(t, Options{})

	for _, id := range []string{"", "   ", "../../etc/passwd", "sessions/agt_1", "agt_a/b", "other"} {
		_, err := store.Open(id)
		if err == nil {
			t.Fatalf("Open(%q) was accepted", id)
		}
		if kind := apperrors.KindOf(err); kind != apperrors.KindInvalidArgument {
			t.Fatalf("Open(%q) kind = %s, want %s", id, kind, apperrors.KindInvalidArgument)
		}
	}
}

func TestOpenReportsAnUnknownSession(t *testing.T) {
	store := newTestStore(t, Options{})

	_, err := store.Open("agt_0000000000000000")
	if err == nil {
		t.Fatal("Open accepted an unknown session")
	}
	// A missing session and an unreadable one are both "not found" as far as a
	// caller is concerned: the identifier is valid, the session is not there.
	if kind := apperrors.KindOf(err); kind != apperrors.KindNotFound {
		t.Fatalf("kind = %s, want %s", kind, apperrors.KindNotFound)
	}
}

func TestListIsNewestFirstAndSkipsDamagedSessions(t *testing.T) {
	store := newTestStore(t, Options{PersistMessages: true})

	older := startTestSession(t, store)
	if err := older.Finish(&agent.Result{Status: agent.StatusCompleted, Agent: "local-reviewer"}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	newer := startTestSession(t, store)
	if err := newer.Finish(&agent.Result{Status: agent.StatusCompleted, Agent: "local-reviewer"}); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	// A directory with a broken metadata file, and one that is not a session at
	// all, must not make the list unusable.
	damaged := filepath.Join(store.Directory(), "agt_ffffffffffffffff")
	if err := os.MkdirAll(damaged, DirectoryMode); err != nil {
		t.Fatalf("creating the damaged session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(damaged, MetadataFile), []byte("{"), FileMode); err != nil {
		t.Fatalf("writing the damaged metadata: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(store.Directory(), "not-a-session"), DirectoryMode); err != nil {
		t.Fatalf("creating the foreign directory: %v", err)
	}

	records, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("List returned %d records, want 2", len(records))
	}
	if records[0].ID != newer.ID() {
		t.Fatalf("first record = %q, want the newest %q", records[0].ID, newer.ID())
	}
	if records[1].ID != older.ID() {
		t.Fatalf("second record = %q, want %q", records[1].ID, older.ID())
	}
	if records[0].Directory == "" || records[0].Meta.Agent != "local-reviewer" {
		t.Fatalf("unexpected record: %+v", records[0])
	}
}

func TestPruneKeepsTheNewestSessions(t *testing.T) {
	store := newTestStore(t, Options{Retention: 2})

	ids := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		session := startTestSession(t, store)
		ids = append(ids, session.ID())
		// Distinct start times, so that "newest" is unambiguous.
		time.Sleep(5 * time.Millisecond)
	}

	removed, err := store.Prune()
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 2 {
		t.Fatalf("Prune removed %d sessions, want 2", removed)
	}

	records, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("retention kept %d sessions, want 2", len(records))
	}
	for _, record := range records {
		if record.ID == ids[0] || record.ID == ids[1] {
			t.Fatalf("retention kept the old session %q", record.ID)
		}
	}

	// Retention of zero means unlimited, which is the default.
	unlimited := newTestStore(t, Options{})
	for i := 0; i < 3; i++ {
		startTestSession(t, unlimited)
	}
	if removed, err := unlimited.Prune(); err != nil || removed != 0 {
		t.Fatalf("Prune with unlimited retention removed %d sessions (err %v)", removed, err)
	}
}

func TestPruneIgnoresForeignDirectories(t *testing.T) {
	store := newTestStore(t, Options{Retention: 1})

	foreign := filepath.Join(store.Directory(), "keep-me")
	if err := os.MkdirAll(foreign, DirectoryMode); err != nil {
		t.Fatalf("creating the foreign directory: %v", err)
	}

	first := startTestSession(t, store)
	time.Sleep(5 * time.Millisecond)
	second := startTestSession(t, store)

	// Finishing the newest session triggers the retention sweep, which must
	// only ever consider directories that look like sessions.
	if err := second.Finish(&agent.Result{Status: agent.StatusCompleted, Agent: "local-reviewer"}); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("Prune removed a directory it does not own: %v", err)
	}
	records, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(records) != 1 || records[0].ID != second.ID() {
		t.Fatalf("retention kept %+v, want only %q (first was %q)", records, second.ID(), first.ID())
	}
}

func TestWriteFailuresAreRememberedAndNeverFatal(t *testing.T) {
	store := newTestStore(t, Options{PersistMessages: true})
	session := startTestSession(t, store)

	// A read-only session directory is the cheapest way to make the append fail.
	if err := os.Chmod(session.Directory(), 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(session.Directory(), DirectoryMode) })

	// Recording must never panic or block; it only remembers the failure.
	session.Message(llm.NewUserMessage("this append cannot succeed"))
	if session.WriteError() == nil {
		t.Skip("the filesystem allowed the write, so the failure path cannot be exercised")
	}

	// The failure is reported, but only after the caller had a chance to use
	// the answer: a full disk must not look like a failed task.
	err := session.Finish(&agent.Result{Status: agent.StatusCompleted, Agent: "local-reviewer"})
	if err == nil {
		t.Fatal("Finish did not report the incomplete record")
	}
	if kind := apperrors.KindOf(err); kind != apperrors.KindInternal {
		t.Fatalf("kind = %s, want %s", kind, apperrors.KindInternal)
	}
}

func TestReadJSONLAbsentFileIsNoRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), MessagesFile)
	records, err := readJSONL[TranscriptRecord](path)
	if err != nil {
		t.Fatalf("readJSONL: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("readJSONL returned %d records for a missing file", len(records))
	}
}

func TestDescribeRendersEveryRecordKind(t *testing.T) {
	message := newTranscriptRecord(1, time.Now(), llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{
			llm.TextPart("first line\nsecond line"),
		},
		ToolCalls: []llm.ToolCall{{
			ID:        "call_1",
			Name:      "repo.read",
			Arguments: `{"path":"internal/agent/loop.go"}`,
		}},
	})
	described := message.Describe()
	if !strings.Contains(described, "assistant") {
		t.Fatalf("Describe() = %q, want the role", described)
	}
	if !strings.Contains(described, "repo.read(internal/agent/loop.go)") {
		t.Fatalf("Describe() = %q, want the tool call with its path", described)
	}
	if strings.Contains(described, "second line") {
		t.Fatalf("Describe() = %q, want a single line", described)
	}

	empty := TranscriptRecord{Kind: KindMessage, Role: string(llm.RoleAssistant)}
	if empty.Describe() == "" {
		t.Fatal("Describe() returned nothing for an empty message")
	}

	unknown := TranscriptRecord{Kind: "future", Seq: 9}
	if !strings.Contains(unknown.Describe(), "future") {
		t.Fatalf("Describe() = %q, want the unknown kind", unknown.Describe())
	}
}

func TestReadJSONLRejectsAMalformedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), MessagesFile)
	if err := os.WriteFile(path, []byte("{\"kind\":\"message\"}\nnot json\n"), FileMode); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	if _, err := readJSONL[TranscriptRecord](path); err == nil {
		t.Fatal("readJSONL accepted a malformed line")
	}
}

func TestMetaDurationPrefersTheRecordedValue(t *testing.T) {
	started := time.Now().Add(-10 * time.Second)
	finished := started.Add(5 * time.Second)

	meta := Meta{StartedAt: started, FinishedAt: &finished, DurationMS: 1234}
	if got := meta.Duration(); got != 1234*time.Millisecond {
		t.Fatalf("Duration() = %s, want the recorded 1234ms", got)
	}

	meta.DurationMS = 0
	if got := meta.Duration(); got != 5*time.Second {
		t.Fatalf("Duration() = %s, want 5s", got)
	}

	running := Meta{StartedAt: started, Status: StatusRunning}
	if got := running.Duration(); got != 0 {
		t.Fatalf("Duration() = %s, want 0 for a running session", got)
	}
}
