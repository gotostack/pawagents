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
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/llm"
	"github.com/pawagents/pawagents/internal/tools"
)

// SearchToolName is the identifier of the content search tool.
const SearchToolName = "repo.search"

// MaxSearchFiles caps how many files one search reads, which bounds the work a
// single call can cost.
const MaxSearchFiles = 20000

// Search finds a pattern in the workspace.
type Search struct{}

// Name implements tools.Tool.
func (Search) Name() string { return SearchToolName }

// Definition implements tools.Tool.
func (Search) Definition() llm.ToolDefinition {
	return tools.NewDefinition(
		SearchToolName,
		"Search the workspace for a text pattern and return matching lines as "+
			"path:line: text. This is the tool to reach for when you do not yet "+
			"know which file contains what you need. Set regex to true for a Go "+
			"regular expression, file_glob to narrow the search, and path to "+
			"search a subtree. Binary and oversized files are skipped.",
		map[string]llm.JSONSchema{
			"query":          llm.StringSchema().WithDescription("Text or regular expression to look for in each line."),
			"path":           llm.StringSchema().WithDescription("Subtree to search. Defaults to the workspace root."),
			"regex":          llm.BooleanSchema().WithDescription("Treat query as a Go regular expression. Defaults to false."),
			"case_sensitive": llm.BooleanSchema().WithDescription("Match case sensitively. Defaults to false."),
			"file_glob":      llm.StringSchema().WithDescription("Only search files matching this glob, for example *.go."),
			"max_results":    llm.IntegerSchema().WithDescription("Maximum matching lines to return, up to 200. Defaults to 100."),
			"max_files":      llm.IntegerSchema().WithDescription("Maximum files to read. Defaults to 20000."),
		},
		"query",
	)
}

// Execute implements tools.Tool.
func (Search) Execute(ctx context.Context, raw json.RawMessage, env tools.Env) (tools.Result, error) {
	if err := ctx.Err(); err != nil {
		return tools.Result{}, ctxError("repo.search", err)
	}

	arguments, err := tools.DecodeArguments(raw)
	if err != nil {
		return tools.Result{}, err
	}

	query, err := arguments.String("query", true)
	if err != nil {
		return tools.Result{}, err
	}
	if strings.TrimSpace(query) == "" {
		return tools.Result{}, apperrors.New(apperrors.KindTool, "repo.search",
			"query must not be empty")
	}
	subtree, err := arguments.String("path", false)
	if err != nil {
		return tools.Result{}, err
	}
	useRegex, err := arguments.Bool("regex", false)
	if err != nil {
		return tools.Result{}, err
	}
	caseSensitive, err := arguments.Bool("case_sensitive", false)
	if err != nil {
		return tools.Result{}, err
	}
	glob, err := arguments.String("file_glob", false)
	if err != nil {
		return tools.Result{}, err
	}
	maxResults, err := arguments.Int("max_results", 100, 1, MaxSearchResults)
	if err != nil {
		return tools.Result{}, err
	}
	maxFiles, err := arguments.Int("max_files", MaxSearchFiles, 1, MaxSearchFiles)
	if err != nil {
		return tools.Result{}, err
	}
	if err := arguments.RejectUnknown(); err != nil {
		return tools.Result{}, err
	}

	matcher, err := buildMatcher(query, useRegex, caseSensitive)
	if err != nil {
		return tools.Result{}, err
	}

	guard, err := workspace(env)
	if err != nil {
		return tools.Result{}, err
	}

	start := subtree
	if strings.TrimSpace(start) == "" {
		start = "."
	}
	if _, err := guard.Resolve(start); err != nil {
		return tools.Result{}, err
	}

	var (
		matches      []string
		filesRead    int
		filesSkipped int
		truncated    bool
	)

	// The guard performs the traversal, so hidden entries, symlinks, blocked
	// paths and oversized files are excluded by the same rules repo.list and
	// repo.read obey.
	walkErr := guard.Walk(start, func(relative string, info fs.FileInfo) error {
		if err := ctx.Err(); err != nil {
			return ctxError("repo.search", err)
		}
		if len(matches) >= maxResults {
			truncated = true
			return errStopWalk
		}
		if !matchGlob(glob, relative) {
			return nil
		}
		if filesRead >= maxFiles {
			truncated = true
			return errStopWalk
		}

		data, err := guard.ReadFile(relative)
		if err != nil {
			filesSkipped++
			return nil
		}
		if looksBinary(data) {
			filesSkipped++
			return nil
		}
		filesRead++

		for index, line := range strings.Split(string(data), "\n") {
			if !matcher(line) {
				continue
			}
			if len(matches) >= maxResults {
				truncated = true
				return errStopWalk
			}
			text := line
			if len(text) > MaxLineLength {
				text = text[:MaxLineLength] + "…"
			}
			matches = append(matches, fmt.Sprintf("%s:%d: %s", display(relative), index+1, strings.TrimSpace(text)))
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errStopWalk) {
		return tools.Result{}, walkErr
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d match%s in %d file%s scanned\n",
		len(matches), pluralSuffix(len(matches), "", "es"),
		filesRead, pluralSuffix(filesRead, "", "s"))
	for _, match := range matches {
		b.WriteString(match)
		b.WriteString("\n")
	}
	if truncated {
		fmt.Fprintf(&b, "... [stopped at the search limit; narrow the query, set file_glob, or search a subtree]\n")
	}
	if len(matches) == 0 {
		b.WriteString("no match found\n")
	}

	return tools.Result{
		Content:   b.String(),
		Truncated: truncated,
		Metadata: map[string]any{
			"matches":       len(matches),
			"files_read":    filesRead,
			"files_skipped": filesSkipped,
			"path":          start,
		},
	}, nil
}

// buildMatcher builds the line predicate for a search.
func buildMatcher(query string, useRegex, caseSensitive bool) (func(string) bool, error) {
	if !useRegex {
		if caseSensitive {
			return func(line string) bool { return strings.Contains(line, query) }, nil
		}
		lowered := strings.ToLower(query)
		return func(line string) bool { return strings.Contains(strings.ToLower(line), lowered) }, nil
	}

	pattern := query
	if !caseSensitive {
		pattern = "(?i)" + pattern
	}
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil, apperrors.Wrap(apperrors.KindTool, "repo.search",
			"the regular expression is invalid", err)
	}
	return compiled.MatchString, nil
}
