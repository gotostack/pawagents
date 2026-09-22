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

// Package builtin registers the tools the PawAgents runtime ships with.
//
// It lives in its own package so that internal/tools stays free of imports
// that point back at it: the tool interfaces are in internal/tools, the
// implementations are in internal/tools/repo and internal/tools/git, and this
// package is the single composition point.
package builtin

import (
	"log/slog"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/security"
	"github.com/pawagents/pawagents/internal/tools"
	"github.com/pawagents/pawagents/internal/tools/git"
	"github.com/pawagents/pawagents/internal/tools/repo"
)

// ToolNames returns every tool name this package registers, sorted.
//
// It is used by the tests and by the documentation generator so that the
// configuration validator, the runtime and the README cannot drift apart.
func ToolNames() []string {
	return []string{
		repo.ReadToolName,
		repo.ListToolName,
		repo.SearchToolName,
		repo.StatToolName,
		git.DiffToolName,
		git.ShowToolName,
		git.StatusToolName,
		git.LogToolName,
	}
}

// NewRegistry builds a registry holding every built-in tool.
func NewRegistry() *tools.Registry {
	registry := tools.NewRegistry()
	Register(registry)
	return registry
}

// Register adds every built-in tool to a registry. It panics on a duplicate
// name, which can only happen if two packages claim the same tool.
func Register(registry *tools.Registry) {
	if registry == nil {
		panic("builtin: Register requires a registry")
	}
	registry.Register(repo.Read{})
	registry.Register(repo.List{})
	registry.Register(repo.Search{})
	registry.Register(repo.Stat{})
	registry.Register(git.Diff{})
	registry.Register(git.Show{})
	registry.Register(git.Status{})
	registry.Register(git.Log{})
}

// Environment builds the tool environment for a workspace.
func Environment(workspace *security.Workspace, maxOutputBytes int, logger *slog.Logger) (tools.Env, error) {
	if workspace == nil {
		return tools.Env{}, apperrors.New(apperrors.KindInternal, "tools.environment",
			"the tool runtime needs a workspace guard")
	}
	if maxOutputBytes <= 0 {
		maxOutputBytes = tools.DefaultMaxOutputBytes
	}
	return tools.Env{
		Workspace:      workspace,
		MaxOutputBytes: maxOutputBytes,
		Logger:         logger,
	}, nil
}
