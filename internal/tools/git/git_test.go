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

// newMarkedRepo builds a temporary repository whose paths and commit subject
// carry a marker, so a test can tell which repository a git command read.
func newMarkedRepo(t *testing.T, marker string) (tools.Env, string) {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	root := t.TempDir()
	write(t, filepath.Join(root, marker+".go"), "package main\n\n// "+marker+"\n")

	gitRun(t, root, "init", "--initial-branch=main")
	gitRun(t, root, "config", "user.email", "test@example.com")
	gitRun(t, root, "config", "user.name", "PawAgents Test")
	gitRun(t, root, "config", "commit.gpgsign", "false")
	gitRun(t, root, "add", ".")
	gitRun(t, root, "commit", "-m", "feat: "+marker+" commit")

	// An uncommitted change gives every git tool something to report.
	write(t, filepath.Join(root, marker+".go"), "package main\n\n// "+marker+" changed\n")

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

// TestRunnerIgnoresInheritedRepositoryVariables proves the git tools read the
// guarded workspace even when whatever started PawAgents exported the variables
// git uses to select a repository. The workspace and the repository the
// environment points at are two different temporary repositories.
func TestRunnerIgnoresInheritedRepositoryVariables(t *testing.T) {
	const (
		workspaceMarker = "WorkspaceMarker"
		externalMarker  = "ExternalMarker"
	)

	env, _ := newMarkedRepo(t, workspaceMarker)
	_, externalRoot := newMarkedRepo(t, externalMarker)

	externalGitDir := filepath.Join(externalRoot, ".git")
	t.Setenv("GIT_DIR", externalGitDir)
	t.Setenv("GIT_WORK_TREE", externalRoot)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(externalGitDir, "index"))
	t.Setenv("GIT_COMMON_DIR", externalGitDir)
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(externalGitDir, "objects"))
	t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", filepath.Join(externalGitDir, "objects"))
	t.Setenv("GIT_NAMESPACE", "external")

	everyTool := []struct {
		name string
		tool tools.Tool
	}{
		{DiffToolName, Diff{}},
		{ShowToolName, Show{}},
		{StatusToolName, Status{}},
		{LogToolName, Log{}},
	}

	for _, entry := range everyTool {
		t.Run(entry.name, func(t *testing.T) {
			result := run(t, entry.tool, env, `{}`)
			if !strings.Contains(result.Content, workspaceMarker) {
				t.Fatalf("content = %q, want the %s repository", result.Content, workspaceMarker)
			}
			if strings.Contains(result.Content, externalMarker) {
				t.Fatalf("content = %q, must not read the %s repository", result.Content, externalMarker)
			}
		})
	}

	// The runner must not rewrite the environment of its own process.
	if got := os.Getenv("GIT_DIR"); got != externalGitDir {
		t.Fatalf("GIT_DIR = %q, the runner must leave the parent environment alone", got)
	}
}

// TestRunnerBuildsAMinimalEnvironment checks the allowlist directly: the
// repository-selecting variables never reach the child, the safety settings do,
// and nothing is inherited by accident.
func TestRunnerBuildsAMinimalEnvironment(t *testing.T) {
	env, _ := newRepo(t)
	runner, err := NewRunner(env.Workspace)
	if err != nil {
		t.Fatalf("NewRunner() = %v", err)
	}

	poisoned := []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR",
		"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE",
		"GIT_EXEC_PATH", "GIT_SSH_COMMAND", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM",
		"GIT_ATTR_NOSYSTEM",
	}
	for _, name := range poisoned {
		t.Setenv(name, "poisoned")
	}

	// The variables the runner sets on purpose, in addition to the handful of
	// harmless platform ones it passes on.
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "TMPDIR": true,
		"GIT_TERMINAL_PROMPT": true, "GIT_PAGER": true,
		"GIT_OPTIONAL_LOCKS": true, "GIT_CONFIG_NOSYSTEM": true,
	}

	present := make(map[string]bool)
	for _, entry := range runner.environment() {
		name, _, _ := strings.Cut(entry, "=")
		present[name] = true
		if !allowed[name] {
			t.Fatalf("the child environment must not carry %s", name)
		}
	}

	// The runner builds the child environment; it must not edit its own.
	for _, name := range poisoned {
		if got := os.Getenv(name); got != "poisoned" {
			t.Fatalf("%s = %q, the runner must leave the parent environment alone", name, got)
		}
	}

	for _, name := range []string{
		"PATH", "GIT_TERMINAL_PROMPT", "GIT_PAGER", "GIT_OPTIONAL_LOCKS", "GIT_CONFIG_NOSYSTEM",
	} {
		if !present[name] {
			t.Fatalf("the child environment must define %s", name)
		}
	}

	// A poisoned parent environment must still leave the tools working.
	output, err := runner.Run(context.Background(), "status", "--porcelain")
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if !strings.Contains(output, "main.go") {
		t.Fatalf("git status = %q, want the workspace repository", output)
	}
}

// TestRunnerDoesNotUseAShell pins the rule that git is started directly: an
// argument is an argument, never a command a shell could run.
func TestRunnerDoesNotUseAShell(t *testing.T) {
	env, root := newRepo(t)
	runner, err := NewRunner(env.Workspace)
	if err != nil {
		t.Fatalf("NewRunner() = %v", err)
	}

	canary := filepath.Join(root, "canary")
	if _, err := runner.Run(context.Background(), "status", "; touch "+canary); err == nil {
		// git may accept the argument as a pathspec; what matters is that no
		// shell ran it.
		t.Log("git accepted the argument as a pathspec")
	}
	if _, statErr := os.Stat(canary); statErr == nil {
		t.Fatalf("the argument was executed: %s exists, so git ran through a shell", canary)
	}
}

// TestRunnerTimesOutASlowCommand keeps the timeout behaviour: a git command
// that never returns is stopped and reported as a timeout.
func TestRunnerTimesOutASlowCommand(t *testing.T) {
	if DefaultTimeout != 30*time.Second {
		t.Fatalf("DefaultTimeout = %s, want the documented 30s", DefaultTimeout)
	}

	env, _ := newRepo(t)
	runner, err := NewRunner(env.Workspace)
	if err != nil {
		t.Fatalf("NewRunner() = %v", err)
	}

	// A program that never exits stands in for a git command blocked on a
	// hook or a stuck filesystem. The short timeout only keeps the test fast.
	blocking, err := exec.LookPath("yes")
	if err != nil {
		t.Skip("the yes program is not installed")
	}
	runner.binary = blocking
	runner.timeout = 200 * time.Millisecond

	started := time.Now()
	_, err = runner.Run(context.Background(), "status")
	elapsed := time.Since(started)

	if !apperrors.IsKind(err, apperrors.KindTimeout) {
		t.Fatalf("kind = %q, want a timeout error (%v)", apperrors.KindOf(err), err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("the command ran for %s, want it stopped at its timeout", elapsed)
	}
}

// TestRunnerTruncatesLargeOutput keeps the output cap: a command that produces
// more than the limit is cut and says so.
func TestRunnerTruncatesLargeOutput(t *testing.T) {
	env, _ := newRepo(t)
	runner, err := NewRunner(env.Workspace)
	if err != nil {
		t.Fatalf("NewRunner() = %v", err)
	}
	runner.maxOutputBytes = 64

	output, err := runner.Run(context.Background(), "log", "-p")
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if !strings.Contains(output, "[git output truncated at 64 B]") {
		t.Fatalf("output = %q, want the truncation notice", output)
	}

	captured, _, _ := strings.Cut(output, "\n... [git output truncated")
	if len(captured) != 64 {
		t.Fatalf("captured %d bytes, want exactly the 64 byte limit", len(captured))
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
