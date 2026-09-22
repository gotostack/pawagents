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

package apperrors

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestKindOf(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want Kind
	}{
		{name: "nil error", err: nil, want: ""},
		{name: "classified", err: New(KindConfig, "config.load", "boom"), want: KindConfig},
		{name: "wrapped classified", err: Wrap(KindProvider, "provider.do", "boom", errors.New("cause")), want: KindProvider},
		{name: "nested classified", err: fmt.Errorf("outer: %w", New(KindAgent, "agent.run", "boom")), want: KindAgent},
		{name: "plain error", err: errors.New("boom"), want: KindInternal},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := KindOf(test.err); got != test.want {
				t.Fatalf("KindOf() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestExitCode(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{err: nil, want: ExitOK},
		{err: New(KindConfig, "op", "boom"), want: ExitConfig},
		{err: New(KindInvalidArgument, "op", "boom"), want: ExitUsage},
		{err: New(KindAuthentication, "op", "boom"), want: ExitAuth},
		{err: New(KindCapability, "op", "boom"), want: ExitCapability},
		{err: New(KindPermission, "op", "boom"), want: ExitPermission},
		{err: New(KindWorkspace, "op", "boom"), want: ExitWorkspace},
		{err: New(KindProvider, "op", "boom"), want: ExitProvider},
		{err: New(KindAgent, "op", "boom"), want: ExitAgent},
		{err: New(KindTool, "op", "boom"), want: ExitTool},
		{err: New(KindBudgetExceeded, "op", "boom"), want: ExitBudget},
		{err: New(KindTimeout, "op", "boom"), want: ExitTimeout},
		{err: New(KindCancelled, "op", "boom"), want: ExitCancelled},
		{err: New(KindNotFound, "op", "boom"), want: ExitNotFound},
		{err: errors.New("boom"), want: ExitError},
	}

	for _, test := range tests {
		if got := ExitCode(test.err); got != test.want {
			t.Fatalf("ExitCode(%v) = %d, want %d", test.err, got, test.want)
		}
	}
}

func TestErrorRendering(t *testing.T) {
	err := New(KindTool, "repo.read", "path %q is outside the workspace", "../secret")

	want := `ToolError: repo.read: path "../secret" is outside the workspace`
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if got := err.Code(); got != string(KindTool) {
		t.Fatalf("Code() = %q, want %q", got, KindTool)
	}
	if got := err.MessageText(); got != `path "../secret" is outside the workspace` {
		t.Fatalf("MessageText() = %q", got)
	}
}

func TestWrapKeepsCause(t *testing.T) {
	cause := errors.New("connection refused")
	err := Wrap(KindProvider, "provider.generate", "cannot reach ollama", cause)

	if !errors.Is(err, cause) {
		t.Fatal("wrapped cause is not reachable through errors.Is")
	}
	var target *Error
	if !errors.As(err, &target) {
		t.Fatal("error does not unwrap to *Error")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("Error() = %q, want the cause to be included", err.Error())
	}
}

func TestWithDetailsAndOp(t *testing.T) {
	err := New(KindWorkspace, "", "escape attempt").
		WithOp("workspace.guard").
		WithDetails(map[string]any{"path": "../etc/passwd"})

	if err.Op != "workspace.guard" {
		t.Fatalf("Op = %q", err.Op)
	}
	if err.Details["path"] != "../etc/passwd" {
		t.Fatalf("Details = %v", err.Details)
	}

	err.WithDetails(map[string]any{"kind": "traversal"})
	if len(err.Details) != 2 {
		t.Fatalf("WithDetails should merge, got %v", err.Details)
	}
}

func TestIsMatchesKindAndMessage(t *testing.T) {
	sentinel := New(KindTimeout, "agent.loop", "deadline exceeded")
	if !errors.Is(sentinel, New(KindTimeout, "agent.loop", "deadline exceeded")) {
		t.Fatal("identical classified errors should match")
	}
	if errors.Is(sentinel, New(KindTimeout, "agent.loop", "other")) {
		t.Fatal("different messages must not match")
	}
	if errors.Is(sentinel, New(KindAgent, "agent.loop", "deadline exceeded")) {
		t.Fatal("different kinds must not match")
	}
}

func TestMulti(t *testing.T) {
	if err := Multi(KindConfig, "config.validate", "header"); err != nil {
		t.Fatalf("Multi with no errors = %v, want nil", err)
	}
	if err := Multi(KindConfig, "config.validate", "header", nil, nil); err != nil {
		t.Fatalf("Multi with only nils = %v, want nil", err)
	}

	single := New(KindConfig, "config.validate", "only one")
	if err := Multi(KindConfig, "config.validate", "header", single); err != single {
		t.Fatal("Multi should return a single classified error unchanged")
	}

	err := Multi(KindConfig, "config.validate", "configuration is invalid",
		errors.New("agents.a: model is required"),
		errors.New("providers.p: type is required"),
	)
	if !IsKind(err, KindConfig) {
		t.Fatalf("kind = %q, want %q", KindOf(err), KindConfig)
	}
	message := err.Error()
	for _, want := range []string{"configuration is invalid", "agents.a: model is required", "providers.p: type is required"} {
		if !strings.Contains(message, want) {
			t.Fatalf("Error() = %q, want it to contain %q", message, want)
		}
	}
	if !strings.Contains(message, "\n  - ") {
		t.Fatalf("Error() = %q, want a bullet list", message)
	}
}

func TestCollectFlattensJoinedErrors(t *testing.T) {
	joined := errors.Join(errors.New("a"), nil, errors.New("b"))
	items := Collect(nil, joined, errors.New("c"))
	if len(items) != 3 {
		t.Fatalf("Collect() returned %d items, want 3", len(items))
	}
}

func TestSummaryDropsClassPrefix(t *testing.T) {
	err := New(KindConfig, "config.parse", `unknown field "base_ur"`)
	if got := Summary(err); got != `config.parse: unknown field "base_ur"` {
		t.Fatalf("Summary() = %q", got)
	}
	if got := Summary(errors.New("plain")); got != "plain" {
		t.Fatalf("Summary(plain) = %q", got)
	}
	if got := Summary(nil); got != "" {
		t.Fatalf("Summary(nil) = %q", got)
	}
}

func TestAllKindsAreUniqueAndNonEmpty(t *testing.T) {
	seen := map[Kind]bool{}
	for _, kind := range AllKinds() {
		if kind == "" {
			t.Fatal("AllKinds contains an empty kind")
		}
		if seen[kind] {
			t.Fatalf("duplicate kind %q", kind)
		}
		seen[kind] = true
	}
	if len(seen) != 14 {
		t.Fatalf("AllKinds has %d entries, want 14", len(seen))
	}
}
