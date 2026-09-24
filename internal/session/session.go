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
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/pawagents/pawagents/internal/agent"
	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/llm"
)

// Session is the record of one running task.
//
// It implements the recorder the agent loop writes to, so the loop needs no
// knowledge of files or formats.
type Session struct {
	store     *Store
	directory string
	logger    *slog.Logger

	mu     sync.Mutex
	meta   Meta
	seq    int
	closed bool

	messages *os.File
	tools    *os.File

	// writeErr remembers the first failure, so that a task is not failed by a
	// full disk but the caller is still told that the record is incomplete.
	writeErr error
}

// ID returns the session identifier.
func (s *Session) ID() string { return s.meta.SessionID }

// Directory returns the directory the session writes to.
func (s *Session) Directory() string { return s.directory }

// Meta returns a copy of the current metadata.
func (s *Session) Meta() Meta {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.meta
}

// SetModel records which provider and model actually served the task.
//
// The metadata is written before the model is resolved, so a fallback target
// has to be able to correct it afterwards: a session that claims the configured
// model answered when a fallback did would be misleading.
func (s *Session) SetModel(provider, model string) {
	s.mu.Lock()
	s.meta.Provider = provider
	s.meta.Model = model
	s.mu.Unlock()
}

// Message records one conversation turn.
func (s *Session) Message(message llm.Message) {
	if !s.store.messages {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	record := newTranscriptRecord(s.nextSeq(), time.Now(), message)
	s.writeLine(&s.messages, MessagesFile, record)
	s.meta.Messages++
}

// ToolCall records one tool call and its outcome.
func (s *Session) ToolCall(call llm.ToolCall, result llm.ToolResult) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.meta.ToolCalls++
	if !s.store.tools {
		return
	}

	record := newToolCallRecord(s.nextSeq(), time.Now(), call, result)
	s.writeLine(&s.tools, ToolsFile, record)
}

// Compaction records that the conversation was reduced to fit the window.
//
// It goes into the transcript rather than a file of its own because it is part
// of the conversation the model saw: a reader replaying a session has to know
// that earlier tool output was elided, or the transcript would look as if the
// model had answered with less evidence than it actually had.
func (s *Session) Compaction(report agent.CompactionReport) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.meta.Compactions++
	if !s.store.messages {
		return
	}

	record := newCompactionRecord(s.nextSeq(), time.Now(), report)
	s.writeLine(&s.messages, MessagesFile, record)
}

// Finish writes the outcome and closes the session.
//
// It is safe to call once; a second call returns the remembered write error.
func (s *Session) Finish(result *agent.Result) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	finished := time.Now()
	s.meta.FinishedAt = &finished

	if result != nil {
		s.meta.Status = result.Status
		s.meta.Error = result.Error
		s.meta.Usage = result.Usage
		s.meta.Provider = firstNonEmpty(result.Provider, s.meta.Provider)
		s.meta.Model = firstNonEmpty(result.Model, s.meta.Model)
		s.meta.SessionID = firstNonEmpty(result.SessionID, s.meta.SessionID)
		if !result.StartedAt.IsZero() {
			s.meta.StartedAt = result.StartedAt
		}
	}
	if s.meta.Status == "" {
		s.meta.Status = StatusRunning
	}
	s.meta.DurationMS = finished.Sub(s.meta.StartedAt).Milliseconds()

	// The result is written first: a reader that finds metadata.json without a
	// result knows the task did not finish, while the reverse would look like a
	// finished task with no outcome.
	if result != nil {
		s.writeResult(result)
	}
	s.writeMetadataLocked()

	s.closeLocked()

	if s.writeErr != nil {
		return apperrors.Wrap(apperrors.KindInternal, "session.finish",
			"the session record is incomplete", s.writeErr)
	}

	// Pruning is best effort: failing to remove an old session must not turn a
	// completed task into an error.
	if _, err := s.store.Prune(); err != nil {
		s.logger.Warn("cannot prune the session store",
			slog.String("error", err.Error()))
	}

	return nil
}

// WriteError returns the first write failure, or nil.
func (s *Session) WriteError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeErr
}

// nextSeq returns the next record sequence number. The caller holds the lock.
//
// Messages and tool calls draw from one counter, so the numbers interleave
// across both files and a reader can merge the two into a single timeline. The
// gaps this leaves in either file are expected, not a lost record.
func (s *Session) nextSeq() int {
	s.seq++
	return s.seq
}

// writeLine appends one JSON record to a file, opening it on first use.
//
// The caller holds the lock. A failure is remembered and logged but never
// propagates into the agent loop: an answer that was produced must not be
// discarded because a transcript could not be written.
func (s *Session) writeLine(file **os.File, name string, record any) {
	if s.writeErr != nil {
		return
	}

	encoded, err := encode(record)
	if err != nil {
		s.fail("cannot encode a session record", name, err)
		return
	}

	if *file == nil {
		handle, err := os.OpenFile(filepath.Join(s.directory, name),
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, FileMode)
		if err != nil {
			s.fail("cannot open the session file", name, err)
			return
		}
		*file = handle
	}

	if _, err := (*file).Write(encoded); err != nil {
		s.fail("cannot write the session file", name, err)
	}
}

// writeResult writes the structured result envelope. The caller holds the lock.
func (s *Session) writeResult(result *agent.Result) {
	if s.writeErr != nil {
		return
	}

	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		s.fail("cannot encode the result", ResultFile, err)
		return
	}
	s.writeFile(ResultFile, append(encoded, '\n'))
}

// writeMetadata rewrites metadata.json. The caller holds the lock.
func (s *Session) writeMetadataLocked() {
	if s.writeErr != nil {
		return
	}

	encoded, err := json.MarshalIndent(s.meta, "", "  ")
	if err != nil {
		s.fail("cannot encode the metadata", MetadataFile, err)
		return
	}
	s.writeFile(MetadataFile, append(encoded, '\n'))
}

// writeMetadata writes the metadata of a freshly created session.
func (s *Session) writeMetadata() error {
	if err := s.writeFileError(MetadataFile, s.meta); err != nil {
		return err
	}
	return nil
}

// writeFile replaces a file through a temporary one, so that a reader never
// sees a half written document.
func (s *Session) writeFile(name string, data []byte) {
	_ = s.writeFileError(name, data)
}

// writeFileError replaces a file and reports the failure.
func (s *Session) writeFileError(name string, data any) error {
	target := filepath.Join(s.directory, name)
	temporary := target + ".tmp"

	encoded, ok := data.([]byte)
	if !ok {
		buffer, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return apperrors.Wrap(apperrors.KindInternal, "session.write",
				"cannot encode the session metadata", err)
		}
		encoded = append(buffer, '\n')
	}

	if err := os.WriteFile(temporary, encoded, FileMode); err != nil {
		s.fail("cannot write the session file", name, err)
		return apperrors.Wrap(apperrors.KindInternal, "session.write",
			"cannot write the session file %s", err, name)
	}
	if err := os.Rename(temporary, target); err != nil {
		s.fail("cannot replace the session file", name, err)
		return apperrors.Wrap(apperrors.KindInternal, "session.write",
			"cannot replace the session file %s", err, name)
	}
	return nil
}

// closeLocked releases the open files. The caller holds the lock.
func (s *Session) closeLocked() {
	if s.closed {
		return
	}
	s.closed = true

	for _, handle := range []*os.File{s.messages, s.tools} {
		if handle == nil {
			continue
		}
		if err := handle.Close(); err != nil {
			s.logger.Debug("cannot close a session file",
				slog.String("error", err.Error()))
		}
	}
	s.messages, s.tools = nil, nil
}

// fail remembers and reports a write failure. The caller holds the lock.
func (s *Session) fail(message, name string, err error) {
	if s.writeErr == nil {
		s.writeErr = err
	}
	s.logger.Warn(message+" (the task continues without a complete record)",
		slog.String("session", s.meta.SessionID),
		slog.String("file", name),
		slog.String("error", err.Error()))
}

// decodeResult decodes a stored result envelope.
func decodeResult(raw json.RawMessage, out *agent.Result) error {
	if err := json.Unmarshal(raw, out); err != nil {
		return apperrors.Wrap(apperrors.KindInternal, "session.read",
			"the session result is not valid JSON", err)
	}
	return nil
}

// firstNonEmpty returns the first value that is not empty.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
