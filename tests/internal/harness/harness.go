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

// Package harness holds the fixtures the integration tests share: a built
// binary, a scratch home with a generated configuration, a git repository with
// an uncommitted change and a real Ollama endpoint.
//
// The helpers skip rather than fail when the environment cannot support a test,
// and they always say why. An integration test that silently passes because a
// server was missing is worse than one that does not exist.
package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Environment variables that gate the integration tests.
const (
	// EnvOllama enables the tests that need a running Ollama server.
	EnvOllama = "PAWAGENTS_TEST_OLLAMA"
	// EnvMCP enables the MCP tests that spawn the binary.
	EnvMCP = "PAWAGENTS_TEST_MCP"
	// EnvCodex enables the tests that drive the Codex CLI.
	EnvCodex = "PAWAGENTS_TEST_CODEX"
	// EnvClaude enables the tests that drive the Claude Code CLI.
	EnvClaude = "PAWAGENTS_TEST_CLAUDE"
	// EnvModel overrides the model the tests review with.
	EnvModel = "PAWAGENTS_TEST_MODEL"
	// EnvEndpoint overrides the Ollama endpoint.
	EnvEndpoint = "PAWAGENTS_TEST_OLLAMA_ENDPOINT"
)

// DefaultEndpoint is the endpoint a stock Ollama server listens on.
const DefaultEndpoint = "http://127.0.0.1:11434"

var (
	buildOnce sync.Once
	buildPath string
	buildErr  error
)

// RepoRoot returns the module root, which is where the fixtures run the
// toolchain from.
func RepoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("cannot resolve the working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("cannot find the module root: no go.mod above the test directory")
		}
		dir = parent
	}
}

// Binary builds the CLI once per test binary and returns its path.
//
// The tests run the real command line rather than calling into the packages,
// because the exit status and the rendering are part of what a host depends on.
func Binary(t *testing.T) string {
	t.Helper()

	root := RepoRoot(t)
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "pawagents-int-")
		if err != nil {
			buildErr = err
			return
		}
		path := filepath.Join(dir, "pagent")

		cmd := exec.Command("go", "build", "-o", path, "./cmd/pagent")
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build failed: %v\n%s", err, output)
			return
		}
		buildPath = path
	})
	if buildErr != nil {
		t.Fatalf("cannot build the CLI: %v", buildErr)
	}
	return buildPath
}

// Endpoint returns the Ollama endpoint under test.
func Endpoint() string {
	if value := strings.TrimSpace(os.Getenv(EnvEndpoint)); value != "" {
		return strings.TrimRight(value, "/")
	}
	return DefaultEndpoint
}

// Enabled reports whether an integration gate is set.
func Enabled(name string) bool {
	return strings.TrimSpace(os.Getenv(name)) != "" && os.Getenv(name) != "0"
}

// RequireOllama skips the test unless Ollama is reachable, and returns the
// models it has installed.
//
// The tests never assume a specific model: the acceptance path is "review with
// whatever this machine has", and a hard coded model name would turn a machine
// without it into a false failure.
func RequireOllama(t *testing.T) []string {
	t.Helper()

	if !Enabled(EnvOllama) {
		t.Skipf("%s is not set: set it to 1 to run the tests that need a real model",
			EnvOllama)
	}

	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(Endpoint() + "/api/tags")
	if err != nil {
		t.Skipf("Ollama is not reachable at %s: %v", Endpoint(), err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		t.Skipf("Ollama answered %s at %s", response.Status, Endpoint())
	}

	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Skipf("cannot read the model list from %s: %v", Endpoint(), err)
	}

	models := make([]string, 0, len(payload.Models))
	for _, model := range payload.Models {
		if name := strings.TrimSpace(model.Name); name != "" {
			models = append(models, name)
		}
	}
	if len(models) == 0 {
		t.Skipf("Ollama at %s has no model installed; run \"ollama pull <model>\"",
			Endpoint())
	}
	return models
}

// Model returns the model to review with: the override when set, otherwise the
// first model the server has installed.
func Model(t *testing.T, installed []string) string {
	t.Helper()

	if value := strings.TrimSpace(os.Getenv(EnvModel)); value != "" {
		for _, name := range installed {
			if name == value || strings.HasPrefix(name, value+":") {
				return name
			}
		}
		t.Skipf("%s names %q, which is not installed on %s (installed: %s)",
			EnvModel, value, Endpoint(), strings.Join(installed, ", "))
	}
	return installed[0]
}

// OllamaConfig writes a configuration that points at the real server and
// returns the home directory to run the CLI with.
//
// The configuration is deliberately small: one provider, one model alias, one
// agent with the read-only tools, so a failure points at the chain rather than
// at the fixture.
func OllamaConfig(t *testing.T, model string) string {
	t.Helper()

	home := t.TempDir()
	contents := fmt.Sprintf(`
version: 1

providers:
  local-ollama:
    type: ollama
    base_url: %s
    timeout: 120s

models:
  local-coder:
    provider: local-ollama
    model: %s

agents:
  local-reviewer:
    description: Reviews a change with the local model.
    model: local-coder
    instructions: |
      You review code changes and report only what the evidence supports.
      Use the tools to read the change and the files it touches.
      Report at most three findings, ordered by severity.
    output_mode: structured
    max_rounds: 8
    timeout: 10m
    permissions:
      filesystem: read
      shell: deny
    tools:
      - repo.read
      - repo.search
      - repo.list
      - git.diff
      - git.status
`, Endpoint(), model)

	path := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("cannot write the test configuration: %v", err)
	}
	return home
}

// Config writes an arbitrary configuration into a scratch home. The session
// directory stays inside the home, so a test never writes to the real one.
func Config(t *testing.T, contents string) string {
	t.Helper()

	home := t.TempDir()
	path := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("cannot write the test configuration: %v", err)
	}
	return home
}

// Env returns the environment to run the CLI with: the parent environment
// without the variables that would point it at the developer's own home.
func Env(home string) []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "PAWAGENTS_") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "PAWAGENTS_HOME="+home)
}

// Run executes the CLI and returns its output and exit status. A non-zero
// status is returned rather than fatal, because the exit status is part of the
// contract the test asserts.
func Run(t *testing.T, binary string, env []string, args ...string) (string, string, int) {
	t.Helper()

	cmd := exec.Command(binary, args...)
	cmd.Env = env
	cmd.Dir = RepoRoot(t)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("cannot run %s %s: %v", binary, strings.Join(args, " "), err)
		}
		code = exit.ExitCode()
	}
	return stdout.String(), stderr.String(), code
}

// GitRepo creates a repository with one committed file and one uncommitted
// change, so that git.diff has a real diff to read.
//
// The uncommitted change removes a guard. A reviewer that reads the diff has
// one concrete thing to find; a reviewer that does not read it cannot say
// anything grounded about the file.
func GitRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	file := filepath.Join(dir, "ratelimit.go")

	const before = `package limiter

// Allow reports whether a request may proceed.
func Allow(requests, limit int) bool {
	if limit <= 0 {
		return false
	}
	return requests < limit
}
`
	const after = `package limiter

// Allow reports whether a request may proceed.
func Allow(requests, limit int) bool {
	return requests < limit
}
`

	write(t, file, before)
	git(t, dir, "init", "--quiet")
	git(t, dir, "config", "user.email", "tests@pawagents.dev")
	git(t, dir, "config", "user.name", "PawAgents Tests")
	git(t, dir, "add", "ratelimit.go")
	git(t, dir, "commit", "--quiet", "-m", "Add the rate limiter")
	write(t, file, after)

	return dir
}

// write creates a file, failing the test when it cannot.
func write(t *testing.T, path, contents string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("cannot write %s: %v", path, err)
	}
}

// git runs a git command inside a directory.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
}
