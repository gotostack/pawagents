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

package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/llm"
	"github.com/pawagents/pawagents/internal/tools"
)

// ReadToolName is the identifier of the file reading tool.
const ReadToolName = "repo.read"

// Read reads a text file from the workspace.
type Read struct{}

// Name implements tools.Tool.
func (Read) Name() string { return ReadToolName }

// Definition implements tools.Tool.
func (Read) Definition() llm.ToolDefinition {
	return tools.NewDefinition(
		ReadToolName,
		"Read a text file from the workspace and return its content with line "+
			"numbers, so a finding can cite path:line. Use start_line and end_line "+
			"to read a range of a large file, and repo.search instead when you do "+
			"not know which file contains what you need. Binary files and files "+
			"larger than the configured limit are refused.",
		map[string]llm.JSONSchema{
			"path":       llm.StringSchema().WithDescription("File path, relative to the workspace root or absolute inside it."),
			"start_line": llm.IntegerSchema().WithDescription("First line to return, 1 based. Defaults to the first line."),
			"end_line":   llm.IntegerSchema().WithDescription("Last line to return, inclusive. Defaults to the last line."),
			"max_bytes":  llm.IntegerSchema().WithDescription("Maximum bytes to return. Defaults to 262144."),
		},
		"path",
	)
}

// Execute implements tools.Tool.
func (Read) Execute(ctx context.Context, raw json.RawMessage, env tools.Env) (tools.Result, error) {
	if err := ctx.Err(); err != nil {
		return tools.Result{}, ctxError("repo.read", err)
	}

	arguments, err := tools.DecodeArguments(raw)
	if err != nil {
		return tools.Result{}, err
	}

	path, err := arguments.String("path", true)
	if err != nil {
		return tools.Result{}, err
	}
	startLine, err := arguments.Int("start_line", 1, 1, 0)
	if err != nil {
		return tools.Result{}, err
	}
	endLine, err := arguments.Int("end_line", 0, 0, 0)
	if err != nil {
		return tools.Result{}, err
	}
	maxBytes, err := arguments.Int("max_bytes", MaxReadBytes, 1024, MaxReadBytes)
	if err != nil {
		return tools.Result{}, err
	}
	if err := arguments.RejectUnknown(); err != nil {
		return tools.Result{}, err
	}

	guard, err := workspace(env)
	if err != nil {
		return tools.Result{}, err
	}

	resolved, err := guard.Resolve(path)
	if err != nil {
		return tools.Result{}, err
	}

	data, err := guard.ReadFile(path)
	if err != nil {
		return tools.Result{}, err
	}
	if looksBinary(data) {
		return tools.Result{
			Content: fmt.Sprintf("%s looks like a binary file and was not read", display(path)),
			Metadata: map[string]any{
				"binary": true,
				"bytes":  len(data),
			},
		}, nil
	}

	lines := strings.Split(string(data), "\n")
	// A trailing newline produces one empty trailing element, which is not a
	// line of the file.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	total := len(lines)

	if startLine > total && total > 0 {
		return tools.Result{}, apperrors.New(apperrors.KindTool, "repo.read",
			"start_line %d is past the end of %s, which has %d lines",
			startLine, display(path), total)
	}
	if endLine == 0 || endLine > total {
		endLine = total
	}
	if endLine < startLine {
		return tools.Result{}, apperrors.New(apperrors.KindTool, "repo.read",
			"end_line %d is before start_line %d", endLine, startLine)
	}

	selected := lines[startLine-1 : endLine]
	firstLine := startLine
	width := numberWidth(endLine)

	header := fmt.Sprintf("%s (lines %d-%d of %d)\n", display(path), firstLine, endLine, total)

	var b strings.Builder
	b.WriteString(header)
	b.WriteString(numbered(selected, firstLine, width))

	content := b.String()
	truncated := false
	if len(content) > maxBytes {
		content = cutLines(content, maxBytes)
		truncated = true
		content += fmt.Sprintf("\n... [truncated at %d bytes; read the rest with start_line/end_line]", maxBytes)
	}

	return tools.Result{
		Content:   content,
		Truncated: truncated,
		Metadata: map[string]any{
			"path":       resolved,
			"relative":   display(path),
			"lines":      total,
			"returned":   len(selected),
			"start_line": firstLine,
			"end_line":   endLine,
		},
	}, nil
}

// cutLines truncates a numbered block on a line boundary.
func cutLines(content string, limit int) string {
	if len(content) <= limit {
		return content
	}
	cut := content[:limit]
	if index := strings.LastIndexByte(cut, '\n'); index > limit/2 {
		cut = cut[:index]
	}
	return cut
}
