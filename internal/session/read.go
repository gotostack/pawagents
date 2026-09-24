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
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pawagents/pawagents/internal/apperrors"
)

// maxRecordBytes bounds one recorded line. Tool results are truncated before
// they are recorded, so a larger value here only guards against a corrupt
// file consuming unbounded memory.
const maxRecordBytes = 8 << 20

// Transcript returns the recorded messages of a session, oldest first.
//
// A missing transcript is not an error: message persistence can be switched
// off in the configuration, and an empty transcript is then the honest answer.
func (s *Store) Transcript(id string) ([]TranscriptRecord, error) {
	record, err := s.Open(id)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(record.Directory, MessagesFile)
	records, err := readJSONL[TranscriptRecord](path)
	if err != nil {
		return nil, apperrors.Wrap(apperrors.KindInternal, "session.Transcript",
			fmt.Sprintf("cannot read the transcript of session %s", id), err)
	}
	return records, nil
}

// ToolCalls returns the recorded tool calls of a session, oldest first.
//
// As with Transcript, a missing file yields no calls rather than an error.
func (s *Store) ToolCalls(id string) ([]ToolCallRecord, error) {
	record, err := s.Open(id)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(record.Directory, ToolsFile)
	records, err := readJSONL[ToolCallRecord](path)
	if err != nil {
		return nil, apperrors.Wrap(apperrors.KindInternal, "session.ToolCalls",
			fmt.Sprintf("cannot read the tool calls of session %s", id), err)
	}
	return records, nil
}

// readJSONL decodes one JSON object per line. A missing file is empty.
func readJSONL[T any](path string) ([]T, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = file.Close() }()

	var records []T
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), maxRecordBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record T
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, fmt.Errorf("line %d: %w", len(records)+1, err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

// Describe renders a one line summary of a record. It exists so that the CLI
// can present a transcript without knowing the wire shape.
func (r TranscriptRecord) Describe() string {
	switch r.Kind {
	case KindCompaction:
		return fmt.Sprintf("compaction  elided %d results (%d -> %d tokens)",
			r.Elided, r.BeforeTokens, r.AfterTokens)
	case KindMessage:
		return fmt.Sprintf("%-11s %s", r.Role, r.text())
	default:
		return fmt.Sprintf("%-11s %s", r.Kind, r.text())
	}
}

// text flattens the content parts into a single line of plain text.
func (r TranscriptRecord) text() string {
	parts := make([]string, 0, len(r.Content)+2)
	for _, content := range r.Content {
		switch {
		case content.Text != "":
			// Each part is flattened before it is joined: a message that
			// spans lines must not hide the tool call that follows it.
			parts = append(parts, firstLine(content.Text))
		case content.Kind == "image":
			parts = append(parts, fmt.Sprintf("[image %s, %d bytes]",
				content.MIMEType, content.Bytes))
		}
	}
	if len(r.ToolCalls) > 0 {
		names := make([]string, 0, len(r.ToolCalls))
		for _, call := range r.ToolCalls {
			names = append(names, callLabel(call))
		}
		parts = append(parts, "called "+strings.Join(names, ", "))
	}
	if r.ToolResult != nil {
		result := r.ToolResult
		status := fmt.Sprintf("%d bytes", len(result.Content))
		if result.IsError {
			status = "error: " + status
		}
		parts = append(parts, fmt.Sprintf("-> %s (%s)", result.Name, status))
	}
	if len(parts) == 0 {
		return "(empty)"
	}
	return firstLine(strings.Join(parts, " "))
}

// callLabel renders a tool call with the argument that identifies the target.
func callLabel(call ToolCallEntry) string {
	if call.Arguments == "" {
		return call.Name
	}
	var arguments map[string]any
	if err := json.Unmarshal([]byte(call.Arguments), &arguments); err != nil {
		return call.Name
	}
	for _, key := range []string{"path", "file", "directory", "query", "ref"} {
		if value, ok := arguments[key].(string); ok && value != "" {
			return fmt.Sprintf("%s(%s)", call.Name, firstLine(value))
		}
	}
	return call.Name
}

// firstLine returns the first non empty line of text, clipped to a readable
// length so that one verbose message cannot dominate the transcript view.
func firstLine(text string) string {
	const limit = 160
	line := strings.TrimSpace(text)
	if index := strings.IndexByte(line, '\n'); index >= 0 {
		line = strings.TrimSpace(line[:index])
	}
	if len(line) > limit {
		line = strings.TrimSpace(line[:limit]) + "..."
	}
	return line
}

// Duration reports how long the session ran, measured from its start.
func (m Meta) Duration() time.Duration {
	if m.DurationMS > 0 {
		return time.Duration(m.DurationMS) * time.Millisecond
	}
	if m.StartedAt.IsZero() {
		return 0
	}
	if m.FinishedAt != nil {
		return m.FinishedAt.Sub(m.StartedAt)
	}
	return 0
}
