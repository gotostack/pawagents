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

// Package session persists the record of a delegated task.
//
// Every task that a host delegates produces one session directory:
//
//	~/.pawagents/sessions/<session-id>/
//	    metadata.json    what ran, on which model, with which outcome
//	    messages.jsonl   the conversation, one record per line
//	    tools.jsonl      the tool calls, one record per line
//	    result.json      the structured result envelope
//
// The record exists for three reasons. It is the audit trail a user needs to
// see what a subagent actually did with their repository; it is what
// `continue_task` will replay; and it is written incrementally, so a task that
// is interrupted still leaves its metadata and the messages up to that point on
// disk rather than nothing at all.
//
// Two rules shape the implementation. Persistence never fails a task: a write
// error is logged and remembered, and the agent answer is still returned,
// because a full disk must not turn a successful review into an error. And a
// session never contains a credential: the runtime reads credentials from the
// environment at request time and never puts them in a message, which is what
// keeps the transcript safe to read.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pawagents/pawagents/internal/apperrors"
)

// IDPrefix marks a session identifier, as documented in the specification.
const IDPrefix = "agt_"

// idBytes is how much randomness an identifier carries. Eight bytes make a
// collision between two sessions of the same user practically impossible while
// keeping the identifier readable in a report.
const idBytes = 8

// NewID returns a fresh session identifier.
func NewID() (string, error) {
	buffer := make([]byte, idBytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", apperrors.Wrap(apperrors.KindInternal, "session.id",
			"cannot generate a session identifier", err)
	}
	return IDPrefix + hex.EncodeToString(buffer), nil
}

// DirectoryMode and FileMode are the permissions used for a session, which a
// transcript of repository content does not need to share.
const (
	DirectoryMode = 0o700
	FileMode      = 0o600
)

// Meta returns the metadata of a session without reading its transcript.
func readMeta(path string) (Meta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Meta{}, apperrors.Wrap(apperrors.KindNotFound, "session.read",
			"cannot read the session metadata", err)
	}

	var meta Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		return Meta{}, apperrors.Wrap(apperrors.KindInternal, "session.read",
			"the session metadata is not valid JSON", err)
	}
	return meta, nil
}

// readResult reads the structured result of a finished session.
func readResult(path string) (*json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, apperrors.Wrap(apperrors.KindInternal, "session.read",
			"cannot read the session result", err)
	}

	raw := json.RawMessage(data)
	return &raw, nil
}

// sessionDirectories lists the session directories under a root, newest first.
//
// Only directories whose name carries the identifier prefix are returned: a
// retention pass deletes directories, and deleting something a user put in the
// sessions directory by hand would be unforgivable.
func sessionDirectories(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, apperrors.Wrap(apperrors.KindInternal, "session.list",
			"cannot read the session directory", err)
	}

	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), IDPrefix) {
			continue
		}
		out = append(out, filepath.Join(root, entry.Name()))
	}
	return out, nil
}

// sortNewestFirst orders sessions by their start time, falling back to the
// directory modification time for a session whose metadata cannot be read.
func sortNewestFirst(root string, directories []string, metas map[string]Meta) {
	sort.SliceStable(directories, func(i, j int) bool {
		left := startTimeOf(root, directories[i], metas)
		right := startTimeOf(root, directories[j], metas)
		if left.Equal(right) {
			return directories[i] > directories[j]
		}
		return left.After(right)
	})
}

// startTimeOf returns the recorded start time, or the directory time.
func startTimeOf(root string, directory string, metas map[string]Meta) time.Time {
	if meta, ok := metas[directory]; ok && !meta.StartedAt.IsZero() {
		return meta.StartedAt
	}
	if info, err := os.Stat(directory); err == nil {
		return info.ModTime()
	}
	return time.Time{}
}
