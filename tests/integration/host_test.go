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

//go:build integration

package integration

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/pawagents/pawagents/tests/internal/harness"
)

// The host tests register the built binary with a real host CLI and ask the
// host whether the connection works. That is the host half of the chain:
//
//	Codex / Claude Code -> MCP -> PawAgents
//
// The other half, PawAgents -> Ollama, is covered by the review tests and by
// the delegation test in tests/mcp, because a host's own model configuration
// belongs to the host and not to this project. A machine whose Claude Code
// points at a gateway model, for example, can still prove the connection and
// nothing more.
//
// Both tests are gated and both clean up after themselves: they write to a
// host's user configuration, so a test that leaves an entry behind would break
// the developer's own environment.

// hostBinary returns the CLI to register, or skips when it is not installed.
func hostBinary(t *testing.T, gate, command string) string {
	t.Helper()

	if !harness.Enabled(gate) {
		t.Skipf("%s is not set: set it to 1 to drive the %s CLI", gate, command)
	}
	path, err := exec.LookPath(command)
	if err != nil {
		t.Skipf("the %s CLI is not on PATH: %v", command, err)
	}

	// A wrapper that exists but cannot run (a broken install, a missing
	// vendored binary) is an environment problem, not a failure of this
	// project, so it skips with the reason instead of failing.
	if output, err := exec.Command(path, "--version").CombinedOutput(); err != nil {
		t.Skipf("the %s CLI is installed but does not run: %v\n%s", command, err, output)
	}
	return path
}

// TestCodexCLIRegistersTheServer walks the Codex route from the README.
func TestCodexCLIRegistersTheServer(t *testing.T) {
	codex := hostBinary(t, harness.EnvCodex, "codex")
	binary := harness.Binary(t)

	// The server needs a configuration to serve; the host spawns it with the
	// environment of this command.
	home := harness.Config(t, offlineConfig())
	env := harness.Env(home)

	// Never clobber an existing entry: a developer may already use PawAgents
	// from Codex.
	if list, _, code := runHost(t, env, codex, "mcp", "list"); code == 0 &&
		strings.Contains(list, "pawagents") {
		t.Skip("codex already has a pawagents server; remove it to run this test")
	}

	if _, stderr, code := runHost(t, env, codex,
		"mcp", "add", "pawagents", "--", binary, "mcp", "serve", "--stdio"); code != 0 {
		t.Fatalf("codex mcp add exited %d:\n%s", code, stderr)
	}
	registered := true
	t.Cleanup(func() {
		if !registered {
			return
		}
		if _, stderr, code := runHost(t, env, codex, "mcp", "remove", "pawagents"); code != 0 {
			t.Errorf("codex mcp remove exited %d, so the registration is still there:\n%s",
				code, stderr)
		}
	})

	list, stderr, code := runHost(t, env, codex, "mcp", "list")
	if code != 0 {
		t.Fatalf("codex mcp list exited %d:\n%s", code, stderr)
	}
	if !strings.Contains(list, "pawagents") {
		t.Fatalf("codex does not list the server it just accepted:\n%s", list)
	}

	if _, stderr, code := runHost(t, env, codex, "mcp", "remove", "pawagents"); code != 0 {
		t.Fatalf("codex mcp remove exited %d:\n%s", code, stderr)
	}
	registered = false

	list, _, code = runHost(t, env, codex, "mcp", "list")
	if code == 0 && strings.Contains(list, "pawagents") {
		t.Fatalf("the registration survived its removal:\n%s", list)
	}
}

// TestClaudeCodeCLIConnectsToTheServer walks the Claude Code route and asks the
// host itself whether the connection works.
//
// `claude mcp list` starts the server, completes the handshake and fetches the
// tools, so a success here means a host accepted the schemas this server
// returns. That is the check that caught the list_agents output schema, and it
// is the cheapest honest host verification there is.
func TestClaudeCodeCLIConnectsToTheServer(t *testing.T) {
	claude := hostBinary(t, harness.EnvClaude, "claude")
	binary := harness.Binary(t)
	home := harness.Config(t, offlineConfig())
	env := harness.Env(home)

	if list, _, code := runHost(t, env, claude, "mcp", "list"); code == 0 &&
		strings.Contains(list, "pawagents") {
		t.Skip("claude already has a pawagents server; remove it to run this test")
	}

	if _, stderr, code := runHost(t, env, claude,
		"mcp", "add", "--scope", "user", "pawagents", "--",
		binary, "mcp", "serve", "--stdio"); code != 0 {
		t.Fatalf("claude mcp add exited %d:\n%s", code, stderr)
	}
	registered := true
	t.Cleanup(func() {
		if !registered {
			return
		}
		if _, stderr, code := runHost(t, env, claude,
			"mcp", "remove", "--scope", "user", "pawagents"); code != 0 {
			t.Errorf("claude mcp remove exited %d, so the registration is still there:\n%s",
				code, stderr)
		}
	})

	list, stderr, code := runHost(t, env, claude, "mcp", "list")
	if code != 0 {
		t.Fatalf("claude mcp list exited %d:\n%s", code, stderr)
	}
	if !strings.Contains(list, "pawagents") {
		t.Fatalf("claude does not list the server it just accepted:\n%s", list)
	}
	if !strings.Contains(list, "Connected") {
		t.Fatalf("claude did not connect to the server; the handshake or the tool "+
			"schemas were rejected:\n%s", list)
	}

	if _, stderr, code := runHost(t, env, claude,
		"mcp", "remove", "--scope", "user", "pawagents"); code != 0 {
		t.Fatalf("claude mcp remove exited %d:\n%s", code, stderr)
	}
	registered = false
}

// runHost runs a host CLI with the environment of the server it will spawn.
func runHost(t *testing.T, env []string, binary string, args ...string) (string, string, int) {
	t.Helper()

	cmd := exec.Command(binary, args...)
	cmd.Env = env
	cmd.Dir = harness.RepoRoot(t)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	code := 0
	if err := cmd.Run(); err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("cannot run %s %s: %v", binary, strings.Join(args, " "), err)
		}
		code = exit.ExitCode()
	}
	return stdout.String(), stderr.String(), code
}

// offlineConfig is a configuration that needs no endpoint, so the host tests
// work on a machine without a provider.
func offlineConfig() string {
	return `
version: 1

providers:
  local-endpoint:
    type: openai-compatible
    base_url: https://api.example.invalid/v1
    api_key_env: PAWAGENTS_TEST_ABSENT_KEY

models:
  slow-coder:
    provider: local-endpoint
    model: some-model

agents:
  reviewer:
    description: Reviews a change.
    model: slow-coder
    instructions: Review the change.
    output_mode: structured
    tools:
      - repo.read
      - git.diff
`
}
