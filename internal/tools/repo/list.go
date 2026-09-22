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
	"path"
	"sort"
	"strings"

	"github.com/pawagents/pawagents/internal/llm"
	"github.com/pawagents/pawagents/internal/tools"
)

// ListToolName is the identifier of the directory listing tool.
const ListToolName = "repo.list"

// List enumerates directory entries.
type List struct{}

// Name implements tools.Tool.
func (List) Name() string { return ListToolName }

// Definition implements tools.Tool.
func (List) Definition() llm.ToolDefinition {
	return tools.NewDefinition(
		ListToolName,
		"List the entries of a workspace directory, optionally several levels "+
			"deep. Use it to discover the layout of a project before reading "+
			"files, or after repo.search to see what else lives next to a hit. "+
			"Directories are marked with a trailing slash.",
		map[string]llm.JSONSchema{
			"path":           llm.StringSchema().WithDescription("Directory to list. Defaults to the workspace root."),
			"depth":          llm.IntegerSchema().WithDescription("How many levels to descend, 1 to 4. Defaults to 1."),
			"file_glob":      llm.StringSchema().WithDescription("Only include entries whose name matches this glob, for example *.go."),
			"max_entries":    llm.IntegerSchema().WithDescription("Maximum entries to return, up to 500. Defaults to 500."),
			"include_hidden": llm.BooleanSchema().WithDescription("Include dot files and dot directories. Defaults to false."),
		},
		"path",
	)
}

// Execute implements tools.Tool.
func (List) Execute(ctx context.Context, raw json.RawMessage, env tools.Env) (tools.Result, error) {
	if err := ctx.Err(); err != nil {
		return tools.Result{}, ctxError("repo.list", err)
	}

	arguments, err := tools.DecodeArguments(raw)
	if err != nil {
		return tools.Result{}, err
	}

	directory, err := arguments.String("path", false)
	if err != nil {
		return tools.Result{}, err
	}
	depth, err := arguments.Int("depth", 1, 1, 4)
	if err != nil {
		return tools.Result{}, err
	}
	glob, err := arguments.String("file_glob", false)
	if err != nil {
		return tools.Result{}, err
	}
	maxEntries, err := arguments.Int("max_entries", MaxListEntries, 1, MaxListEntries)
	if err != nil {
		return tools.Result{}, err
	}
	includeHidden, err := arguments.Bool("include_hidden", false)
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

	root, err := guard.Resolve(directory)
	if err != nil {
		return tools.Result{}, err
	}

	entries := make([]string, 0, 64)
	truncated := false

	var walk func(relative string, level int) error
	walk = func(relative string, level int) error {
		if level > depth || len(entries) >= maxEntries {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return ctxError("repo.list", err)
		}

		children, err := guard.ReadDir(relative)
		if err != nil {
			return err
		}
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })

		directories := make([]string, 0, 8)
		for _, child := range children {
			if len(entries) >= maxEntries {
				truncated = true
				return nil
			}

			name := child.Name()
			if strings.HasPrefix(name, ".") && !includeHidden && name != "." && name != ".." {
				continue
			}
			if name == ".git" {
				continue
			}

			relativeChild := path.Join(relative, name)
			if glob != "" && !matchGlob(glob, relativeChild) {
				// A directory may still contain matches, so it is descended
				// into even when its own name does not match.
				if child.IsDir() {
					directories = append(directories, relativeChild)
				}
				continue
			}

			if child.IsDir() {
				entries = append(entries, display(relativeChild)+"/")
				directories = append(directories, relativeChild)
				continue
			}

			info, err := child.Info()
			if err != nil {
				continue
			}
			if info.Mode()&0o111 != 0 && info.Mode().IsRegular() {
				entries = append(entries, fmt.Sprintf("%s (executable, %s)", display(relativeChild), humanBytes(info.Size())))
				continue
			}
			entries = append(entries, fmt.Sprintf("%s (%s)", display(relativeChild), humanBytes(info.Size())))
		}

		if level == depth {
			return nil
		}
		for _, child := range directories {
			if err := walk(child, level+1); err != nil {
				return err
			}
		}
		return nil
	}

	start := directory
	if strings.TrimSpace(start) == "" {
		start = "."
	}
	if err := walk(start, 1); err != nil {
		return tools.Result{}, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d entr%s\n", display(start), len(entries), pluralSuffix(len(entries), "y", "ies"))
	for _, entry := range entries {
		b.WriteString(entry)
		b.WriteString("\n")
	}
	if truncated {
		fmt.Fprintf(&b, "... [stopped at the %d entry limit; raise max_entries or list a subdirectory]\n", maxEntries)
	}

	return tools.Result{
		Content:   b.String(),
		Truncated: truncated,
		Metadata: map[string]any{
			"directory": root,
			"depth":     depth,
			"entries":   len(entries),
		},
	}, nil
}

// pluralSuffix picks the singular or plural suffix for a count.
func pluralSuffix(count int, singular, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
}
