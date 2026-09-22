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

package builtin

import (
	"log/slog"
	"sort"
	"strings"
	"testing"

	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/security"
	"github.com/pawagents/pawagents/internal/tools"
)

func TestRegistryHoldsEveryBuiltinTool(t *testing.T) {
	registry := NewRegistry()

	if registry.Len() != 8 {
		t.Fatalf("Len() = %d, want 8", registry.Len())
	}

	expected := append([]string(nil), ToolNames()...)
	sort.Strings(expected)
	if got := strings.Join(registry.Names(), ","); got != strings.Join(expected, ",") {
		t.Fatalf("Names() = %q, want %q", got, strings.Join(expected, ","))
	}

	definitions, err := registry.Definitions(registry.Names())
	if err != nil {
		t.Fatalf("Definitions() = %v", err)
	}
	for _, definition := range definitions {
		if definition.Description == "" {
			t.Fatalf("tool %q has no description", definition.Name)
		}
		if definition.InputSchema.IsZero() {
			t.Fatalf("tool %q has no input schema", definition.Name)
		}
	}
}

func TestBuiltinNamesMatchTheConfiguration(t *testing.T) {
	// The configuration validator accepts a fixed set of tool names. If the
	// runtime provided a different set, a valid profile would fail at run time
	// with a capability error, so the two lists must agree exactly.
	known := append([]string(nil), config.KnownToolNames()...)
	provided := append([]string(nil), ToolNames()...)
	sort.Strings(known)
	sort.Strings(provided)

	if strings.Join(known, ",") != strings.Join(provided, ",") {
		t.Fatalf("the runtime provides %v but the configuration knows %v", provided, known)
	}
}

func TestEnvironment(t *testing.T) {
	workspace, err := security.NewWorkspace(t.TempDir(), security.WorkspaceOptions{})
	if err != nil {
		t.Fatalf("NewWorkspace() = %v", err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	logger := slog.New(slog.DiscardHandler)
	env, err := Environment(workspace, 0, logger)
	if err != nil {
		t.Fatalf("Environment() = %v", err)
	}
	if env.Workspace != workspace {
		t.Fatal("the workspace must be passed through")
	}
	if env.MaxOutputBytes != tools.DefaultMaxOutputBytes {
		t.Fatalf("MaxOutputBytes = %d, want the default", env.MaxOutputBytes)
	}

	env, err = Environment(workspace, 4096, logger)
	if err != nil {
		t.Fatalf("Environment() = %v", err)
	}
	if env.MaxOutputBytes != 4096 {
		t.Fatalf("MaxOutputBytes = %d", env.MaxOutputBytes)
	}

	if _, err := Environment(nil, 0, logger); err == nil {
		t.Fatal("a missing workspace guard must be refused")
	}
}
