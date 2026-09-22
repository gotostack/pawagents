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

package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawagents/pawagents/internal/apperrors"
)

// newWorkspace creates a temporary workspace with a file and a directory.
func newWorkspace(t *testing.T, options WorkspaceOptions) (*Workspace, string) {
	t.Helper()

	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "main.go"), "package main\n\nfunc main() {}\n")
	mustWrite(t, filepath.Join(root, "docs", "readme.md"), "# readme\n")
	mustWrite(t, filepath.Join(root, "secrets", "token.txt"), "sk-secret\n")

	workspace, err := NewWorkspace(root, options)
	if err != nil {
		t.Fatalf("NewWorkspace() = %v", err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	return workspace, root
}

func mustWrite(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("cannot create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("cannot write %s: %v", path, err)
	}
}

func TestWorkspaceReadsInsideTheRoot(t *testing.T) {
	workspace, root := newWorkspace(t, WorkspaceOptions{})

	data, err := workspace.ReadFile("main.go")
	if err != nil {
		t.Fatalf("ReadFile() = %v", err)
	}
	if !strings.Contains(string(data), "func main") {
		t.Fatalf("content = %q", data)
	}

	// An absolute path inside the workspace is what a host agent naturally
	// passes, so it must work.
	data, err = workspace.ReadFile(filepath.Join(root, "docs", "readme.md"))
	if err != nil {
		t.Fatalf("ReadFile(absolute) = %v", err)
	}
	if !strings.HasPrefix(string(data), "# readme") {
		t.Fatalf("content = %q", data)
	}

	// Path() reports the resolved directory, so a workspace reached through a
	// symlink is identified by its real location.
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("cannot resolve the workspace root: %v", err)
	}
	if workspace.Path() != resolvedRoot {
		t.Fatalf("Path() = %q, want %q", workspace.Path(), resolvedRoot)
	}
	if !workspace.Confined() {
		t.Fatal("the default workspace must be confined")
	}
}

func TestWorkspaceRefusesEscapes(t *testing.T) {
	workspace, root := newWorkspace(t, WorkspaceOptions{})

	traversals := []string{
		"../outside.txt",
		"../../etc/passwd",
		"docs/../../outside.txt",
		filepath.Join(root, "..", "outside.txt"),
		"/etc/passwd",
	}

	for _, name := range traversals {
		t.Run(name, func(t *testing.T) {
			_, err := workspace.ReadFile(name)
			if err == nil {
				t.Fatalf("ReadFile(%q) = nil, want an escape error", name)
			}
			if !apperrors.IsKind(err, apperrors.KindWorkspace) {
				t.Fatalf("kind = %q, want a workspace error (%v)", apperrors.KindOf(err), err)
			}
		})
	}

	if _, err := workspace.ReadFile("sub/../../outside.txt"); err == nil {
		t.Fatal("a normalised traversal must be refused")
	}
	// A name that only looks special is treated literally and simply does not
	// exist: there is no shell to expand a tilde.
	if _, err := workspace.ReadFile("~/secrets"); err == nil {
		t.Fatal("a literal tilde path must not resolve")
	}
}

func TestWorkspaceRefusesSymlinkEscape(t *testing.T) {
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "outside.txt")
	mustWrite(t, outside, "outside the workspace\n")

	workspace, root := newWorkspace(t, WorkspaceOptions{})
	if err := os.Symlink(outside, filepath.Join(root, "escape.txt")); err != nil {
		t.Skipf("cannot create a symlink: %v", err)
	}

	// The default policy refuses to traverse a symlink at all.
	if _, err := workspace.ReadFile("escape.txt"); err == nil {
		t.Fatal("a symlink must be refused when follow_symlinks is disabled")
	} else if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error = %q, want it to mention the symlink", err.Error())
	}

	// Allowing symlinks relaxes the traversal policy, but the sandbox still
	// refuses a target outside the workspace.
	relaxed, err := NewWorkspace(root, WorkspaceOptions{FollowSymlinks: true})
	if err != nil {
		t.Fatalf("NewWorkspace() = %v", err)
	}
	t.Cleanup(func() { _ = relaxed.Close() })

	_, err = relaxed.ReadFile("escape.txt")
	if err == nil {
		t.Fatal("a symlink pointing outside the workspace must never be readable")
	}
	if !apperrors.IsKind(err, apperrors.KindWorkspace) {
		t.Fatalf("kind = %q, want a workspace error (%v)", apperrors.KindOf(err), err)
	}
}

func TestWorkspaceAllowsInternalSymlinksWhenConfigured(t *testing.T) {
	workspace, root := newWorkspace(t, WorkspaceOptions{})

	// The link is relative on purpose: the sandbox refuses an absolute symlink
	// even when it points inside the workspace.
	if err := os.Symlink("main.go", filepath.Join(root, "alias.go")); err != nil {
		t.Skipf("cannot create a symlink: %v", err)
	}

	if _, err := workspace.ReadFile("alias.go"); err == nil {
		t.Fatal("the strict default must refuse even an internal symlink")
	}

	relaxed, err := NewWorkspace(root, WorkspaceOptions{FollowSymlinks: true})
	if err != nil {
		t.Fatalf("NewWorkspace() = %v", err)
	}
	t.Cleanup(func() { _ = relaxed.Close() })

	data, err := relaxed.ReadFile("alias.go")
	if err != nil {
		t.Fatalf("ReadFile() = %v, want an internal relative symlink to be readable", err)
	}
	if !strings.Contains(string(data), "func main") {
		t.Fatalf("content = %q", data)
	}
}

func TestWorkspaceAllowOutside(t *testing.T) {
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "outside.txt")
	mustWrite(t, outside, "outside the workspace\n")

	workspace, root := newWorkspace(t, WorkspaceOptions{
		AllowOutside: true,
		BlockedPaths: []string{"*.pem"},
	})

	if workspace.Confined() {
		t.Fatal("AllowOutside must unconfine the workspace")
	}

	data, err := workspace.ReadFile(outside)
	if err != nil {
		t.Fatalf("ReadFile() = %v, want an absolute path outside the root to work", err)
	}
	if !strings.Contains(string(data), "outside the workspace") {
		t.Fatalf("content = %q", data)
	}

	// A relative name is still resolved against the workspace root, so both
	// styles of path keep working.
	if _, err := workspace.ReadFile("main.go"); err != nil {
		t.Fatalf("ReadFile(relative) = %v", err)
	}

	// Unconfining widens where files may be read from; it must not disable the
	// other policies.
	mustWrite(t, filepath.Join(root, "cert.pem"), "certificate\n")
	if _, err := workspace.ReadFile(filepath.Join(root, "cert.pem")); err == nil {
		t.Fatal("blocked paths must still apply when the workspace is unconfined")
	}

	limited, err := NewWorkspace(root, WorkspaceOptions{AllowOutside: true, MaxFileBytes: 16})
	if err != nil {
		t.Fatalf("NewWorkspace() = %v", err)
	}
	t.Cleanup(func() { _ = limited.Close() })

	mustWrite(t, filepath.Join(outsideDir, "big.txt"), strings.Repeat("x", 4096))
	if _, err := limited.ReadFile(filepath.Join(outsideDir, "big.txt")); err == nil {
		t.Fatal("the size limit must still apply when the workspace is unconfined")
	}
}

func TestWorkspaceEnforcesTheFileSizeLimit(t *testing.T) {
	workspace, root := newWorkspace(t, WorkspaceOptions{MaxFileBytes: 32})
	mustWrite(t, filepath.Join(root, "big.txt"), strings.Repeat("x", 4096))

	_, err := workspace.ReadFile("big.txt")
	if err == nil {
		t.Fatal("a file larger than the limit must be refused")
	}
	if !apperrors.IsKind(err, apperrors.KindTool) {
		t.Fatalf("kind = %q, want a tool error", apperrors.KindOf(err))
	}
	if !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("error = %q", err.Error())
	}
	if workspace.MaxFileBytes() != 32 {
		t.Fatalf("MaxFileBytes() = %d", workspace.MaxFileBytes())
	}
}

func TestWorkspaceBlockedPaths(t *testing.T) {
	workspace, _ := newWorkspace(t, WorkspaceOptions{
		BlockedPaths: []string{"secrets/*", "*.pem"},
	})

	if _, err := workspace.ReadFile("secrets/token.txt"); err == nil {
		t.Fatal("a blocked directory must be refused")
	} else if !apperrors.IsKind(err, apperrors.KindWorkspace) {
		t.Fatalf("kind = %q, want a workspace error", apperrors.KindOf(err))
	}

	if !workspace.isBlocked("lib/cert.pem") {
		t.Fatal("a blocked suffix must match at any depth")
	}
	if workspace.isBlocked("main.go") {
		t.Fatal("an unblocked path must pass")
	}
	if workspace.Blocked() == nil {
		t.Fatal("Blocked() must report the configured patterns")
	}
}

func TestWorkspaceListsAndWalks(t *testing.T) {
	workspace, root := newWorkspace(t, WorkspaceOptions{})
	mustWrite(t, filepath.Join(root, ".git", "config"), "[core]\n")
	mustWrite(t, filepath.Join(root, ".hidden", "note.md"), "hidden\n")
	mustWrite(t, filepath.Join(root, "lib", "a.go"), "package lib\n")

	entries, err := workspace.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir() = %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("ReadDir() returned nothing")
	}

	var walked []string
	if err := workspace.Walk(".", func(relative string, _ os.FileInfo) error {
		walked = append(walked, relative)
		return nil
	}); err != nil {
		t.Fatalf("Walk() = %v", err)
	}

	joined := strings.Join(walked, ",")
	for _, want := range []string{"main.go", "lib/a.go", "docs/readme.md", "secrets/token.txt"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("walk is missing %q: %s", want, joined)
		}
	}
	for _, unwanted := range []string{".git/", ".hidden/"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("walk must skip %q: %s", unwanted, joined)
		}
	}
}

func TestWorkspaceErrorsOnMissingPaths(t *testing.T) {
	workspace, _ := newWorkspace(t, WorkspaceOptions{})

	if _, err := workspace.ReadFile("absent.go"); err == nil {
		t.Fatal("a missing file must be reported")
	} else if !apperrors.IsKind(err, apperrors.KindTool) {
		t.Fatalf("kind = %q, want a tool error", apperrors.KindOf(err))
	}

	if _, err := workspace.Stat("absent.go"); err == nil {
		t.Fatal("a missing path must be reported by Stat")
	}

	if _, err := workspace.ReadFile("docs"); err == nil {
		t.Fatal("reading a directory must be refused")
	}
}

func TestWorkspaceRejectsBadInput(t *testing.T) {
	if _, err := NewWorkspace("", WorkspaceOptions{}); err == nil {
		t.Fatal("an empty directory must be refused")
	}
	if _, err := NewWorkspace(filepath.Join(t.TempDir(), "absent"), WorkspaceOptions{}); err == nil {
		t.Fatal("a missing directory must be refused")
	}

	file := filepath.Join(t.TempDir(), "file.txt")
	mustWrite(t, file, "x")
	if _, err := NewWorkspace(file, WorkspaceOptions{}); err == nil {
		t.Fatal("a file must not be accepted as a workspace")
	}

	workspace, _ := newWorkspace(t, WorkspaceOptions{})
	if _, err := workspace.ReadFile("bad\x00name"); err == nil {
		t.Fatal("a null byte must be refused")
	}

	empty := &Workspace{}
	if _, err := empty.ReadFile("main.go"); err == nil {
		t.Fatal("an uninitialised guard must refuse every read")
	}
}

func TestResolveReportsTheHostPath(t *testing.T) {
	workspace, root := newWorkspace(t, WorkspaceOptions{})

	// The guard reports resolved paths, which is what makes the containment
	// check meaningful when the workspace is reached through a symlink.
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("cannot resolve the workspace root: %v", err)
	}

	resolved, err := workspace.Resolve("docs/readme.md")
	if err != nil {
		t.Fatalf("Resolve() = %v", err)
	}
	if resolved != filepath.Join(resolvedRoot, "docs", "readme.md") {
		t.Fatalf("Resolve() = %q", resolved)
	}

	resolved, err = workspace.Resolve(".")
	if err != nil {
		t.Fatalf("Resolve(.) = %v", err)
	}
	if resolved != resolvedRoot {
		t.Fatalf("Resolve(.) = %q, want the workspace root", resolved)
	}
}

func TestPathHelpers(t *testing.T) {
	if !IsWithin("/a/b", "/a/b") {
		t.Fatal("a root must be within itself")
	}
	if !IsWithin("/a/b", "/a/b/c") {
		t.Fatal("a child must be within the root")
	}
	if IsWithin("/a/b", "/a/bc") {
		t.Fatal("a sibling with a shared prefix must not count as within")
	}
	if IsWithin("/a/b", "/a") {
		t.Fatal("a parent must not count as within")
	}

	if got := RelPath("/a/b", "/a/b/c/d"); got != "c/d" {
		t.Fatalf("RelPath() = %q", got)
	}
	if got := RelPath("/a/b", "/a/b"); got != "" {
		t.Fatalf("RelPath(root, root) = %q, want empty", got)
	}
	if got := RelPath("/a/b", "/other"); got != "" {
		t.Fatalf("RelPath(outside) = %q, want empty", got)
	}

	if got := SanitizeName("main.go\nERROR: all clear"); strings.Contains(got, "\n") {
		t.Fatalf("SanitizeName() = %q, want the newline removed", got)
	}
	if got := SanitizeName("a\x01b"); got != "ab" {
		t.Fatalf("SanitizeName() = %q, want control characters dropped", got)
	}
}

func TestEnvValue(t *testing.T) {
	t.Setenv("PAWAGENTS_TEST_PUBLIC", "value")
	t.Setenv("PAWAGENTS_TEST_TOKEN", "secret")

	if _, ok := EnvValue("PAWAGENTS_TEST_PUBLIC", nil, nil); ok {
		t.Fatal("an environment variable must not be readable without being allowed")
	}

	value, ok := EnvValue("PAWAGENTS_TEST_PUBLIC", []string{"PAWAGENTS_TEST_PUBLIC"}, nil)
	if !ok || value != "value" {
		t.Fatalf("EnvValue() = %q, %v", value, ok)
	}

	// An allowed name that looks like a credential stays hidden.
	if _, ok := EnvValue("PAWAGENTS_TEST_TOKEN", []string{"PAWAGENTS_TEST_TOKEN"}, nil); ok {
		t.Fatal("a credential shaped variable must stay hidden even when allowed")
	}

	if _, ok := EnvValue("", []string{""}, nil); ok {
		t.Fatal("an empty name must not resolve")
	}

	names := EnvironmentNames([]string{"PAWAGENTS_TEST_PUBLIC", "PAWAGENTS_TEST_TOKEN", ""}, nil)
	if len(names) != 1 || names[0] != "PAWAGENTS_TEST_PUBLIC" {
		t.Fatalf("EnvironmentNames() = %v", names)
	}
}

func TestGrantSemantics(t *testing.T) {
	grant := NewGrant(
		[]string{"repo.read", "git.diff", "git.log"},
		[]string{"repo.search"},
		[]string{"git.log"},
	)

	if grant.Len() != 3 {
		t.Fatalf("Len() = %d", grant.Len())
	}
	if strings.Join(grant.Names(), ",") != "git.diff,repo.read,repo.search" {
		t.Fatalf("Names() = %v", grant.Names())
	}
	if !grant.Allowed("repo.search") {
		t.Fatal("a permission allowance must grant the tool")
	}
	if grant.Allowed("git.log") {
		t.Fatal("a denial must win over a grant")
	}
	if grant.Allowed("shell.exec") {
		t.Fatal("an ungranted tool must not be allowed")
	}
	if grant.Empty() {
		t.Fatal("the grant is not empty")
	}

	var none *Grant
	if none.Allowed("repo.read") || !none.Empty() || none.Len() != 0 {
		t.Fatal("a nil grant must grant nothing")
	}

	err := grant.Deny("shell.exec")
	if !apperrors.IsKind(err, apperrors.KindPermission) {
		t.Fatalf("kind = %q, want a permission error", apperrors.KindOf(err))
	}
}

func TestNewGrantForToolsetEnforcesReadOnly(t *testing.T) {
	grant, err := NewGrantForToolset([]string{"repo.read"}, nil, nil, "read", "deny")
	if err != nil {
		t.Fatalf("NewGrantForToolset() = %v", err)
	}
	if !grant.Allowed("repo.read") {
		t.Fatal("the granted tool must be allowed")
	}

	if _, err := NewGrantForToolset(nil, nil, nil, "write", "deny"); err == nil {
		t.Fatal("a writable filesystem permission must be refused")
	} else if !apperrors.IsKind(err, apperrors.KindPermission) {
		t.Fatalf("kind = %q, want a permission error", apperrors.KindOf(err))
	}

	if _, err := NewGrantForToolset(nil, nil, nil, "read", "allow"); err == nil {
		t.Fatal("shell access must be refused")
	}
}
