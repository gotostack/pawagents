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

// Package git implements the read-only git tools: diff, show, status and log.
//
// Running git means running a program, and the tool runtime otherwise refuses
// shell access. The distinction is that these tools never build a command line:
// they execute a fixed git subcommand with an argument vector, validate every
// caller supplied value first, and pass paths after "--" so that a name
// starting with a dash cannot become an option. There is no shell, no
// interpolation and no way for a model to reach an arbitrary program.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/security"
)

// Limits shared by the git tools.
const (
	// MaxOutputBytes caps what one git command may return.
	MaxOutputBytes = 256 * 1024
	// DefaultTimeout bounds one git command. A hook or a slow filesystem must
	// not hang a task.
	DefaultTimeout = 30 * time.Second
	// MaxContextLines bounds the context of a diff.
	MaxContextLines = 10
)

// Runner executes git commands in one repository.
type Runner struct {
	// directory is the repository working tree.
	directory string
	// timeout bounds a single command.
	timeout time.Duration
	// maxOutputBytes caps the captured output.
	maxOutputBytes int
	// binary is the git executable, overridable for tests.
	binary string
}

// NewRunner builds a runner for a workspace.
func NewRunner(workspace *security.Workspace) (*Runner, error) {
	if workspace == nil {
		return nil, apperrors.New(apperrors.KindInternal, "git.runner",
			"the git tools need a workspace guard")
	}
	return &Runner{
		directory:      workspace.Path(),
		timeout:        DefaultTimeout,
		maxOutputBytes: MaxOutputBytes,
		binary:         "git",
	}, nil
}

// Run executes a git command and returns its output.
//
// The output is captured with a hard byte limit, so a repository with a
// gigantic diff cannot exhaust memory before the executor applies its own
// budget.
func (r *Runner) Run(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", apperrors.New(apperrors.KindInternal, "git.run", "no git arguments")
	}
	for _, arg := range args {
		if strings.ContainsRune(arg, 0) {
			return "", apperrors.New(apperrors.KindTool, "git.run",
				"a git argument contains a null byte")
		}
	}

	commandCtx := ctx
	cancel := func() {}
	if r.timeout > 0 {
		commandCtx, cancel = context.WithTimeout(ctx, r.timeout)
	}
	defer cancel()

	// -C <dir> runs git as if it had been started in that directory, and
	// --no-pager stops git from launching a pager that would block on a pipe.
	full := append([]string{"-C", r.directory, "--no-pager"}, args...)
	command := exec.CommandContext(commandCtx, r.binary, full...)

	stdout := &limitedBuffer{limit: r.maxOutputBytes}
	stderr := &limitedBuffer{limit: 8 * 1024}
	command.Stdout = stdout
	command.Stderr = stderr

	err := command.Run()

	if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
		return "", apperrors.New(apperrors.KindTimeout, "git.run",
			"git %s timed out after %s", args[0], r.timeout)
	}
	if errors.Is(commandCtx.Err(), context.Canceled) {
		return "", apperrors.Wrap(apperrors.KindCancelled, "git.run",
			"git %s was cancelled", commandCtx.Err(), args[0])
	}

	if err != nil {
		return stdout.String(), r.failure(args, err, stderr.String())
	}

	output := stdout.String()
	if stdout.truncated {
		output += fmt.Sprintf("\n... [git output truncated at %s]", humanBytes(int64(r.maxOutputBytes)))
	}
	return output, nil
}

// failure turns a git exit status into a classified error.
func (r *Runner) failure(args []string, err error, stderr string) error {
	message := strings.TrimSpace(stderr)
	if message == "" {
		message = err.Error()
	}
	message = sanitize(message)

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if exitErr.ExitCode() == 128 && strings.Contains(message, "not a git repository") {
			return apperrors.New(apperrors.KindWorkspace, "git.run",
				"%s is not a git repository", r.directory).WithDetails(map[string]any{
				"command": args[0],
				"stderr":  message,
			})
		}
		return apperrors.New(apperrors.KindTool, "git.run",
			"git %s failed (exit %d): %s", args[0], exitErr.ExitCode(), message).
			WithDetails(map[string]any{"command": args[0], "stderr": message})
	}

	if errors.Is(err, exec.ErrNotFound) {
		return apperrors.Wrap(apperrors.KindTool, "git.run",
			"the git executable was not found in PATH", err)
	}
	return apperrors.Wrap(apperrors.KindTool, "git.run",
		"cannot run git %s", err, args[0])
}

// ValidateRef checks a caller supplied revision.
//
// A ref becomes a command line argument, so it is restricted to the characters
// git uses and must not start with a dash, which would turn it into an option.
func ValidateRef(ref string) (string, error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return "", nil
	}
	if strings.HasPrefix(trimmed, "-") {
		return "", apperrors.New(apperrors.KindTool, "git.ref",
			"a revision must not start with a dash")
	}
	if len(trimmed) > 256 {
		return "", apperrors.New(apperrors.KindTool, "git.ref",
			"the revision is longer than 256 characters")
	}
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '/' || r == '_' || r == '-' || r == '~' ||
			r == '^' || r == '@' || r == '{' || r == '}' || r == ':':
		default:
			return "", apperrors.New(apperrors.KindTool, "git.ref",
				"the revision %q contains the unsupported character %q", trimmed, string(r))
		}
	}
	return trimmed, nil
}

// ValidatePath checks a caller supplied path against the workspace and returns
// it relative to the repository root.
func (r *Runner) ValidatePath(workspace *security.Workspace, path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", nil
	}
	resolved, err := workspace.Resolve(trimmed)
	if err != nil {
		return "", err
	}
	relative := security.RelPath(r.directory, resolved)
	if relative == "" && resolved != r.directory {
		return "", apperrors.New(apperrors.KindWorkspace, "git.path",
			"path %q is outside the repository", trimmed)
	}
	return relative, nil
}

// limitedBuffer captures output up to a limit and remembers that it cut
// something.
type limitedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

// Write implements io.Writer.
func (b *limitedBuffer) Write(data []byte) (int, error) {
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(data), nil
	}
	if len(data) > remaining {
		b.truncated = true
		data = data[:remaining]
	}
	_, _ = b.buffer.Write(data)
	return len(data), nil
}

// String returns what was captured.
func (b *limitedBuffer) String() string { return b.buffer.String() }

var _ io.Writer = (*limitedBuffer)(nil)

// sanitize collapses a multi-line message into one line for an error.
func sanitize(message string) string {
	lines := strings.Split(strings.ReplaceAll(message, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	joined := strings.Join(out, "; ")
	if len(joined) > 600 {
		joined = joined[:600] + "..."
	}
	return joined
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
