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

package git

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/pawagents/pawagents/internal/llm"
	"github.com/pawagents/pawagents/internal/security"
	"github.com/pawagents/pawagents/internal/tools"
)

// Tool names.
const (
	DiffToolName   = "git.diff"
	ShowToolName   = "git.show"
	StatusToolName = "git.status"
	LogToolName    = "git.log"
)

// Diff shows the differences of a working tree.
type Diff struct{}

// Name implements tools.Tool.
func (Diff) Name() string { return DiffToolName }

// Definition implements tools.Tool.
func (Diff) Definition() llm.ToolDefinition {
	return tools.NewDefinition(
		DiffToolName,
		"Show a unified diff of the repository, by default the unstaged changes "+
			"against the index. Set staged to review what is already added, or "+
			"ref to compare against a commit, branch or range such as HEAD~3 or "+
			"main...HEAD. This is the tool to start a review with.",
		map[string]llm.JSONSchema{
			"path":          llm.StringSchema().WithDescription("Limit the diff to this file or directory."),
			"staged":        llm.BooleanSchema().WithDescription("Show the staged changes instead of the unstaged ones."),
			"ref":           llm.StringSchema().WithDescription("Revision or range to compare against, for example HEAD~1 or main...HEAD."),
			"context_lines": llm.IntegerSchema().WithDescription("Lines of context around each change, 0 to 10. Defaults to 3."),
			"stat":          llm.BooleanSchema().WithDescription("Return only the diffstat summary."),
		},
	)
}

// Execute implements tools.Tool.
func (Diff) Execute(ctx context.Context, raw json.RawMessage, env tools.Env) (tools.Result, error) {
	runner, workspace, arguments, err := prepare(ctx, raw, env, "git.diff")
	if err != nil {
		return tools.Result{}, err
	}

	path, err := arguments.String("path", false)
	if err != nil {
		return tools.Result{}, err
	}
	staged, err := arguments.Bool("staged", false)
	if err != nil {
		return tools.Result{}, err
	}
	ref, err := arguments.String("ref", false)
	if err != nil {
		return tools.Result{}, err
	}
	contextLines, err := arguments.Int("context_lines", 3, 0, MaxContextLines)
	if err != nil {
		return tools.Result{}, err
	}
	statOnly, err := arguments.Bool("stat", false)
	if err != nil {
		return tools.Result{}, err
	}
	if err := arguments.RejectUnknown(); err != nil {
		return tools.Result{}, err
	}

	validRef, err := ValidateRef(ref)
	if err != nil {
		return tools.Result{}, err
	}
	validPath, err := runner.ValidatePath(workspace, path)
	if err != nil {
		return tools.Result{}, err
	}

	args := []string{"diff", "--no-color", "--unified=" + strconv.Itoa(contextLines)}
	if statOnly {
		args = append(args, "--stat")
	}
	if staged {
		args = append(args, "--cached")
	}
	if validRef != "" {
		args = append(args, validRef)
	}
	if validPath != "" {
		// "--" stops option parsing, so a path that begins with a dash stays a
		// path.
		args = append(args, "--", validPath)
	}

	output, err := runner.Run(ctx, args...)
	if err != nil {
		return tools.Result{}, err
	}

	return describe("git.diff", output, validRef, validPath, staged), nil
}

// Show displays one revision.
type Show struct{}

// Name implements tools.Tool.
func (Show) Name() string { return ShowToolName }

// Definition implements tools.Tool.
func (Show) Definition() llm.ToolDefinition {
	return tools.NewDefinition(
		ShowToolName,
		"Show the commit message and the diff of one revision, for example HEAD "+
			"or a commit hash. Use it to understand what a change intended to do "+
			"before judging whether it does it.",
		map[string]llm.JSONSchema{
			"ref":           llm.StringSchema().WithDescription("Revision to show. Defaults to HEAD."),
			"path":          llm.StringSchema().WithDescription("Limit the output to this file or directory."),
			"context_lines": llm.IntegerSchema().WithDescription("Lines of context around each change, 0 to 10. Defaults to 3."),
		},
	)
}

// Execute implements tools.Tool.
func (Show) Execute(ctx context.Context, raw json.RawMessage, env tools.Env) (tools.Result, error) {
	runner, workspace, arguments, err := prepare(ctx, raw, env, "git.show")
	if err != nil {
		return tools.Result{}, err
	}

	ref, err := arguments.String("ref", false)
	if err != nil {
		return tools.Result{}, err
	}
	path, err := arguments.String("path", false)
	if err != nil {
		return tools.Result{}, err
	}
	contextLines, err := arguments.Int("context_lines", 3, 0, MaxContextLines)
	if err != nil {
		return tools.Result{}, err
	}
	if err := arguments.RejectUnknown(); err != nil {
		return tools.Result{}, err
	}

	validRef, err := ValidateRef(ref)
	if err != nil {
		return tools.Result{}, err
	}
	if validRef == "" {
		validRef = "HEAD"
	}
	validPath, err := runner.ValidatePath(workspace, path)
	if err != nil {
		return tools.Result{}, err
	}

	args := []string{"show", "--no-color", "--unified=" + strconv.Itoa(contextLines), validRef}
	if validPath != "" {
		args = append(args, "--", validPath)
	}

	output, err := runner.Run(ctx, args...)
	if err != nil {
		return tools.Result{}, err
	}

	return describe("git.show", output, validRef, validPath, false), nil
}

// Status reports the working tree state.
type Status struct{}

// Name implements tools.Tool.
func (Status) Name() string { return StatusToolName }

// Definition implements tools.Tool.
func (Status) Definition() llm.ToolDefinition {
	return tools.NewDefinition(
		StatusToolName,
		"Report the branch, the upstream tracking information and every changed "+
			"path, in the short porcelain format: XY path, where X is the index "+
			"status and Y the working tree status. Use it to see the full scope "+
			"of a change, including untracked and renamed files.",
		map[string]llm.JSONSchema{},
	)
}

// Execute implements tools.Tool.
func (Status) Execute(ctx context.Context, raw json.RawMessage, env tools.Env) (tools.Result, error) {
	runner, _, arguments, err := prepare(ctx, raw, env, "git.status")
	if err != nil {
		return tools.Result{}, err
	}
	if err := arguments.RejectUnknown(); err != nil {
		return tools.Result{}, err
	}

	output, err := runner.Run(ctx, "status", "--porcelain=v1", "--branch", "--untracked-files=all")
	if err != nil {
		return tools.Result{}, err
	}

	return tools.Result{
		Content:   output,
		Metadata:  map[string]any{"tool": "git.status"},
		Truncated: strings.Contains(output, "[git output truncated"),
	}, nil
}

// Log lists recent commits.
type Log struct{}

// Name implements tools.Tool.
func (Log) Name() string { return LogToolName }

// Definition implements tools.Tool.
func (Log) Definition() llm.ToolDefinition {
	return tools.NewDefinition(
		LogToolName,
		"List recent commits, one line each, as hash, date, author and subject. "+
			"Use it to find when a behaviour changed, then read the commit with "+
			"git.show. Set path to follow the history of one file.",
		map[string]llm.JSONSchema{
			"limit": llm.IntegerSchema().WithDescription("How many commits to return, 1 to 200. Defaults to 20."),
			"path":  llm.StringSchema().WithDescription("Only commits that touched this file or directory."),
			"ref":   llm.StringSchema().WithDescription("Revision or range to walk. Defaults to HEAD."),
		},
	)
}

// Execute implements tools.Tool.
func (Log) Execute(ctx context.Context, raw json.RawMessage, env tools.Env) (tools.Result, error) {
	runner, workspace, arguments, err := prepare(ctx, raw, env, "git.log")
	if err != nil {
		return tools.Result{}, err
	}

	limit, err := arguments.Int("limit", 20, 1, 200)
	if err != nil {
		return tools.Result{}, err
	}
	path, err := arguments.String("path", false)
	if err != nil {
		return tools.Result{}, err
	}
	ref, err := arguments.String("ref", false)
	if err != nil {
		return tools.Result{}, err
	}
	if err := arguments.RejectUnknown(); err != nil {
		return tools.Result{}, err
	}

	validRef, err := ValidateRef(ref)
	if err != nil {
		return tools.Result{}, err
	}
	validPath, err := runner.ValidatePath(workspace, path)
	if err != nil {
		return tools.Result{}, err
	}

	args := []string{"log", "--no-color", "-n", strconv.Itoa(limit),
		"--pretty=format:%h %ad %an %s", "--date=short"}
	if validRef != "" {
		args = append(args, validRef)
	}
	if validPath != "" {
		args = append(args, "--", validPath)
	} else {
		args = append(args, "--")
	}

	output, err := runner.Run(ctx, args...)
	if err != nil {
		return tools.Result{}, err
	}

	return describe("git.log", output, validRef, validPath, false), nil
}

// prepare decodes the arguments shared by every git tool.
func prepare(ctx context.Context, raw json.RawMessage, env tools.Env, op string) (*Runner, *security.Workspace, *tools.Arguments, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, contextError(op, err)
	}
	if env.Workspace == nil {
		return nil, nil, nil, missingWorkspace(op)
	}

	arguments, err := tools.DecodeArguments(raw)
	if err != nil {
		return nil, nil, nil, err
	}
	runner, err := NewRunner(env.Workspace)
	if err != nil {
		return nil, nil, nil, err
	}
	return runner, env.Workspace, arguments, nil
}

// describe wraps git output with a header so the model knows what it is
// looking at, and reports whether the output was cut.
func describe(tool, output, ref, path string, staged bool) tools.Result {
	var header strings.Builder
	header.WriteString(tool)
	if ref != "" {
		fmt.Fprintf(&header, " %s", ref)
	}
	if staged {
		header.WriteString(" --cached")
	}
	if path != "" {
		fmt.Fprintf(&header, " -- %s", path)
	}

	content := output
	if strings.TrimSpace(output) == "" {
		content = "no changes\n"
	}

	truncated := strings.Contains(content, "[git output truncated")

	return tools.Result{
		Content:   fmt.Sprintf("%s\n%s", header.String(), content),
		Truncated: truncated,
		Metadata: map[string]any{
			"tool": tool,
			"ref":  ref,
			"path": path,
		},
	}
}
