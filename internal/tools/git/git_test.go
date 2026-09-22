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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/security"
	"github.com/pawagents/pawagents/internal/tools"
)

// newRepo builds a temporary git repository with one commit and one
// uncommitted change.
func newRepo(t *testing.T) (tools.Env, string) {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	root := t.TempDir()
	write(t, filepath.Join(root, "main.go"), "package main\n\nfunc main() {\n\tprintln(\"v1\")\n}\n")
	write(t, filepath.Join(root, "lib", "a.go"), "package lib\n\nfunc A() {}\n")

	gitRun(t, root, "init", "--initial-branch=main")
	gitRun(t, root, "config", "user.email", "test@example.com")
	gitRun(t, root, "config", "user.name", "PawAgents Test")
	gitRun(t, root, "config", "commit.gpgsign", "false")
	gitRun(t, root, "add", ".")
	gitRun(t, root, "commit", "-m", "feat: initial commit")

	// An unstaged change, a staged change and an untracked file give every git
	// tool something to report.
	write(t, filepath.Join(root, "main.go"), "package main\n\nfunc main() {\n\tprintln(\"v2\")\n}\n")
	write(t, filepath.Join(root, "lib", "b.go"), "package lib\n\nfunc B() {}\n")
	gitRun(t, root, "add", "lib/b.go")
	write(t, filepath.Join(root, "notes.md"), "# scratch\n")

	workspace, err := security.NewWorkspace(root, security.WorkspaceOptions{})
	if err != nil {
		t.Fatalf("NewWorkspace() = %v", err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	return tools.Env{Workspace: workspace, MaxOutputBytes: 1 << 20}, root
}

func write(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("cannot create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("cannot write %s: %v", path, err)
	}
}

func gitRun(t *testing.T, directory string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", directory}, args...)
	command := exec.Command("git", full...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+directory)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
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

func TestDiffShowsUnstagedChanges(t *testing.T) {
	env, _ := newRepo(t)

	result := run(t, Diff{}, env, `{}`)
	if !strings.Contains(result.Content, "git.diff") {
		t.Fatalf("content = %q, want a header naming the tool", result.Content)
	}
	if !strings.Contains(result.Content, "+++ b/main.go") {
		t.Fatalf("content = %q, want the unstaged change", result.Content)
	}
	// The staged file is not part of the unstaged diff.
	if strings.Contains(result.Content, "lib/b.go") {
		t.Fatalf("content = %q, want only the unstaged change", result.Content)
	}
}

func TestDiffShowsStagedChanges(t *testing.T) {
	env, _ := newRepo(t)

	result := run(t, Diff{}, env, `{"staged":true}`)
	if !strings.Contains(result.Content, "lib/b.go") {
		t.Fatalf("content = %q, want the staged file", result.Content)
	}
	if strings.Contains(result.Content, "main.go") {
		t.Fatalf("content = %q, want the staged diff only", result.Content)
	}
	if !strings.Contains(result.Content, "--cached") {
		t.Fatalf("content = %q, want the header to explain the mode", result.Content)
	}
}

func TestDiffSupportsPathsRefsAndStat(t *testing.T) {
	env, _ := newRepo(t)

	result := run(t, Diff{}, env, `{"path":"lib"}`)
	if strings.Contains(result.Content, "main.go") {
		t.Fatalf("content = %q, want the path filter applied", result.Content)
	}

	write(t, filepath.Join(env.Workspace.Path(), "main.go"), "package main\n\nfunc main() {\n\tprintln(\"v3\")\n}\n")

	result = run(t, Diff{}, env, `{"ref":"HEAD"}`)
	if !strings.Contains(result.Content, "println") {
		t.Fatalf("content = %q, want the diff against HEAD", result.Content)
	}

	result = run(t, Diff{}, env, `{"stat":true}`)
	if !strings.Contains(result.Content, "main.go") || !strings.Contains(result.Content, "|") {
		t.Fatalf("content = %q, want the diffstat", result.Content)
	}
}

func TestDiffRefusesBadInput(t *testing.T) {
	env, _ := newRepo(t)

	if err := runErr(t, Diff{}, env, `{"ref":"--upload-pack=evil"}`); err == nil {
		t.Fatal("a ref that looks like an option must be refused")
	}
	if err := runErr(t, Diff{}, env, `{"ref":"HEAD; rm -rf /"}`); err == nil {
		t.Fatal("a ref with shell metacharacters must be refused")
	}
	if err := runErr(t, Diff{}, env, `{"path":"../../../etc/passwd"}`); !apperrors.IsKind(err, apperrors.KindWorkspace) {
		t.Fatalf("kind = %q, want a workspace error", apperrors.KindOf(err))
	}
	if err := runErr(t, Diff{}, env, `{"unknown":true}`); err == nil {
		t.Fatal("an unknown argument must be refused")
	}
}

func TestShowReportsOneCommit(t *testing.T) {
	env, _ := newRepo(t)

	result := run(t, Show{}, env, `{"ref":"HEAD"}`)
	for _, want := range []string{"git.show HEAD", "feat: initial commit", "func main()"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("content is missing %q:\n%s", want, result.Content)
		}
	}

	// The default revision is HEAD.
	result = run(t, Show{}, env, `{}`)
	if !strings.Contains(result.Content, "feat: initial commit") {
		t.Fatalf("content = %q", result.Content)
	}

	// A path narrows the output.
	result = run(t, Show{}, env, `{"path":"lib/a.go"}`)
	if strings.Contains(result.Content, "func main()") {
		t.Fatalf("content = %q, want the path filter applied", result.Content)
	}
}

func TestShowRejectsUnknownRevision(t *testing.T) {
	env, _ := newRepo(t)

	err := runErr(t, Show{}, env, `{"ref":"deadbeefdeadbeef"}`)
	if !apperrors.IsKind(err, apperrors.KindTool) {
		t.Fatalf("kind = %q, want a tool error", apperrors.KindOf(err))
	}
}

func TestStatusListsEveryChange(t *testing.T) {
	env, root := newRepo(t)

	result := run(t, Status{}, env, `{}`)
	for _, want := range []string{"## main", "main.go", "lib/b.go", "notes.md"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("status is missing %q:\n%s", want, result.Content)
		}
	}

	// A directory that is not a repository is reported clearly.
	outside := t.TempDir()
	guard, err := security.NewWorkspace(outside, security.WorkspaceOptions{})
	if err != nil {
		t.Fatalf("NewWorkspace() = %v", err)
	}
	t.Cleanup(func() { _ = guard.Close() })

	err = runErr(t, Status{}, tools.Env{Workspace: guard}, `{}`)
	if !apperrors.IsKind(err, apperrors.KindWorkspace) {
		t.Fatalf("kind = %q, want a workspace error (%v)", apperrors.KindOf(err), err)
	}
	_ = root
}

func TestLogListsCommits(t *testing.T) {
	env, root := newRepo(t)

	write(t, filepath.Join(root, "lib", "a.go"), "package lib\n\nfunc A() {}\n\nfunc C() {}\n")
	gitRun(t, root, "add", "lib/a.go")
	gitRun(t, root, "commit", "-m", "fix: extend A")

	result := run(t, Log{}, env, `{}`)
	if !strings.Contains(result.Content, "fix: extend A") || !strings.Contains(result.Content, "feat: initial commit") {
		t.Fatalf("content = %q", result.Content)
	}

	result = run(t, Log{}, env, `{"limit":1}`)
	if strings.Contains(result.Content, "feat: initial commit") {
		t.Fatalf("content = %q, want the limit applied", result.Content)
	}

	// Following one file.
	result = run(t, Log{}, env, `{"path":"main.go"}`)
	if !strings.Contains(result.Content, "feat: initial commit") {
		t.Fatalf("content = %q", result.Content)
	}
	if strings.Contains(result.Content, "fix: extend A") {
		t.Fatalf("content = %q, want only the commits that touched main.go", result.Content)
	}
}

func TestValidateRef(t *testing.T) {
	valid := []string{"", "HEAD", "main", "HEAD~3", "main...HEAD", "v1.2.3", "abc123^", "origin/main", "HEAD@{1}"}
	for _, ref := range valid {
		if _, err := ValidateRef(ref); err != nil {
			t.Fatalf("ValidateRef(%q) = %v, want it accepted", ref, err)
		}
	}

	invalid := []string{"--upload-pack=evil", "-x", "HEAD; rm -rf /", "HEAD$(whoami)", "HEAD`id`", "HEAD|cat", "a b", strings.Repeat("a", 300)}
	for _, ref := range invalid {
		if _, err := ValidateRef(ref); err == nil {
			t.Fatalf("ValidateRef(%q) = nil, want it refused", ref)
		}
	}
}

func TestValidatePath(t *testing.T) {
	env, root := newRepo(t)
	runner, err := NewRunner(env.Workspace)
	if err != nil {
		t.Fatalf("NewRunner() = %v", err)
	}

	if got, err := runner.ValidatePath(env.Workspace, "lib/a.go"); err != nil || got != "lib/a.go" {
		t.Fatalf("ValidatePath() = %q, %v", got, err)
	}
	if got, err := runner.ValidatePath(env.Workspace, ""); err != nil || got != "" {
		t.Fatalf("ValidatePath(\"\") = %q, %v", got, err)
	}
	if _, err := runner.ValidatePath(env.Workspace, "../../../etc/passwd"); err == nil {
		t.Fatal("an escaping path must be refused")
	}

	// An absolute path inside the workspace is converted to a repository
	// relative path.
	got, err := runner.ValidatePath(env.Workspace, filepath.Join(root, "lib", "a.go"))
	if err != nil || got != "lib/a.go" {
		t.Fatalf("ValidatePath(absolute) = %q, %v", got, err)
	}
}

func TestRunnerRejectsBadPrograms(t *testing.T) {
	env, _ := newRepo(t)
	runner, err := NewRunner(env.Workspace)
	if err != nil {
		t.Fatalf("NewRunner() = %v", err)
	}

	if _, err := runner.Run(context.Background()); err == nil {
		t.Fatal("running git without arguments must be refused")
	}
	if _, err := runner.Run(context.Background(), "status", "bad\x00arg"); err == nil {
		t.Fatal("an argument with a null byte must be refused")
	}

	missing := &Runner{directory: env.Workspace.Path(), timeout: time.Second, binary: "git-not-installed"}
	if _, err := missing.Run(context.Background(), "status"); err == nil {
		t.Fatal("a missing git executable must be reported")
	}
}

func TestRunnerHonoursCancellation(t *testing.T) {
	env, _ := newRepo(t)
	runner, err := NewRunner(env.Workspace)
	if err != nil {
		t.Fatalf("NewRunner() = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = runner.Run(ctx, "status")
	if !apperrors.IsKind(err, apperrors.KindCancelled) {
		t.Fatalf("kind = %q, want a cancelled error (%v)", apperrors.KindOf(err), err)
	}
}

func TestRunnerNeedsAWorkspace(t *testing.T) {
	if _, err := NewRunner(nil); err == nil {
		t.Fatal("a runner without a workspace must be refused")
	}

	_, err := Diff{}.Execute(context.Background(), json.RawMessage(`{}`), tools.Env{})
	if !apperrors.IsKind(err, apperrors.KindInternal) {
		t.Fatalf("kind = %q, want an internal error", apperrors.KindOf(err))
	}
}

func TestLimitedBuffer(t *testing.T) {
	buffer := &limitedBuffer{limit: 8}
	if _, err := buffer.Write([]byte("1234567890")); err != nil {
		t.Fatalf("Write() = %v", err)
	}
	if buffer.String() != "12345678" {
		t.Fatalf("buffer = %q", buffer.String())
	}
	if !buffer.truncated {
		t.Fatal("the buffer must remember that it cut output")
	}

	if _, err := buffer.Write([]byte("more")); err != nil {
		t.Fatalf("Write() = %v", err)
	}
	if buffer.String() != "12345678" {
		t.Fatalf("buffer = %q", buffer.String())
	}
}

func TestDefinitionsCoverEveryGitTool(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(Diff{})
	registry.Register(Show{})
	registry.Register(Status{})
	registry.Register(Log{})

	definitions, err := registry.Definitions(registry.Names())
	if err != nil {
		t.Fatalf("Definitions() = %v", err)
	}
	if len(definitions) != 4 {
		t.Fatalf("definitions = %d, want 4", len(definitions))
	}
	for _, definition := range definitions {
		if !strings.HasPrefix(definition.Name, "git.") {
			t.Fatalf("tool %q must use the git namespace", definition.Name)
		}
	}
}
