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
	"strings"
	"time"

	"github.com/pawagents/pawagents/internal/agent"
	"github.com/pawagents/pawagents/internal/llm"
)

// File names inside a session directory.
const (
	// MetadataFile describes the task.
	MetadataFile = "metadata.json"
	// MessagesFile holds the conversation, one JSON object per line.
	MessagesFile = "messages.jsonl"
	// ToolsFile holds the tool calls, one JSON object per line.
	ToolsFile = "tools.jsonl"
	// ResultFile holds the structured result envelope.
	ResultFile = "result.json"
)

// Record kinds used in messages.jsonl.
const (
	// KindMessage is a conversation turn.
	KindMessage = "message"
	// KindCompaction marks a context compaction.
	KindCompaction = "compaction"
)

// Status values recorded while a task is in flight, in addition to the task
// statuses the runtime reports.
const (
	// StatusRunning is the status of a session whose task has not finished.
	StatusRunning = "running"
)

// Meta is the metadata document of a session.
//
// It is written twice: once when the session is created, so that a crash still
// leaves the task identifiable, and once when it finishes, with the outcome and
// the counters. That is what makes `pagent session list` able to show an
// interrupted run instead of losing it.
type Meta struct {
	SessionID  string `json:"session_id"`
	Agent      string `json:"agent"`
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
	ModelAlias string `json:"model_alias,omitempty"`
	Workspace  string `json:"workspace,omitempty"`
	OutputMode string `json:"output_mode,omitempty"`

	Status string `json:"status"`

	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	DurationMS int64      `json:"duration_ms,omitempty"`

	Usage agent.Usage `json:"usage"`

	// Tools lists the tools the agent was granted, so that a reader can see
	// what the subagent was allowed to touch.
	Tools []string `json:"tools,omitempty"`
	// MaxRounds is the round budget the task ran with.
	MaxRounds int `json:"max_rounds,omitempty"`

	// Messages, ToolCalls and Compactions count what was recorded. They are
	// derived from the files, but the list command needs them without reading
	// the whole transcript.
	Messages    int `json:"messages"`
	ToolCalls   int `json:"tool_calls"`
	Compactions int `json:"compactions"`

	// Version is the build that produced the session.
	Version string `json:"version,omitempty"`
	// Error carries the failure message when the task failed.
	Error string `json:"error,omitempty"`
}

// Finished reports whether the task reached a terminal state.
func (m Meta) Finished() bool { return m.Status != StatusRunning && m.Status != "" }

// Record is a session read back from disk.
type Record struct {
	// ID is the session identifier.
	ID string `json:"session_id"`
	// Directory is the absolute path of the session directory.
	Directory string `json:"directory"`
	// Meta is the metadata document.
	Meta Meta `json:"metadata"`
	// Result is the structured result, when the task finished.
	Result *agent.Result `json:"result,omitempty"`
}

// TranscriptRecord is one line of messages.jsonl.
//
// The wire shape is defined here rather than by marshalling llm.Message, so
// that the file format stays stable when the internal protocol changes. Image
// bytes are deliberately not written: a session records what was said, and
// copying a user's image payload into a second file on disk would duplicate
// data the workspace already holds.
type TranscriptRecord struct {
	Kind       string           `json:"kind"`
	Seq        int              `json:"seq"`
	At         time.Time        `json:"at"`
	Role       string           `json:"role,omitempty"`
	Content    []ContentRecord  `json:"content,omitempty"`
	ToolCalls  []ToolCallEntry  `json:"tool_calls,omitempty"`
	ToolResult *ToolResultEntry `json:"tool_result,omitempty"`

	// Compaction fields, set when Kind is KindCompaction.
	Elided       int    `json:"elided,omitempty"`
	BeforeTokens int    `json:"before_tokens,omitempty"`
	AfterTokens  int    `json:"after_tokens,omitempty"`
	Summary      string `json:"summary,omitempty"`
}

// ContentRecord is one part of a message.
type ContentRecord struct {
	Kind string `json:"kind"`
	Text string `json:"text,omitempty"`
	// MIMEType and Bytes describe an image the session does not copy.
	MIMEType string `json:"mime_type,omitempty"`
	Bytes    int    `json:"bytes,omitempty"`
	URL      string `json:"url,omitempty"`
}

// ToolCallEntry is a tool call the model requested.
type ToolCallEntry struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
}

// ToolResultEntry is the outcome of a tool call.
type ToolResultEntry struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name,omitempty"`
	Content    string `json:"content,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
}

// ToolCallRecord is one line of tools.jsonl.
type ToolCallRecord struct {
	Kind       string    `json:"kind"`
	Seq        int       `json:"seq"`
	At         time.Time `json:"at"`
	Tool       string    `json:"tool"`
	Arguments  string    `json:"arguments,omitempty"`
	CallID     string    `json:"call_id,omitempty"`
	OK         bool      `json:"ok"`
	IsError    bool      `json:"is_error,omitempty"`
	Truncated  bool      `json:"truncated,omitempty"`
	Bytes      int       `json:"bytes"`
	DurationMS int64     `json:"duration_ms,omitempty"`
	ErrorKind  string    `json:"error_kind,omitempty"`
}

// newTranscriptRecord converts one protocol message into a record.
func newTranscriptRecord(seq int, at time.Time, message llm.Message) TranscriptRecord {
	record := TranscriptRecord{
		Kind: KindMessage,
		Seq:  seq,
		At:   at,
		Role: string(message.Role),
	}

	for _, part := range message.Content {
		switch part.Kind {
		case llm.ContentText:
			record.Content = append(record.Content, ContentRecord{
				Kind: string(llm.ContentText),
				Text: part.Text,
			})
		case llm.ContentThinking:
			record.Content = append(record.Content, ContentRecord{
				Kind: string(llm.ContentThinking),
				Text: part.Text,
			})
		case llm.ContentImage:
			if part.Image == nil {
				continue
			}
			record.Content = append(record.Content, ContentRecord{
				Kind:     string(llm.ContentImage),
				MIMEType: part.Image.MIMEType,
				Bytes:    len(part.Image.Data),
				URL:      part.Image.URL,
			})
		}
	}

	for _, call := range message.ToolCalls {
		record.ToolCalls = append(record.ToolCalls, ToolCallEntry{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: call.Arguments,
		})
	}

	if message.ToolResult != nil {
		record.ToolResult = &ToolResultEntry{
			ToolCallID: message.ToolResult.ToolCallID,
			Name:       message.ToolResult.Name,
			Content:    message.ToolResult.Content,
			IsError:    message.ToolResult.IsError,
			Truncated:  message.ToolResult.Truncated,
		}
	}

	return record
}

// newToolCallRecord converts a tool call and its result into a record.
func newToolCallRecord(seq int, at time.Time, call llm.ToolCall, result llm.ToolResult) ToolCallRecord {
	record := ToolCallRecord{
		Kind:      "tool_call",
		Seq:       seq,
		At:        at,
		Tool:      call.Name,
		Arguments: call.Arguments,
		CallID:    call.ID,
		OK:        true,
		IsError:   result.IsError,
		Truncated: result.Truncated,
		Bytes:     len(result.Content),
	}
	if result.Name != "" {
		record.Tool = result.Name
	}
	if result.IsError {
		record.OK = false
	}

	// The executor records how long the tool took and how large its output was
	// before truncation, which is far more useful than what the model saw.
	if value, ok := result.Metadata["duration_ms"].(int64); ok {
		record.DurationMS = value
	}
	if value, ok := result.Metadata["bytes"].(int); ok {
		record.Bytes = value
	}
	if value, ok := result.Metadata["error_kind"].(string); ok {
		record.ErrorKind = value
	}

	return record
}

// newCompactionRecord converts a compaction report into a record.
func newCompactionRecord(seq int, at time.Time, report agent.CompactionReport) TranscriptRecord {
	return TranscriptRecord{
		Kind:         KindCompaction,
		Seq:          seq,
		At:           at,
		Elided:       report.Elided,
		BeforeTokens: report.BeforeTokens,
		AfterTokens:  report.AfterTokens,
		Summary:      strings.TrimSpace(report.Summary),
	}
}

// encode renders a record as one JSON line.
func encode(record any) ([]byte, error) {
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}
