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
	"io/fs"
	"strings"

	"github.com/pawagents/pawagents/internal/llm"
	"github.com/pawagents/pawagents/internal/tools"
)

// StatToolName is the identifier of the metadata tool.
const StatToolName = "repo.stat"

// Stat reports metadata about a path.
type Stat struct{}

// Name implements tools.Tool.
func (Stat) Name() string { return StatToolName }

// Definition implements tools.Tool.
func (Stat) Definition() llm.ToolDefinition {
	return tools.NewDefinition(
		StatToolName,
		"Report metadata about a workspace path: type, size, permissions, "+
			"modification time and, for a text file, the line count. Use it to "+
			"decide whether a file is worth reading before spending context on it.",
		map[string]llm.JSONSchema{
			"path": llm.StringSchema().WithDescription("File or directory path, relative to the workspace root or absolute inside it."),
		},
		"path",
	)
}

// Execute implements tools.Tool.
func (Stat) Execute(ctx context.Context, raw json.RawMessage, env tools.Env) (tools.Result, error) {
	if err := ctx.Err(); err != nil {
		return tools.Result{}, ctxError("repo.stat", err)
	}

	arguments, err := tools.DecodeArguments(raw)
	if err != nil {
		return tools.Result{}, err
	}

	path, err := arguments.String("path", true)
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

	info, err := guard.Stat(path)
	if err != nil {
		return tools.Result{}, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "path:     %s\n", display(path))
	fmt.Fprintf(&b, "type:     %s\n", describeType(info))
	fmt.Fprintf(&b, "size:     %s (%d bytes)\n", humanBytes(info.Size()), info.Size())
	fmt.Fprintf(&b, "mode:     %s\n", info.Mode().Perm())
	fmt.Fprintf(&b, "modified: %s\n", formatTime(info.ModTime()))

	metadata := map[string]any{
		"path":     resolved,
		"relative": display(path),
		"size":     info.Size(),
		"mode":     info.Mode().String(),
		"modified": formatTime(info.ModTime()),
		"is_dir":   info.IsDir(),
	}

	if !info.IsDir() && info.Size() > 0 {
		if data, err := guard.ReadFile(path); err == nil {
			if looksBinary(data) {
				b.WriteString("content:  binary\n")
				metadata["binary"] = true
			} else {
				lines := strings.Count(string(data), "\n")
				if len(data) > 0 && data[len(data)-1] != '\n' {
					lines++
				}
				fmt.Fprintf(&b, "lines:    %d\n", lines)
				metadata["lines"] = lines
			}
		}
	}

	return tools.Result{Content: b.String(), Metadata: metadata}, nil
}

// describeType names what kind of entry a path is.
func describeType(info fs.FileInfo) string {
	switch {
	case info.IsDir():
		return "directory"
	case info.Mode()&fs.ModeSymlink != 0:
		return "symlink"
	case info.Mode().IsRegular():
		return "file"
	default:
		return "special"
	}
}
