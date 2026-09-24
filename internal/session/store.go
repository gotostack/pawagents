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

	"github.com/pawagents/pawagents/internal/agent"
	"github.com/pawagents/pawagents/internal/apperrors"
)

// Options configures a store.
type Options struct {
	// Directory is the root of the session store. It is created on demand.
	Directory string
	// PersistMessages writes messages.jsonl. False keeps metadata and result
	// only, which is what a privacy sensitive deployment may prefer.
	PersistMessages bool
	// PersistToolCalls writes tools.jsonl.
	PersistToolCalls bool
	// Retention caps how many sessions stay on disk. Zero means unlimited.
	Retention int
	// Logger receives store diagnostics.
	Logger *slog.Logger
}

// Store owns the session directory.
//
// Building a store touches no disk: the directory is created when the first
// session is written, so that a process which never delegates anything leaves
// no trace, and so that a misconfigured path is reported by the run that needs
// it rather than by every command.
type Store struct {
	directory string
	messages  bool
	tools     bool
	retention int
	logger    *slog.Logger
}

// NewStore builds a store from its options.
func NewStore(options Options) (*Store, error) {
	directory := strings.TrimSpace(options.Directory)
	if directory == "" {
		return nil, apperrors.New(apperrors.KindConfig, "session.store",
			"sessions.directory is not configured")
	}

	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Store{
		directory: directory,
		messages:  options.PersistMessages,
		tools:     options.PersistToolCalls,
		retention: options.Retention,
		logger:    logger,
	}, nil
}

// Directory returns the root of the store.
func (s *Store) Directory() string { return s.directory }

// Start opens a session for a task.
//
// The metadata is written immediately, so an interrupted task is still visible
// in `pagent session list` with its agent and model, and a reader can tell a
// running session from a finished one.
func (s *Store) Start(meta Meta) (*Session, error) {
	id, err := NewID()
	if err != nil {
		return nil, err
	}
	meta.SessionID = id

	directory := filepath.Join(s.directory, id)
	if err := os.MkdirAll(directory, DirectoryMode); err != nil {
		return nil, apperrors.Wrap(apperrors.KindWorkspace, "session.start",
			"cannot create the session directory %s", err, directory)
	}

	session := &Session{
		store:     s,
		directory: directory,
		meta:      meta,
		logger:    s.logger,
	}
	if err := session.writeMetadata(); err != nil {
		_ = os.RemoveAll(directory)
		return nil, err
	}

	return session, nil
}

// Open reads a session back.
//
// The transcript is not decoded here: `session show` decides whether it wants
// the messages, and a caller that only wants the outcome should not pay for
// reading megabytes of tool output.
func (s *Store) Open(id string) (*Record, error) {
	clean := strings.TrimSpace(id)
	if clean == "" {
		return nil, apperrors.New(apperrors.KindInvalidArgument, "session.open",
			"a session identifier is required")
	}
	if !strings.HasPrefix(clean, IDPrefix) || strings.ContainsAny(clean, `/\`) {
		return nil, apperrors.New(apperrors.KindInvalidArgument, "session.open",
			"%q is not a session identifier", clean)
	}

	directory := filepath.Join(s.directory, clean)
	meta, err := readMeta(filepath.Join(directory, MetadataFile))
	if err != nil {
		return nil, apperrors.Wrap(apperrors.KindNotFound, "session.open",
			"session %q was not found in %s", err, clean, s.directory)
	}

	record := &Record{ID: meta.SessionID, Directory: directory, Meta: meta}
	if record.ID == "" {
		record.ID = clean
	}

	raw, err := readResult(filepath.Join(directory, ResultFile))
	if err != nil {
		return nil, err
	}
	if raw != nil {
		var result agent.Result
		if err := decodeResult(*raw, &result); err != nil {
			return nil, err
		}
		record.Result = &result
	}

	return record, nil
}

// List returns the recorded sessions, newest first.
//
// It reads one metadata file per session and skips a directory whose metadata
// cannot be parsed: one damaged session must not make the list unusable.
func (s *Store) List() ([]Record, error) {
	directories, err := sessionDirectories(s.directory)
	if err != nil {
		return nil, err
	}

	metas := make(map[string]Meta, len(directories))
	records := make([]Record, 0, len(directories))

	for _, directory := range directories {
		meta, err := readMeta(filepath.Join(directory, MetadataFile))
		if err != nil {
			s.logger.Warn("skipping a session whose metadata cannot be read",
				slog.String("directory", directory),
				slog.String("error", err.Error()))
			continue
		}
		metas[directory] = meta
	}

	sortNewestFirst(s.directory, directories, metas)

	for _, directory := range directories {
		meta, ok := metas[directory]
		if !ok {
			continue
		}
		records = append(records, Record{
			ID:        meta.SessionID,
			Directory: directory,
			Meta:      meta,
		})
	}

	return records, nil
}

// Prune keeps the newest sessions and removes the rest.
//
// It is called after a session finishes, so the session that just completed is
// never the one that gets pruned away.
func (s *Store) Prune() (int, error) {
	if s.retention <= 0 {
		return 0, nil
	}

	records, err := s.List()
	if err != nil {
		return 0, err
	}
	if len(records) <= s.retention {
		return 0, nil
	}

	removed := 0
	for _, record := range records[s.retention:] {
		if err := os.RemoveAll(record.Directory); err != nil {
			s.logger.Warn("cannot remove an expired session",
				slog.String("session", record.ID),
				slog.String("error", err.Error()))
			continue
		}
		removed++
	}

	if removed > 0 {
		s.logger.Debug("pruned expired sessions",
			slog.Int("removed", removed),
			slog.Int("retention", s.retention))
	}
	return removed, nil
}
