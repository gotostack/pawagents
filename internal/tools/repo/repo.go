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

// Package repo implements the read-only repository tools: read, list, search
// and stat.
//
// Every path goes through the workspace guard, so these tools cannot escape the
// workspace root, cannot follow a symlink out of it and cannot read a path the
// configuration blocks. The output is formatted for a model to cite: line
// numbers for reads and searches, sizes and modification times for metadata.
package repo

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/security"
	"github.com/pawagents/pawagents/internal/tools"
)

// Limits shared by the repository tools.
const (
	// MaxReadBytes is the largest amount of a file repo.read returns in one
	// call, independent of the workspace read cap.
	MaxReadBytes = 256 * 1024
	// MaxLineLength caps a single line so that a minified file cannot consume
	// the whole budget with one line.
	MaxLineLength = 1000
	// MaxSearchResults caps how many matches repo.search returns.
	MaxSearchResults = 200
	// MaxListEntries caps how many entries repo.list returns.
	MaxListEntries = 500
	// MaxWalkDepth caps the recursion depth of list and search.
	MaxWalkDepth = 8
)

// environment returns the workspace of an execution, refusing to run without
// one: a repository tool without a guard would read the whole filesystem.
func workspace(env tools.Env) (*security.Workspace, error) {
	if env.Workspace == nil {
		return nil, apperrors.New(apperrors.KindInternal, "repo.workspace",
			"the tool runtime has no workspace guard")
	}
	return env.Workspace, nil
}

// display renders a path for output. Control characters are removed because a
// crafted file name could otherwise inject lines into the text a model reads.
func display(relative string) string {
	cleaned := security.SanitizeName(strings.TrimPrefix(relative, "./"))
	if cleaned == "" || cleaned == "." {
		return "."
	}
	return cleaned
}

// numbered renders lines with a right aligned line number, which is what lets
// a finding cite file:line.
func numbered(lines []string, firstLine, width int) string {
	var b strings.Builder
	for index, line := range lines {
		number := firstLine + index
		if len(line) > MaxLineLength {
			line = line[:MaxLineLength] + "…"
		}
		fmt.Fprintf(&b, "%*d|%s\n", width, number, line)
	}
	return b.String()
}

// numberWidth returns the width of the largest line number.
func numberWidth(lastLine int) int {
	width := 1
	for value := lastLine; value >= 10; value /= 10 {
		width++
	}
	return width
}

// looksBinary reports whether a buffer contains a null byte, which is the
// cheapest reliable signal that a file is not text.
func looksBinary(data []byte) bool {
	limit := len(data)
	if limit > 8000 {
		limit = 8000
	}
	for _, b := range data[:limit] {
		if b == 0 {
			return true
		}
	}
	return false
}

// formatTime renders a modification time in a stable, readable form.
func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// matchGlob reports whether a path matches a glob pattern, accepting either the
// base name or the whole relative path so that "*.c" and "lib/*.c" both work.
func matchGlob(pattern, relative string) bool {
	if strings.TrimSpace(pattern) == "" {
		return true
	}
	if ok, err := path.Match(pattern, relative); err == nil && ok {
		return true
	}
	base := relative
	if index := strings.LastIndexByte(relative, '/'); index >= 0 {
		base = relative[index+1:]
	}
	ok, err := path.Match(pattern, base)
	return err == nil && ok
}

// summarise builds the one line header of a tool result.
func summarise(parts ...string) string {
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			filtered = append(filtered, part)
		}
	}
	return strings.Join(filtered, " ")
}

// errStopWalk stops a workspace walk early. It is not reported as a failure:
// hitting a result limit is a normal outcome of a search.
var errStopWalk = errors.New("stop walk")

// humanBytes renders a byte count for a tool result.
func humanBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	divisor, exponent := int64(unit), 0
	for value := size / unit; value >= unit; value /= unit {
		divisor *= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %ciB", float64(size)/float64(divisor), "KMGTPE"[exponent])
}

// ctxError converts a context failure into a classified error so that a
// cancelled or timed out tool call is distinguishable from a tool failure.
func ctxError(op string, err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return apperrors.Wrap(apperrors.KindTimeout, op, "the tool call timed out", err)
	case errors.Is(err, context.Canceled):
		return apperrors.Wrap(apperrors.KindCancelled, op, "the tool call was cancelled", err)
	default:
		return apperrors.Wrap(apperrors.KindInternal, op, "unexpected context error", err)
	}
}
