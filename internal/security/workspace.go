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

// Package security owns the workspace guard, the permission checks and the
// secret handling rules.
//
// The workspace guard is built on os.Root, the standard library sandbox: a
// Root opened at the workspace directory refuses any name that would resolve
// outside it, including ".." traversal, absolute paths and symlinks whose
// target escapes. That gives a kernel enforced guarantee instead of a string
// comparison that a clever path can defeat.
package security

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/pawagents/pawagents/internal/apperrors"
)

// Workspace is a guarded directory tree that repository tools may read.
//
// Every filesystem access of the tool runtime goes through this type, so the
// escape rules live in exactly one place.
//
// One standard library rule is worth stating because it surprises people: an
// absolute symlink is never followed, even when it points inside the
// workspace, and a relative symlink is followed only while its target stays
// inside. A repository that relies on absolute symlinks must be made to use
// relative ones.
type Workspace struct {
	// root is the sandbox handle.
	root *os.Root
	// path is the absolute, symlink resolved workspace directory.
	path string
	// confined reports whether access is limited to the workspace tree.
	confined bool
	// followSymlinks reports whether a symlink inside the workspace may be
	// traversed at all.
	followSymlinks bool
	// maxFileBytes caps a single read.
	maxFileBytes int64
	// blocked holds glob patterns that are refused even inside the workspace.
	blocked []string
}

// WorkspaceOptions configures the guard. The zero value is the safe default:
// confined, no symlink traversal, one megabyte reads.
type WorkspaceOptions struct {
	// AllowOutside permits reading outside the workspace root.
	AllowOutside bool
	// FollowSymlinks permits traversing a symlink inside the workspace. The
	// target must still resolve inside the workspace unless AllowOutside is
	// also set.
	FollowSymlinks bool
	// MaxFileBytes caps a single read. Zero means DefaultMaxFileBytes.
	MaxFileBytes int64
	// BlockedPaths are glob patterns refused inside the workspace.
	BlockedPaths []string
}

// DefaultMaxFileBytes is the read cap used when none is configured.
const DefaultMaxFileBytes = 1 << 20

// NewWorkspace opens the guard for a directory.
//
// The directory is resolved through symlinks first, so a workspace reached
// through /tmp on macOS, where /tmp is a symlink to /private/tmp, is treated as
// the real directory and internal symlinks are judged against it.
func NewWorkspace(directory string, options WorkspaceOptions) (*Workspace, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, apperrors.New(apperrors.KindWorkspace, "workspace.open",
			"the workspace directory is empty")
	}

	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, apperrors.Wrap(apperrors.KindWorkspace, "workspace.open",
			"cannot resolve the workspace directory", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, apperrors.Wrap(apperrors.KindWorkspace, "workspace.open",
			"cannot resolve the workspace directory %s", err, absolute)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return nil, apperrors.Wrap(apperrors.KindWorkspace, "workspace.open",
			"cannot open the workspace directory %s", err, resolved)
	}
	if !info.IsDir() {
		return nil, apperrors.New(apperrors.KindWorkspace, "workspace.open",
			"%s is not a directory", resolved)
	}

	// An unconfined workspace is rooted at the filesystem root, so the same
	// code path is used for both modes and the sandbox still refuses names
	// that try to walk above "/".
	rootPath := resolved
	if options.AllowOutside {
		rootPath = string(filepath.Separator)
	}

	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, apperrors.Wrap(apperrors.KindWorkspace, "workspace.open",
			"cannot open the workspace root %s", err, rootPath)
	}

	maxBytes := options.MaxFileBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxFileBytes
	}

	blocked := make([]string, 0, len(options.BlockedPaths))
	for _, pattern := range options.BlockedPaths {
		if trimmed := strings.TrimSpace(pattern); trimmed != "" {
			blocked = append(blocked, trimmed)
		}
	}

	return &Workspace{
		root:           root,
		path:           resolved,
		confined:       !options.AllowOutside,
		followSymlinks: options.FollowSymlinks,
		maxFileBytes:   maxBytes,
		blocked:        blocked,
	}, nil
}

// NewWorkspaceFromOptions builds the guard from plain options.
//
// It exists so that a caller holding a configuration can build the guard
// without this package depending on the configuration package: the dependency
// only ever points from configuration to security.
func NewWorkspaceFromOptions(directory string, options WorkspaceOptions) (*Workspace, error) {
	return NewWorkspace(directory, options)
}

// Path returns the workspace directory.
func (w *Workspace) Path() string {
	if w == nil {
		return ""
	}
	return w.path
}

// Confined reports whether access is limited to the workspace tree.
func (w *Workspace) Confined() bool { return w != nil && w.confined }

// MaxFileBytes returns the read cap.
func (w *Workspace) MaxFileBytes() int64 {
	if w == nil {
		return DefaultMaxFileBytes
	}
	return w.maxFileBytes
}

// Blocked returns the configured blocked path patterns.
func (w *Workspace) Blocked() []string {
	if w == nil {
		return nil
	}
	return append([]string(nil), w.blocked...)
}

// Close releases the sandbox handle.
func (w *Workspace) Close() error {
	if w == nil || w.root == nil {
		return nil
	}
	return w.root.Close()
}

// Resolve validates a caller supplied path and returns the absolute host path
// it refers to. It is used for display and for the git tools, which need a
// real path, and it never grants access on its own: reads go through the
// sandbox methods below.
func (w *Workspace) Resolve(name string) (string, error) {
	rootName, err := w.rootName(name)
	if err != nil {
		return "", err
	}
	if rootName == "." {
		return w.path, nil
	}
	if w.confined {
		return filepath.Join(w.path, filepath.FromSlash(rootName)), nil
	}
	return filepath.Join(string(filepath.Separator), filepath.FromSlash(rootName)), nil
}

// ReadFile reads a file through the sandbox, enforcing the size cap.
func (w *Workspace) ReadFile(name string) ([]byte, error) {
	rootName, err := w.rootName(name)
	if err != nil {
		return nil, err
	}

	info, err := w.root.Stat(rootName)
	if err != nil {
		return nil, w.statError(name, rootName, err)
	}
	if info.IsDir() {
		return nil, apperrors.New(apperrors.KindTool, "workspace.read",
			"%s is a directory", name)
	}
	if info.Size() > w.maxFileBytes {
		return nil, apperrors.New(apperrors.KindTool, "workspace.read",
			"%s is %s bytes, larger than the %s byte limit",
			name, humanBytes(info.Size()), humanBytes(w.maxFileBytes)).
			WithDetails(map[string]any{"limit_bytes": w.maxFileBytes, "size_bytes": info.Size()})
	}

	data, err := w.root.ReadFile(rootName)
	if err != nil {
		return nil, w.openError(name, err)
	}
	// The size check above races with a concurrent writer, so the result is
	// capped again after the read.
	if int64(len(data)) > w.maxFileBytes {
		return data[:w.maxFileBytes], nil
	}
	return data, nil
}

// Stat returns the file information of a path inside the sandbox.
func (w *Workspace) Stat(name string) (fs.FileInfo, error) {
	rootName, err := w.rootName(name)
	if err != nil {
		return nil, err
	}
	info, err := w.root.Stat(rootName)
	if err != nil {
		return nil, w.statError(name, rootName, err)
	}
	return info, nil
}

// Open opens a file inside the sandbox for reading.
func (w *Workspace) Open(name string) (*os.File, error) {
	rootName, err := w.rootName(name)
	if err != nil {
		return nil, err
	}
	file, err := w.root.Open(rootName)
	if err != nil {
		return nil, w.openError(name, err)
	}
	return file, nil
}

// ReadDir lists a directory inside the sandbox.
func (w *Workspace) ReadDir(name string) ([]fs.DirEntry, error) {
	rootName, err := w.rootName(name)
	if err != nil {
		return nil, err
	}
	dir, err := w.root.Open(rootName)
	if err != nil {
		return nil, w.openError(name, err)
	}
	defer func() { _ = dir.Close() }()

	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, apperrors.Wrap(apperrors.KindTool, "workspace.list",
			"cannot list %s", err, name)
	}
	return entries, nil
}

// Walk calls fn for every regular file below directory, skipping blocked paths
// and never following a symlink. It is used by repo.search so that the
// traversal rules stay in one place.
func (w *Workspace) Walk(directory string, fn func(relative string, info fs.FileInfo) error) error {
	rootName, err := w.rootName(directory)
	if err != nil {
		return err
	}
	return w.walk(rootName, fn)
}

func (w *Workspace) walk(rootName string, fn func(string, fs.FileInfo) error) error {
	dir, err := w.root.Open(rootName)
	if err != nil {
		return w.openError(rootName, err)
	}
	entries, err := dir.ReadDir(-1)
	_ = dir.Close()
	if err != nil {
		return apperrors.Wrap(apperrors.KindTool, "workspace.walk",
			"cannot list %s", err, rootName)
	}

	for _, entry := range entries {
		name := path.Join(rootName, entry.Name())
		if w.isBlocked(name) {
			continue
		}
		// Hidden entries, and .git in particular, are skipped: they are noise
		// for a search and can be very large.
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if entry.IsDir() {
			// Symlinked directories are skipped rather than followed, which
			// keeps the walk inside the tree and avoids cycles.
			if entry.Type()&fs.ModeSymlink != 0 {
				continue
			}
			if err := w.walk(name, fn); err != nil {
				return err
			}
			continue
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if err := fn(name, info); err != nil {
			return err
		}
	}
	return nil
}

// rootName validates a caller supplied path and returns it as a slash
// separated name relative to the sandbox root.
//
// Every method funnels through it, so the blocked path policy and the symlink
// policy apply to reads, stats, listings and directory walks alike.
func (w *Workspace) rootName(name string) (string, error) {
	if w == nil || w.root == nil {
		return "", apperrors.New(apperrors.KindInternal, "workspace.guard",
			"the workspace guard is not initialised")
	}
	if strings.ContainsRune(name, 0) {
		return "", apperrors.New(apperrors.KindWorkspace, "workspace.guard",
			"the path contains a null byte")
	}

	base := "."
	if !w.confined {
		base = relativeToRoot(w.path)
	}

	trimmed := strings.TrimSpace(name)
	if trimmed == "" || trimmed == "." {
		return base, nil
	}

	slashed := filepath.ToSlash(trimmed)
	cleaned := ""

	if path.IsAbs(slashed) {
		if w.confined {
			// A host agent passes the path it knows, which may reach the
			// workspace through a symlink (macOS /tmp for example). Resolving
			// the input first compares like with like, and it cannot hide an
			// escape: resolving only ever makes the target more concrete.
			candidate := filepath.FromSlash(slashed)
			if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
				candidate = resolved
			}
			relative, err := filepath.Rel(w.path, candidate)
			if err != nil || relative == ".." || strings.HasPrefix(relative, "../") {
				return "", escapeError("workspace.guard", trimmed, w.path)
			}
			cleaned = path.Clean(filepath.ToSlash(relative))
		} else {
			cleaned = path.Clean(strings.TrimPrefix(slashed, "/"))
		}
	} else {
		cleaned = path.Clean(slashed)
		if w.confined && (cleaned == ".." || strings.HasPrefix(cleaned, "../")) {
			return "", escapeError("workspace.guard", trimmed, w.path)
		}
		if !w.confined {
			cleaned = relativeToRoot(filepath.Join(w.path, filepath.FromSlash(cleaned)))
		}
	}

	if cleaned == "" || cleaned == "." {
		return base, nil
	}
	if w.isBlocked(cleaned) {
		return "", blockedError(cleaned)
	}
	if w.confined {
		if err := w.checkSymlinks(cleaned); err != nil {
			return "", err
		}
	}
	return cleaned, nil
}

// checkSymlinks refuses to traverse a symlink when the configuration disables
// it. The sandbox already prevents an escaping target; this check is the
// stricter policy that a workspace with symlinks must opt out of.
func (w *Workspace) checkSymlinks(cleaned string) error {
	if w.followSymlinks || cleaned == "." {
		return nil
	}

	segments := strings.Split(cleaned, "/")
	current := ""
	for _, segment := range segments {
		if segment == "" {
			continue
		}
		if current == "" {
			current = segment
		} else {
			current = current + "/" + segment
		}
		info, err := w.root.Lstat(current)
		if err != nil {
			// A missing component is reported by the real operation with a
			// better message.
			return nil
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return apperrors.New(apperrors.KindWorkspace, "workspace.guard",
				"%s is a symlink; set security.follow_symlinks to true to allow it", current)
		}
	}
	return nil
}

// isBlocked reports whether a relative path matches a blocked pattern.
func (w *Workspace) isBlocked(relative string) bool {
	if len(w.blocked) == 0 {
		return false
	}
	cleaned := strings.TrimPrefix(path.Clean(relative), "./")
	base := path.Base(cleaned)

	for _, pattern := range w.blocked {
		normalized := strings.TrimPrefix(pattern, "./")
		normalized = strings.TrimPrefix(normalized, "**/")
		if ok, err := path.Match(normalized, cleaned); err == nil && ok {
			return true
		}
		if ok, err := path.Match(normalized, base); err == nil && ok {
			return true
		}
	}
	return false
}

// statError converts a stat failure into a classified error.
func (w *Workspace) statError(name, rootName string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return apperrors.Wrap(apperrors.KindTool, "workspace.stat",
			"%s does not exist", err, name)
	}
	return w.openError(name, err)
}

// openError converts an open failure into a classified error, recognising the
// escape errors the sandbox reports.
func (w *Workspace) openError(name string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return apperrors.Wrap(apperrors.KindTool, "workspace.open",
			"%s does not exist", err, name)
	}
	if errors.Is(err, fs.ErrPermission) {
		return apperrors.Wrap(apperrors.KindPermission, "workspace.open",
			"%s is not readable", err, name)
	}

	message := err.Error()
	if strings.Contains(message, "outside root") || strings.Contains(message, "path escapes") {
		return escapeError("workspace.open", name, w.path)
	}
	return apperrors.Wrap(apperrors.KindTool, "workspace.open",
		"cannot open %s", err, name)
}

// escapeError builds the error every escape attempt produces.
func escapeError(op, name, root string) error {
	return apperrors.New(apperrors.KindWorkspace, op,
		"path %q is outside the workspace root", name).
		WithDetails(map[string]any{"path": name, "workspace": root})
}

// blockedError builds the error a blocked path produces.
func blockedError(name string) error {
	return apperrors.New(apperrors.KindWorkspace, "workspace.guard",
		"path %q is blocked by security.blocked_paths", name).
		WithDetails(map[string]any{"path": name})
}

// relativeToRoot converts an absolute path into a sandbox name.
func relativeToRoot(absolute string) string {
	cleaned := filepath.ToSlash(absolute)
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "" {
		return "."
	}
	return cleaned
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
