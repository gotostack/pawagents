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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/security"
	"github.com/pawagents/pawagents/internal/tools"
)

// newEnv builds a tool environment over a temporary workspace.
func newEnv(t *testing.T, files map[string]string) tools.Env {
	t.Helper()

	root := t.TempDir()
	for name, contents := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("cannot create %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o600); err != nil {
			t.Fatalf("cannot write %s: %v", full, err)
		}
	}

	workspace, err := security.NewWorkspace(root, security.WorkspaceOptions{MaxFileBytes: 1 << 20})
	if err != nil {
		t.Fatalf("NewWorkspace() = %v", err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	return tools.Env{Workspace: workspace, MaxOutputBytes: 1 << 20}
}

func run(t *testing.T, tool tools.Tool, env tools.Env, payload string) tools.Result {
	t.Helper()

	result, err := tool.Execute(context.Background(), json.RawMessage(payload), env)
	if err != nil {
		t.Fatalf("%s.Execute() = %v", tool.Name(), err)
	}
	return result
}

func runErr(t *testing.T, tool tools.Tool, env tools.Env, payload string) error {
	t.Helper()

	_, err := tool.Execute(context.Background(), json.RawMessage(payload), env)
	if err == nil {
		t.Fatalf("%s.Execute() = nil, want an error", tool.Name())
	}
	return err
}

func TestReadReturnsNumberedLines(t *testing.T) {
	env := newEnv(t, map[string]string{
		"main.go": "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n",
	})

	result := run(t, Read{}, env, `{"path":"main.go"}`)
	for _, want := range []string{"main.go (lines 1-5 of 5)", "1|package main", "5|}"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("content is missing %q:\n%s", want, result.Content)
		}
	}
	if result.Metadata["lines"] != 5 {
		t.Fatalf("metadata = %v", result.Metadata)
	}
	if result.Truncated {
		t.Fatal("a small file must not be truncated")
	}
}

func TestReadHonoursLineRanges(t *testing.T) {
	env := newEnv(t, map[string]string{
		"big.txt": "one\ntwo\nthree\nfour\nfive\n",
	})

	result := run(t, Read{}, env, `{"path":"big.txt","start_line":2,"end_line":4}`)
	if !strings.Contains(result.Content, "lines 2-4 of 5") {
		t.Fatalf("content = %q", result.Content)
	}
	if strings.Contains(result.Content, "1|one") || strings.Contains(result.Content, "5|five") {
		t.Fatalf("content = %q, want only lines 2 to 4", result.Content)
	}

	// A range past the end is clamped rather than refused.
	result = run(t, Read{}, env, `{"path":"big.txt","start_line":3,"end_line":99}`)
	if !strings.Contains(result.Content, "lines 3-5 of 5") {
		t.Fatalf("content = %q", result.Content)
	}

	// A start beyond the file is a clear error.
	err := runErr(t, Read{}, env, `{"path":"big.txt","start_line":99}`)
	if !strings.Contains(err.Error(), "past the end") {
		t.Fatalf("error = %q", err.Error())
	}

	err = runErr(t, Read{}, env, `{"path":"big.txt","start_line":4,"end_line":2}`)
	if !strings.Contains(err.Error(), "before start_line") {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestReadTruncatesLargeFiles(t *testing.T) {
	content := strings.Repeat("a line of source code\n", 5000)
	env := newEnv(t, map[string]string{"big.txt": content})

	result := run(t, Read{}, env, `{"path":"big.txt","max_bytes":4096}`)
	if !result.Truncated {
		t.Fatal("a result above max_bytes must be marked as truncated")
	}
	if !strings.Contains(result.Content, "read the rest with start_line/end_line") {
		t.Fatalf("content = %q, want an actionable notice", result.Content)
	}
	if len(result.Content) > 8192 {
		t.Fatalf("content is %d bytes, want it cut close to the limit", len(result.Content))
	}
}

func TestReadRefusesBinaryFiles(t *testing.T) {
	env := newEnv(t, map[string]string{"data.bin": "binary\x00content"})

	result := run(t, Read{}, env, `{"path":"data.bin"}`)
	if result.Metadata["binary"] != true {
		t.Fatalf("metadata = %v", result.Metadata)
	}
	if !strings.Contains(result.Content, "binary") {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestReadRefusesEscapesAndBadArguments(t *testing.T) {
	env := newEnv(t, map[string]string{"main.go": "package main\n"})

	err := runErr(t, Read{}, env, `{"path":"../../../etc/passwd"}`)
	if !apperrors.IsKind(err, apperrors.KindWorkspace) {
		t.Fatalf("kind = %q, want a workspace error", apperrors.KindOf(err))
	}

	if err := runErr(t, Read{}, env, `{"path":"absent.go"}`); err == nil {
		t.Fatal("a missing file must be reported")
	}
	if err := runErr(t, Read{}, env, `{}`); err == nil {
		t.Fatal("a missing path argument must be reported")
	}
	if err := runErr(t, Read{}, env, `{"path":"main.go","unknown":1}`); err == nil {
		t.Fatal("an unknown argument must be reported")
	}
	if err := runErr(t, Read{}, env, `{"path":123}`); err == nil {
		t.Fatal("a wrongly typed argument must be reported")
	}
}

func TestReadRequiresAWorkspace(t *testing.T) {
	err := runErr(t, Read{}, tools.Env{}, `{"path":"main.go"}`)
	if !apperrors.IsKind(err, apperrors.KindInternal) {
		t.Fatalf("kind = %q, want an internal error", apperrors.KindOf(err))
	}
}

func TestListReportsEntries(t *testing.T) {
	env := newEnv(t, map[string]string{
		"main.go":        "package main\n",
		"lib/a.go":       "package lib\n",
		"lib/b.go":       "package lib\n",
		"docs/readme.md": "# readme\n",
	})

	result := run(t, List{}, env, `{}`)
	for _, want := range []string{"main.go", "lib/", "docs/"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("listing is missing %q:\n%s", want, result.Content)
		}
	}
	if result.Metadata["depth"] != 1 {
		t.Fatalf("metadata = %v", result.Metadata)
	}

	// A deeper listing shows the children.
	result = run(t, List{}, env, `{"depth":2}`)
	if !strings.Contains(result.Content, "lib/a.go") {
		t.Fatalf("listing is missing the nested file:\n%s", result.Content)
	}

	// A glob narrows the result.
	result = run(t, List{}, env, `{"depth":2,"file_glob":"*.go"}`)
	if strings.Contains(result.Content, "readme.md") {
		t.Fatalf("listing = %q, want the glob applied", result.Content)
	}
	if !strings.Contains(result.Content, "a.go") {
		t.Fatalf("listing = %q, want the matching files", result.Content)
	}
}

func TestListSkipsHiddenByDefault(t *testing.T) {
	env := newEnv(t, map[string]string{
		"main.go":          "package main\n",
		".env":             "SECRET=1\n",
		".github/work.yml": "name: ci\n",
	})

	result := run(t, List{}, env, `{"depth":2}`)
	if strings.Contains(result.Content, ".env") || strings.Contains(result.Content, ".github") {
		t.Fatalf("listing = %q, want hidden entries skipped by default", result.Content)
	}

	result = run(t, List{}, env, `{"depth":2,"include_hidden":true}`)
	if !strings.Contains(result.Content, ".env") {
		t.Fatalf("listing = %q, want hidden entries when asked", result.Content)
	}
}

func TestListBoundsTheResult(t *testing.T) {
	files := map[string]string{}
	for index := range 30 {
		files[filepath.Join("lib", strings.Repeat("d", index+1)+".go")] = "package lib\n"
	}
	env := newEnv(t, files)

	result := run(t, List{}, env, `{"max_entries":5,"depth":2}`)
	if !result.Truncated {
		t.Fatal("the listing must report that it stopped early")
	}
	if !strings.Contains(result.Content, "entry limit") {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestListErrors(t *testing.T) {
	env := newEnv(t, map[string]string{"main.go": "package main\n"})

	if err := runErr(t, List{}, env, `{"path":"../.."}`); !apperrors.IsKind(err, apperrors.KindWorkspace) {
		t.Fatalf("kind = %q, want a workspace error", apperrors.KindOf(err))
	}
	if err := runErr(t, List{}, env, `{"path":"absent"}`); err == nil {
		t.Fatal("a missing directory must be reported")
	}
}

func TestSearchFindsTextAndPatterns(t *testing.T) {
	env := newEnv(t, map[string]string{
		"lib/a.go":       "package lib\n\nfunc Lock() {}\nfunc Unlock() {}\n",
		"lib/b.go":       "package lib\n\nfunc helper() {}\n",
		"docs/readme.md": "# readme\n\nthe lock is documented here\n",
		".hidden/x.go":   "package hidden\n",
	})

	result := run(t, Search{}, env, `{"query":"Lock"}`)
	if !strings.Contains(result.Content, "lib/a.go:3: func Lock() {}") {
		t.Fatalf("content = %q", result.Content)
	}
	// The search is case insensitive by default, so the lower case mention in
	// the documentation matches too.
	if !strings.Contains(result.Content, "docs/readme.md:3: the lock is documented here") {
		t.Fatalf("content = %q, want the case insensitive match", result.Content)
	}
	if strings.Contains(result.Content, ".hidden") {
		t.Fatalf("content = %q, want hidden files skipped", result.Content)
	}
	if result.Metadata["matches"] == 0 {
		t.Fatalf("metadata = %v", result.Metadata)
	}

	// A regular expression with a glob.
	result = run(t, Search{}, env, `{"query":"func (Lock|Unlock)","regex":true,"file_glob":"*.go"}`)
	if !strings.Contains(result.Content, "func Lock()") || !strings.Contains(result.Content, "func Unlock()") {
		t.Fatalf("content = %q", result.Content)
	}
	if strings.Contains(result.Content, "helper") {
		t.Fatalf("content = %q, want the pattern applied", result.Content)
	}

	// Case sensitive matching.
	result = run(t, Search{}, env, `{"query":"lock","case_sensitive":true}`)
	if strings.Contains(result.Content, "func Lock()") {
		t.Fatalf("content = %q, want the case to matter", result.Content)
	}

	// A subtree limits the search.
	result = run(t, Search{}, env, `{"query":"package","path":"docs"}`)
	if strings.Contains(result.Content, "lib/a.go") {
		t.Fatalf("content = %q, want the subtree respected", result.Content)
	}
}

func TestSearchReportsNoMatchAndBounds(t *testing.T) {
	env := newEnv(t, map[string]string{
		"lib/a.go": "package lib\n// hit\n// hit\n// hit\n",
	})

	result := run(t, Search{}, env, `{"query":"nothing here"}`)
	if !strings.Contains(result.Content, "no match found") {
		t.Fatalf("content = %q", result.Content)
	}

	result = run(t, Search{}, env, `{"query":"hit","max_results":1}`)
	if !result.Truncated {
		t.Fatal("hitting the result limit must be reported")
	}
	if !strings.Contains(result.Content, "search limit") {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestSearchErrors(t *testing.T) {
	env := newEnv(t, map[string]string{"main.go": "package main\n"})

	if err := runErr(t, Search{}, env, `{}`); err == nil {
		t.Fatal("a missing query must be reported")
	}
	if err := runErr(t, Search{}, env, `{"query":"   "}`); err == nil {
		t.Fatal("an empty query must be reported")
	}
	if err := runErr(t, Search{}, env, `{"query":"(","regex":true}`); err == nil {
		t.Fatal("an invalid regular expression must be reported")
	}
	if err := runErr(t, Search{}, env, `{"query":"x","path":"../.."}`); !apperrors.IsKind(err, apperrors.KindWorkspace) {
		t.Fatalf("kind = %q, want a workspace error", apperrors.KindOf(err))
	}
}

func TestStatDescribesFilesAndDirectories(t *testing.T) {
	env := newEnv(t, map[string]string{"main.go": "package main\n\nfunc main() {}\n"})

	result := run(t, Stat{}, env, `{"path":"main.go"}`)
	for _, want := range []string{"type:     file", "lines:    3", "modified:"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("content is missing %q:\n%s", want, result.Content)
		}
	}
	if result.Metadata["is_dir"] != false {
		t.Fatalf("metadata = %v", result.Metadata)
	}
	if result.Metadata["lines"] != 3 {
		t.Fatalf("metadata = %v", result.Metadata)
	}

	result = run(t, Stat{}, env, `{"path":"."}`)
	if !strings.Contains(result.Content, "type:     directory") {
		t.Fatalf("content = %q", result.Content)
	}
	if result.Metadata["is_dir"] != true {
		t.Fatalf("metadata = %v", result.Metadata)
	}

	if err := runErr(t, Stat{}, env, `{"path":"absent.go"}`); err == nil {
		t.Fatal("a missing path must be reported")
	}
	if err := runErr(t, Stat{}, env, `{"path":"../../etc"}`); !apperrors.IsKind(err, apperrors.KindWorkspace) {
		t.Fatalf("kind = %q, want a workspace error", apperrors.KindOf(err))
	}
}

func TestDefinitionsAreComplete(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(Read{})
	registry.Register(List{})
	registry.Register(Search{})
	registry.Register(Stat{})

	definitions, err := registry.Definitions(registry.Names())
	if err != nil {
		t.Fatalf("Definitions() = %v", err)
	}
	if len(definitions) != 4 {
		t.Fatalf("definitions = %d, want 4", len(definitions))
	}
	for _, definition := range definitions {
		if definition.Description == "" {
			t.Fatalf("tool %s has no description", definition.Name)
		}
	}
}

func TestToolNamesMatchTheConfiguration(t *testing.T) {
	// The configuration validator accepts these names, so the runtime must
	// provide exactly them.
	for _, name := range []string{ReadToolName, ListToolName, SearchToolName, StatToolName} {
		if !strings.HasPrefix(name, "repo.") {
			t.Fatalf("tool name %q must use the repo namespace", name)
		}
	}
}
