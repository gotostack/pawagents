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

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/llm"
	"github.com/pawagents/pawagents/internal/security"
)

// DefaultMaxOutputBytes caps a single tool result when the environment does not
// set a budget. A full `git diff` of a large repository would otherwise fill
// the context window in one call.
const DefaultMaxOutputBytes = 256 * 1024

// truncationNotice is appended to a truncated result so that the model knows
// it did not see everything and can narrow its request.
const truncationNotice = "\n... [output truncated at %s; narrow the request to see the rest]"

// Executor runs tool calls on behalf of an agent.
//
// It is the only place that decides whether a call is allowed: the registry
// knows what exists, the grant knows what the agent may use, and the executor
// enforces the split. A model cannot reach a tool by naming it, because an
// ungranted name is refused before the tool is looked up.
type Executor struct {
	registry *Registry
	grant    *security.Grant
	env      Env
	logger   *slog.Logger
}

// NewExecutor builds an executor. A nil grant grants nothing.
func NewExecutor(registry *Registry, grant *security.Grant, env Env) *Executor {
	return &Executor{
		registry: registry,
		grant:    grant,
		env:      env,
		logger:   env.Log(),
	}
}

// Registry returns the underlying registry.
func (e *Executor) Registry() *Registry { return e.registry }

// Grant returns the grant the executor enforces.
func (e *Executor) Grant() *security.Grant { return e.grant }

// Definitions returns the definitions of the granted tools.
func (e *Executor) Definitions() ([]llm.ToolDefinition, error) {
	if e.registry == nil {
		return nil, apperrors.New(apperrors.KindInternal, "tool.definitions",
			"the tool registry is not initialised")
	}
	return e.registry.Definitions(e.grant.Names())
}

// Execute runs one tool call and always returns a tool result, except for the
// failures the model cannot recover from on its own: an unknown tool or a
// denied tool. A failing operation (missing file, git error, bad argument) is
// returned as a result marked as an error, so the agent loop can hand it back
// and let the model adapt instead of aborting the task.
func (e *Executor) Execute(ctx context.Context, call llm.ToolCall) (llm.ToolResult, error) {
	result := llm.ToolResult{ToolCallID: call.ID, Name: call.Name}

	if strings.TrimSpace(call.Name) == "" {
		return result, apperrors.New(apperrors.KindTool, "tool.execute",
			"the tool call has no tool name")
	}
	if e.grant.Empty() || !e.grant.Allowed(call.Name) {
		return result, e.grant.Deny(call.Name)
	}

	tool, ok := e.registry.Lookup(call.Name)
	if !ok {
		return result, apperrors.New(apperrors.KindTool, "tool.execute",
			"unknown tool %q", call.Name).
			WithDetails(map[string]any{"available": e.registry.Names()})
	}

	started := time.Now()
	output, err := tool.Execute(ctx, json.RawMessage(call.Arguments), e.env)
	duration := time.Since(started)

	if err != nil {
		result.IsError = true
		result.Content = failureContent(err)
		result.Metadata = map[string]any{
			"duration_ms": duration.Milliseconds(),
			"error_kind":  string(apperrors.KindOf(err)),
		}
		e.logger.Debug("tool failed",
			slog.String("tool", call.Name),
			slog.String("kind", string(apperrors.KindOf(err))),
			slog.String("duration", duration.String()))
		return result, nil
	}

	content, truncated := e.truncate(output.Content)
	result.Content = content
	result.Truncated = truncated

	result.Metadata = map[string]any{
		"duration_ms":         duration.Milliseconds(),
		"bytes":               len(content),
		"truncated":           truncated,
		"result_bytes_before": len(output.Content),
	}
	for key, value := range output.Metadata {
		result.Metadata[key] = value
	}

	e.logger.Debug("tool finished",
		slog.String("tool", call.Name),
		slog.Int("bytes", len(content)),
		slog.Bool("truncated", truncated),
		slog.String("duration", duration.String()))

	return result, nil
}

// ExecuteAll runs a batch of tool calls in order and returns the results.
//
// Order is preserved rather than parallelised: the results are appended to the
// conversation as they are produced, and a model that asked for two calls
// usually wants the first one's outcome before the second. Providers that
// cannot do parallel tool calls would also be confused by a reordered batch.
func (e *Executor) ExecuteAll(ctx context.Context, calls []llm.ToolCall) ([]llm.ToolResult, error) {
	results := make([]llm.ToolResult, 0, len(calls))
	for _, call := range calls {
		result, err := e.Execute(ctx, call)
		if err != nil {
			return results, err
		}
		results = append(results, result)
	}
	return results, nil
}

// truncate enforces the output budget.
func (e *Executor) truncate(content string) (string, bool) {
	limit := e.env.MaxOutputBytes
	if limit <= 0 {
		limit = DefaultMaxOutputBytes
	}
	if len(content) <= limit {
		return content, false
	}

	// Cut on a line boundary when one is close, so the model does not receive
	// half of a line and mistake it for the real content.
	cut := content[:limit]
	if index := strings.LastIndexByte(cut, '\n'); index > limit/2 {
		cut = cut[:index]
	}
	return cut + fmt.Sprintf(truncationNotice, humanBytes(int64(limit))), true
}

// failureContent renders a tool failure for the model.
//
// The kind is included because it changes what the model should do next: a
// ToolError means the request was wrong, a WorkspaceError means the path was
// refused, a PermissionError means the tool is not available to it.
func failureContent(err error) string {
	if err == nil {
		return "tool failed"
	}
	return fmt.Sprintf("tool failed [%s]: %s", apperrors.KindOf(err), err.Error())
}

// humanBytes renders a byte count for a message.
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
