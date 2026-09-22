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

// Package tools implements the read-only tool runtime.
//
// Every tool is a small, single-purpose reader. The runtime deliberately ships
// no writer, no shell and no network tool: an external subagent analyses a
// workspace and reports, while the host agent stays responsible for changing
// files. That division is the core safety property of PawAgents, so a new tool
// that mutates state does not belong in this package.
package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/llm"
	"github.com/pawagents/pawagents/internal/security"
)

// Result is what a tool returns to the agent loop.
type Result struct {
	// Content is the text handed to the model.
	Content string
	// Truncated reports whether the runtime cut the content.
	Truncated bool
	// Metadata carries structured extras. It is never sent to a model.
	Metadata map[string]any
}

// Env is the environment a tool runs in.
//
// Tools never reach for a global: everything they may touch is injected, which
// is what makes them testable and keeps the workspace guard unavoidable.
type Env struct {
	// Workspace is the guarded directory tree.
	Workspace *security.Workspace
	// MaxOutputBytes caps a single tool result.
	MaxOutputBytes int
	// Logger receives tool diagnostics.
	Logger *slog.Logger
}

// Logger returns the environment logger, never nil.
func (e Env) Log() *slog.Logger {
	if e.Logger == nil {
		return slog.Default()
	}
	return e.Logger
}

// Tool is a single capability an agent may be granted.
type Tool interface {
	// Name is the "namespace.tool" identifier.
	Name() string
	// Definition describes the tool to a model.
	Definition() llm.ToolDefinition
	// Execute runs the tool. Arguments are the raw JSON object produced by
	// the model; a malformed payload is a ToolError, while a failure of the
	// operation itself is reported through Result.
	Execute(ctx context.Context, arguments json.RawMessage, env Env) (Result, error)
}

// NewDefinition builds a tool definition from its properties.
//
// The description is the strongest lever on tool selection quality, so every
// tool says what it does and when to prefer it over a sibling.
func NewDefinition(name, description string, properties map[string]llm.JSONSchema, required ...string) llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        name,
		Description: description,
		InputSchema: llm.ObjectSchema(properties, required...),
	}
}

// Registry holds the tools a runtime can offer.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

// Register adds a tool. A duplicate name panics: tool names are a contract
// with the model, and a silent overwrite would change behaviour invisibly.
func (r *Registry) Register(tool Tool) {
	if tool == nil {
		panic("tools: Register requires a tool")
	}
	name := tool.Name()
	if name == "" {
		panic("tools: Register requires a tool name")
	}
	if _, exists := r.tools[name]; exists {
		panic("tools: tool " + name + " is already registered")
	}
	r.tools[name] = tool
}

// Lookup returns a tool by name.
func (r *Registry) Lookup(name string) (Tool, bool) {
	tool, ok := r.tools[strings.TrimSpace(name)]
	return tool, ok
}

// Names returns every registered tool name, sorted.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.tools))
	for name := range r.tools {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Len returns how many tools are registered.
func (r *Registry) Len() int { return len(r.tools) }

// Definitions returns the definitions of the named tools, in the given order.
// An unknown name is an error, because an agent profile that grants a tool the
// runtime does not have would otherwise fail later with a confusing message.
func (r *Registry) Definitions(names []string) ([]llm.ToolDefinition, error) {
	definitions := make([]llm.ToolDefinition, 0, len(names))
	var unknown []string

	for _, name := range names {
		tool, ok := r.Lookup(name)
		if !ok {
			unknown = append(unknown, name)
			continue
		}
		definitions = append(definitions, tool.Definition())
	}

	if len(unknown) > 0 {
		available := r.Names()
		return nil, apperrors.New(apperrors.KindCapability, "tools.definitions",
			"unknown tool(s): %s", strings.Join(unknown, ", ")).
			WithDetails(map[string]any{"unknown": unknown, "available": available})
	}

	if err := llm.ValidateToolDefinitions(definitions); err != nil {
		return nil, err
	}
	return definitions, nil
}
