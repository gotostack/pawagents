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
	"log/slog"
	"strings"
	"testing"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/llm"
	"github.com/pawagents/pawagents/internal/security"
)

// echoTool is a minimal tool used to exercise the runtime.
type echoTool struct {
	name    string
	result  Result
	err     error
	args    *Arguments
	lastRaw json.RawMessage
}

func (e *echoTool) Name() string { return e.name }

func (e *echoTool) Definition() llm.ToolDefinition {
	return NewDefinition(e.name, "A test tool.", map[string]llm.JSONSchema{
		"path": llm.StringSchema(),
	})
}

func (e *echoTool) Execute(_ context.Context, raw json.RawMessage, _ Env) (Result, error) {
	e.lastRaw = raw
	arguments, err := DecodeArguments(raw)
	if err != nil {
		return Result{}, err
	}
	e.args = arguments
	if _, err := arguments.String("path", false); err != nil {
		return Result{}, err
	}
	if err := arguments.RejectUnknown(); err != nil {
		return Result{}, err
	}
	return e.result, e.err
}

func silentEnv() Env {
	return Env{MaxOutputBytes: 64, Logger: slog.New(slog.DiscardHandler)}
}

func TestDecodeArguments(t *testing.T) {
	for _, raw := range []string{"", "null", "  ", "{}"} {
		arguments, err := DecodeArguments(json.RawMessage(raw))
		if err != nil {
			t.Fatalf("DecodeArguments(%q) = %v", raw, err)
		}
		if arguments.Has("anything") {
			t.Fatalf("DecodeArguments(%q) reported a key", raw)
		}
	}

	if _, err := DecodeArguments(json.RawMessage("[]")); err == nil {
		t.Fatal("a JSON array must be rejected")
	}
	if _, err := DecodeArguments(json.RawMessage("{not json")); err == nil {
		t.Fatal("malformed JSON must be rejected")
	}
}

func TestArgumentsTypedAccessors(t *testing.T) {
	arguments, err := DecodeArguments(json.RawMessage(`{
		"path": "main.go",
		"limit": 500,
		"depth": "3",
		"flag": "yes",
		"glob": "*.go",
		"list": ["a", "b"]
	}`))
	if err != nil {
		t.Fatalf("DecodeArguments() = %v", err)
	}

	path, err := arguments.String("path", true)
	if err != nil || path != "main.go" {
		t.Fatalf("String() = %q, %v", path, err)
	}
	if _, err := arguments.String("absent", true); err == nil {
		t.Fatal("a required argument must be reported as missing")
	}

	// A model that asks for too much is clamped instead of refused.
	limit, err := arguments.Int("limit", 10, 1, 200)
	if err != nil || limit != 200 {
		t.Fatalf("Int() = %d, %v, want the value clamped to the maximum", limit, err)
	}
	depth, err := arguments.Int("depth", 1, 1, 4)
	if err != nil || depth != 3 {
		t.Fatalf("Int() = %d, %v, want the string form accepted", depth, err)
	}
	if _, err := arguments.Int("glob", 0, 0, 0); err == nil {
		t.Fatal("a string where an integer is expected must be refused")
	}

	flag, err := arguments.Bool("flag", false)
	if err != nil || !flag {
		t.Fatalf("Bool() = %v, %v, want the string form accepted", flag, err)
	}
	if _, err := arguments.Bool("path", false); err == nil {
		t.Fatal("a string where a boolean is expected must be refused")
	}

	glob, err := arguments.String("glob", false)
	if err != nil || glob != "*.go" {
		t.Fatalf("String() = %q, %v", glob, err)
	}
	list, err := arguments.StringSlice("list")
	if err != nil || strings.Join(list, ",") != "a,b" {
		t.Fatalf("StringSlice() = %v, %v", list, err)
	}
	single, err := arguments.StringSlice("glob")
	if err != nil || len(single) != 1 {
		t.Fatalf("StringSlice() = %v, %v, want a single string to become a list", single, err)
	}
	if _, err := arguments.StringSlice("path"); err != nil {
		t.Fatalf("StringSlice() = %v", err)
	}

	// Every key was read, so nothing is unknown.
	if err := arguments.RejectUnknown(); err != nil {
		t.Fatalf("RejectUnknown() = %v", err)
	}
}

func TestArgumentsRejectUnknown(t *testing.T) {
	arguments, err := DecodeArguments(json.RawMessage(`{"path": "main.go", "levle": 3}`))
	if err != nil {
		t.Fatalf("DecodeArguments() = %v", err)
	}
	if _, err := arguments.String("path", true); err != nil {
		t.Fatalf("String() = %v", err)
	}

	err = arguments.RejectUnknown()
	if err == nil {
		t.Fatal("an unknown argument must be reported so the model can correct itself")
	}
	if !strings.Contains(err.Error(), "levle") {
		t.Fatalf("error = %q, want it to name the unknown argument", err.Error())
	}
	if !strings.Contains(err.Error(), "path") {
		t.Fatalf("error = %q, want it to list the accepted arguments", err.Error())
	}
}

func TestRegistry(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&echoTool{name: "test.echo"})

	if registry.Len() != 1 {
		t.Fatalf("Len() = %d", registry.Len())
	}
	if _, ok := registry.Lookup("test.echo"); !ok {
		t.Fatal("Lookup() did not find the registered tool")
	}
	if _, ok := registry.Lookup("absent"); ok {
		t.Fatal("Lookup() found an unregistered tool")
	}
	if names := registry.Names(); strings.Join(names, ",") != "test.echo" {
		t.Fatalf("Names() = %v", names)
	}

	definitions, err := registry.Definitions([]string{"test.echo"})
	if err != nil {
		t.Fatalf("Definitions() = %v", err)
	}
	if len(definitions) != 1 || definitions[0].Name != "test.echo" {
		t.Fatalf("definitions = %+v", definitions)
	}

	if _, err := registry.Definitions([]string{"test.absent"}); err == nil {
		t.Fatal("an unknown tool must be reported")
	} else if !apperrors.IsKind(err, apperrors.KindCapability) {
		t.Fatalf("kind = %q, want a capability error", apperrors.KindOf(err))
	}
}

func TestRegistryPanicsOnDuplicates(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&echoTool{name: "test.echo"})

	defer func() {
		if recover() == nil {
			t.Fatal("registering the same name twice must panic")
		}
	}()
	registry.Register(&echoTool{name: "test.echo"})
}

func TestExecutorEnforcesTheGrant(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&echoTool{name: "test.echo", result: Result{Content: "ok"}})

	executor := NewExecutor(registry, security.NewGrant([]string{"test.echo"}, nil, nil), silentEnv())

	definitions, err := executor.Definitions()
	if err != nil {
		t.Fatalf("Definitions() = %v", err)
	}
	if len(definitions) != 1 {
		t.Fatalf("definitions = %+v", definitions)
	}

	result, err := executor.Execute(context.Background(), llm.ToolCall{
		ID: "call_1", Name: "test.echo", Arguments: `{"path":"main.go"}`,
	})
	if err != nil {
		t.Fatalf("Execute() = %v", err)
	}
	if result.Content != "ok" || result.ToolCallID != "call_1" || result.IsError {
		t.Fatalf("result = %+v", result)
	}

	// An ungranted tool is refused before it is looked up.
	restricted := NewExecutor(registry, security.NewGrant([]string{"other.tool"}, nil, nil), silentEnv())
	_, err = restricted.Execute(context.Background(), llm.ToolCall{ID: "c", Name: "test.echo"})
	if !apperrors.IsKind(err, apperrors.KindPermission) {
		t.Fatalf("kind = %q, want a permission error", apperrors.KindOf(err))
	}

	// A grant that names nothing allows nothing.
	empty := NewExecutor(registry, nil, silentEnv())
	if _, err := empty.Execute(context.Background(), llm.ToolCall{Name: "test.echo"}); err == nil {
		t.Fatal("a nil grant must refuse every tool")
	}
}

func TestExecutorReportsUnknownTool(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry, security.NewGrant([]string{"test.absent"}, nil, nil), silentEnv())

	_, err := executor.Execute(context.Background(), llm.ToolCall{Name: "test.absent"})
	if !apperrors.IsKind(err, apperrors.KindTool) {
		t.Fatalf("kind = %q, want a tool error", apperrors.KindOf(err))
	}

	if _, err := executor.Execute(context.Background(), llm.ToolCall{}); err == nil {
		t.Fatal("a call without a name must be refused")
	}
}

func TestExecutorTruncatesLargeResults(t *testing.T) {
	large := strings.Repeat("line of output\n", 100)
	registry := NewRegistry()
	registry.Register(&echoTool{name: "test.echo", result: Result{Content: large}})

	executor := NewExecutor(registry, security.NewGrant([]string{"test.echo"}, nil, nil),
		Env{MaxOutputBytes: 128, Logger: slog.New(slog.DiscardHandler)})

	result, err := executor.Execute(context.Background(), llm.ToolCall{ID: "c", Name: "test.echo"})
	if err != nil {
		t.Fatalf("Execute() = %v", err)
	}
	if !result.Truncated {
		t.Fatal("a result above the budget must be marked as truncated")
	}
	if !strings.Contains(result.Content, "truncated") {
		t.Fatalf("content = %q, want a truncation notice", result.Content)
	}
	if result.Metadata["truncated"] != true {
		t.Fatalf("metadata = %v", result.Metadata)
	}

	// A small result passes through untouched.
	smallRegistry := NewRegistry()
	smallRegistry.Register(&echoTool{name: "test.small", result: Result{Content: "tiny"}})
	small := NewExecutor(smallRegistry, security.NewGrant([]string{"test.small"}, nil, nil), silentEnv())
	result, err = small.Execute(context.Background(), llm.ToolCall{ID: "c", Name: "test.small"})
	if err != nil {
		t.Fatalf("Execute() = %v", err)
	}
	if result.Truncated || result.Content != "tiny" {
		t.Fatalf("result = %+v, want it untouched", result)
	}

	// An unset budget falls back to the default.
	var unset Env
	if unset.MaxOutputBytes != 0 {
		t.Fatalf("MaxOutputBytes = %d", unset.MaxOutputBytes)
	}
}

func TestExecutorTurnsToolFailuresIntoResults(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&echoTool{
		name: "test.echo",
		err:  apperrors.New(apperrors.KindTool, "test.echo", "main.go does not exist"),
	})

	executor := NewExecutor(registry, security.NewGrant([]string{"test.echo"}, nil, nil), silentEnv())

	// A failing operation must not abort the loop: the model has to see it and
	// adapt, so it comes back as a marked result rather than an error.
	result, err := executor.Execute(context.Background(), llm.ToolCall{ID: "c", Name: "test.echo"})
	if err != nil {
		t.Fatalf("Execute() = %v, want the failure inside the result", err)
	}
	if !result.IsError {
		t.Fatal("the result must be marked as an error")
	}
	if !strings.Contains(result.Content, "main.go does not exist") {
		t.Fatalf("content = %q", result.Content)
	}
	if !strings.Contains(result.Content, string(apperrors.KindTool)) {
		t.Fatalf("content = %q, want the error kind", result.Content)
	}
	if result.Metadata["error_kind"] != string(apperrors.KindTool) {
		t.Fatalf("metadata = %v", result.Metadata)
	}
}

func TestExecutorExecuteAllPreservesOrder(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&echoTool{name: "test.first", result: Result{Content: "first"}})
	registry.Register(&echoTool{name: "test.second", result: Result{Content: "second"}})

	executor := NewExecutor(registry,
		security.NewGrant([]string{"test.first", "test.second"}, nil, nil), silentEnv())

	results, err := executor.ExecuteAll(context.Background(), []llm.ToolCall{
		{ID: "1", Name: "test.first"},
		{ID: "2", Name: "test.second"},
	})
	if err != nil {
		t.Fatalf("ExecuteAll() = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].Content != "first" || results[1].Content != "second" {
		t.Fatalf("results = %+v, want the call order preserved", results)
	}

	// A denied call stops the batch, because the model is asking for something
	// it may not have and the loop must report that rather than continue.
	restricted := NewExecutor(registry, security.NewGrant([]string{"test.first"}, nil, nil), silentEnv())
	if _, err := restricted.ExecuteAll(context.Background(), []llm.ToolCall{
		{ID: "1", Name: "test.first"},
		{ID: "2", Name: "test.second"},
	}); err == nil {
		t.Fatal("a denied call in a batch must be reported")
	}
}

func TestEnvLogFallsBackToTheDefaultLogger(t *testing.T) {
	var env Env
	if env.Log() == nil {
		t.Fatal("Log() must never return nil")
	}
}
