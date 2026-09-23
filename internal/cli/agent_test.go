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

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/orchestrator"
	"github.com/pawagents/pawagents/internal/provider"
)

// writeRegistryConfig installs a configuration that exercises the agent and
// model commands: a local provider, a reachable gateway, a provider type this
// build does not implement, an alias with a fallback, an alias pointing at a
// provider that does not exist, and agents covering every prompt and type case.
//
// It returns the prompt path used by the file-reviewer agent.
func writeRegistryConfig(t *testing.T, endpoint string) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfigPath, "")

	promptPath := filepath.Join(home, "reviewer.md")
	if err := os.WriteFile(promptPath, []byte("Review the diff and report findings."), 0o600); err != nil {
		t.Fatalf("cannot write the prompt file: %v", err)
	}

	contents := fmt.Sprintf(`
version: 1
providers:
  local-ollama:
    type: ollama
    base_url: http://127.0.0.1:11434
  gateway:
    type: openai-compatible
    base_url: %s/v1
    api_key: sk-test
  cloud-gemini:
    type: gemini
  gemini:
    type: gemini
  switched-off:
    type: ollama
    disabled: true
models:
  local-coder:
    provider: local-ollama
    model: qwen3-coder
  routed:
    provider: gateway
    model: fake-model
  with-fallback:
    primary:
      provider: cloud-gemini
      model: gemini-2.5-pro
    fallback:
      - provider: local-ollama
        model: qwen3-coder
  orphan:
    provider: switched-off
    model: ghost
agents:
  file-reviewer:
    description: Deep source-code review
    model: local-coder
    prompt: %s
    tools:
      - repo.read
      - repo.search
      - git.diff
    max_rounds: 24
    timeout_sec: 120
  routed-reviewer:
    description: Review through the gateway
    model: routed
    instructions: review carefully
    tools:
      - repo.read
    output_mode: text
  inline-reviewer:
    model: with-fallback
    instructions: review
    tools:
      - repo.read
  broken:
    model: orphan
    prompt: %s
    tools:
      - repo.read
      - repo.write
  team-reviewer:
    type: team
    model: local-coder
    instructions: review
    strategy: parallel
    members:
      - file-reviewer
`, endpoint, promptPath, filepath.Join(home, "prompts", "reviewer.md"))

	path := filepath.Join(home, config.ConfigFileName)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("cannot write the test configuration: %v", err)
	}
	return promptPath
}

func TestAgentListTextOutput(t *testing.T) {
	writeRegistryConfig(t, "http://127.0.0.1:1")

	stdout, stderr, code := run(t, "agent", "list")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}

	for _, want := range []string{"AGENT", "TYPE", "MODEL", "TARGET", "TOOLS", "OUTPUT", "ROUNDS", "TIMEOUT"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("the listing is missing the %q column:\n%s", want, stdout)
		}
	}

	lines := map[string]string{}
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			lines[fields[0]] = line
		}
	}

	if line := lines["file-reviewer"]; !strings.Contains(line, "local-ollama/qwen3-coder") ||
		!strings.Contains(line, "structured") || !strings.Contains(line, "24") {
		t.Fatalf("file-reviewer row = %q", line)
	}
	if line := lines["routed-reviewer"]; !strings.Contains(line, "gateway/fake-model") ||
		!strings.Contains(line, "text") {
		t.Fatalf("routed-reviewer row = %q", line)
	}
	if line := lines["team-reviewer"]; !strings.Contains(line, "team") {
		t.Fatalf("team-reviewer row = %q", line)
	}
}

func TestAgentListJSONOutput(t *testing.T) {
	writeRegistryConfig(t, "http://127.0.0.1:1")

	stdout, stderr, code := run(t, "agent", "list", "--output", "json")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}

	var profiles []orchestrator.AgentProfile
	if err := json.Unmarshal([]byte(stdout), &profiles); err != nil {
		t.Fatalf("cannot decode the profile list: %v\n%s", err, stdout)
	}
	if len(profiles) != 5 {
		t.Fatalf("profiles = %d, want 5", len(profiles))
	}

	byName := map[string]orchestrator.AgentProfile{}
	for _, profile := range profiles {
		byName[profile.Name] = profile
	}

	reviewer := byName["file-reviewer"]
	if reviewer.Prompt.Kind != orchestrator.PromptFile {
		t.Fatalf("prompt = %+v", reviewer.Prompt)
	}
	if reviewer.OutputMode != config.OutputModeStructured {
		t.Fatalf("output mode = %q", reviewer.OutputMode)
	}
	if len(reviewer.Tools) != 3 || !reviewer.Known() {
		t.Fatalf("tools = %+v", reviewer.Tools)
	}
	if reviewer.Permissions.Filesystem != "read" || reviewer.Permissions.Shell != "deny" {
		t.Fatalf("permissions = %+v", reviewer.Permissions)
	}
	if !reviewer.Executable {
		t.Fatalf("a single agent must be executable")
	}

	if byName["team-reviewer"].Executable {
		t.Fatalf("a team agent must not be executable in this release")
	}
}

func TestAgentShowDescribesTheProfileAndCapabilities(t *testing.T) {
	writeRegistryConfig(t, "http://127.0.0.1:1")

	stdout, stderr, code := run(t, "agent", "show", "file-reviewer")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}

	for _, want := range []string{
		"agent:       file-reviewer",
		"description: Deep source-code review",
		"type:        single",
		"primary:   local-ollama/qwen3-coder",
		"file:",
		"repo.search",
		"permissions: filesystem=read shell=deny",
		"rounds:     24",
		"timeout:    2m0s",
		"Capabilities",
		"needs:      tool_calling, system_message",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("the description is missing %q:\n%s", want, stdout)
		}
	}

	// The static ollama table does not claim tool support, so the report has to
	// say exactly which capability is missing instead of letting a run fail.
	if !strings.Contains(stdout, "missing: tool_calling") {
		t.Fatalf("the missing capability was not reported:\n%s", stdout)
	}
	if !strings.Contains(stdout, "--probe") {
		t.Fatalf("the report must mention how to check the endpoint:\n%s", stdout)
	}
}

func TestAgentShowReportsUnknownToolsAndTheBundledPrompt(t *testing.T) {
	writeRegistryConfig(t, "http://127.0.0.1:1")

	stdout, stderr, code := run(t, "agent", "show", "broken")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}

	if !strings.Contains(stdout, "UNKNOWN") || !strings.Contains(stdout, "repo.write") {
		t.Fatalf("the unknown tool was not reported:\n%s", stdout)
	}
	if !strings.Contains(stdout, "bundled:") {
		t.Fatalf("the bundled prompt fallback was not reported:\n%s", stdout)
	}
}

func TestAgentShowReportsANonExecutableType(t *testing.T) {
	writeRegistryConfig(t, "http://127.0.0.1:1")

	stdout, stderr, code := run(t, "agent", "show", "team-reviewer")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "not executable in this release") {
		t.Fatalf("the type restriction was not reported:\n%s", stdout)
	}
}

func TestAgentShowProbesTheEndpoint(t *testing.T) {
	server := newSSEServer(t, textAnswer("answer"))
	writeRegistryConfig(t, server.server.URL)

	stdout, stderr, code := run(t, "agent", "show", "routed-reviewer", "--probe")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "(probed)") {
		t.Fatalf("the probe result was not reported:\n%s", stdout)
	}
	if !strings.Contains(stdout, "✓ the model satisfies every requirement") {
		t.Fatalf("the probe must satisfy the requirements:\n%s", stdout)
	}
}

func TestAgentShowJSONCarriesProfileAndCapability(t *testing.T) {
	writeRegistryConfig(t, "http://127.0.0.1:1")

	stdout, stderr, code := run(t, "agent", "show", "routed-reviewer", "-o", "json")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}

	var payload struct {
		Profile    orchestrator.AgentProfile     `json:"profile"`
		Capability orchestrator.CapabilityReport `json:"capability"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("cannot decode the description: %v\n%s", err, stdout)
	}
	if payload.Profile.Name != "routed-reviewer" || payload.Profile.ModelAlias != "routed" {
		t.Fatalf("profile = %+v", payload.Profile)
	}
	if payload.Capability.ModelAlias != "routed" {
		t.Fatalf("capability = %+v", payload.Capability)
	}
	if payload.Capability.Probed {
		t.Fatalf("a description without --probe must report static capabilities")
	}
}

func TestAgentShowReportsASkippedPrimaryTarget(t *testing.T) {
	writeRegistryConfig(t, "http://127.0.0.1:1")

	stdout, stderr, code := run(t, "agent", "show", "inline-reviewer")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}

	// The gemini provider is on the roadmap only, so the alias is served by
	// its fallback; the report has to say which target answers and why the
	// other one did not.
	if !strings.Contains(stdout, "skipped:    cloud-gemini/gemini-2.5-pro") {
		t.Fatalf("the skipped primary was not reported:\n%s", stdout)
	}
	if !strings.Contains(stdout, "target:     local-ollama/qwen3-coder") {
		t.Fatalf("the serving target was not reported:\n%s", stdout)
	}
}

func TestAgentShowRejectsBadInput(t *testing.T) {
	writeRegistryConfig(t, "http://127.0.0.1:1")

	cases := []struct {
		name string
		args []string
		code int
	}{
		{name: "unknown agent", args: []string{"agent", "show", "nope"}, code: apperrors.ExitNotFound},
		{name: "missing argument", args: []string{"agent", "show"}, code: apperrors.ExitUsage},
		{name: "bad output", args: []string{"agent", "list", "-o", "yaml"}, code: apperrors.ExitUsage},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, stderr, code := run(t, testCase.args...)
			if code != testCase.code {
				t.Fatalf("exit code = %d, want %d (stderr = %s)", code, testCase.code, stderr)
			}
			if strings.TrimSpace(stderr) == "" {
				t.Fatalf("a failure must be explained on stderr")
			}
		})
	}
}

func TestAgentShowFailsWhenTheProbeCannotReachTheProvider(t *testing.T) {
	// Port 1 is closed, so the capability probe cannot answer.
	writeRegistryConfig(t, "http://127.0.0.1:1")

	_, stderr, code := run(t, "agent", "show", "routed-reviewer", "--probe")
	if code != apperrors.ExitProvider {
		t.Fatalf("exit code = %d, want %d (stderr = %s)", code, apperrors.ExitProvider, stderr)
	}
	if !strings.Contains(stderr, "cannot probe agent") {
		t.Fatalf("stderr = %s", stderr)
	}
}

func TestModelListTextOutput(t *testing.T) {
	writeRegistryConfig(t, "http://127.0.0.1:1")

	stdout, stderr, code := run(t, "model", "list")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}

	for _, want := range []string{"ALIAS", "TARGET", "FALLBACK", "STATUS", "AGENTS"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("the listing is missing the %q column:\n%s", want, stdout)
		}
	}

	lines := map[string]string{}
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			lines[fields[0]] = line
		}
	}

	if line := lines["local-coder"]; !strings.Contains(line, "local-ollama/qwen3-coder") ||
		!strings.Contains(line, provider.StatusReady) || !strings.Contains(line, "file-reviewer") {
		t.Fatalf("local-coder row = %q", line)
	}
	// The gemini provider is not compiled into this build, so the alias can
	// only be served by its fallback, which makes it ready anyway.
	if line := lines["with-fallback"]; !strings.Contains(line, "cloud-gemini/gemini-2.5-pro") ||
		!strings.Contains(line, provider.StatusReady) || !strings.Contains(line, "1") {
		t.Fatalf("with-fallback row = %q", line)
	}
	if line := lines["orphan"]; !strings.Contains(line, "switched-off/ghost") ||
		!strings.Contains(line, provider.StatusDisabled) {
		t.Fatalf("orphan row = %q", line)
	}
}

func TestModelListJSONOutput(t *testing.T) {
	writeRegistryConfig(t, "http://127.0.0.1:1")

	stdout, stderr, code := run(t, "model", "list", "-o", "json")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}

	var entries []orchestrator.ModelEntry
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		t.Fatalf("cannot decode the model list: %v\n%s", err, stdout)
	}
	if len(entries) != 4 {
		t.Fatalf("entries = %d, want 4", len(entries))
	}

	byAlias := map[string]orchestrator.ModelEntry{}
	for _, entry := range entries {
		byAlias[entry.Alias] = entry
	}

	fallback := byAlias["with-fallback"]
	if len(fallback.Targets) != 2 || !fallback.Targets[1].Fallback {
		t.Fatalf("targets = %+v", fallback.Targets)
	}
	if !fallback.Ready {
		t.Fatalf("an alias with a usable fallback is ready: %+v", fallback)
	}
	if len(fallback.Agents) != 1 || fallback.Agents[0] != "inline-reviewer" {
		t.Fatalf("agents = %v", fallback.Agents)
	}
}

func TestModelShowDescribesTargetsAndAgents(t *testing.T) {
	writeRegistryConfig(t, "http://127.0.0.1:1")

	stdout, stderr, code := run(t, "model", "show", "with-fallback")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}

	for _, want := range []string{
		"alias:       with-fallback",
		"primary: cloud-gemini/gemini-2.5-pro",
		"fallback 1: local-ollama/qwen3-coder",
		"status: planned",
		"status: " + provider.StatusReady,
		"Agents",
		"inline-reviewer",
		"--probe",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("the description is missing %q:\n%s", want, stdout)
		}
	}
}

func TestModelShowProbesEveryTarget(t *testing.T) {
	server := newSSEServer(t, textAnswer("answer"))
	writeRegistryConfig(t, server.server.URL)

	stdout, stderr, code := run(t, "model", "show", "routed", "--probe")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "capabilities (probed)") {
		t.Fatalf("the probe result was not reported:\n%s", stdout)
	}
	if !strings.Contains(stdout, "tool_calling") {
		t.Fatalf("the probed capabilities were not reported:\n%s", stdout)
	}
}

func TestModelShowReportsUnreachableEndpoints(t *testing.T) {
	writeRegistryConfig(t, "http://127.0.0.1:1")

	stdout, stderr, code := run(t, "model", "show", "routed", "--probe")
	if code != apperrors.ExitProvider {
		t.Fatalf("exit code = %d, want %d (stderr = %s)", code, apperrors.ExitProvider, stderr)
	}
	if !strings.Contains(stdout, "error:") {
		t.Fatalf("the per target failure was not reported:\n%s", stdout)
	}
	if !strings.Contains(stderr, "could be probed") {
		t.Fatalf("stderr = %s", stderr)
	}
}

func TestModelShowRejectsAnUnknownAlias(t *testing.T) {
	writeRegistryConfig(t, "http://127.0.0.1:1")

	_, stderr, code := run(t, "model", "show", "nope")
	if code != apperrors.ExitNotFound {
		t.Fatalf("exit code = %d, want %d (stderr = %s)", code, apperrors.ExitNotFound, stderr)
	}
}

func TestModelAndAgentCommandsReportAnEmptyConfiguration(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfigPath, "")

	stdout, _, code := run(t, "agent", "list")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(stdout, "no agents configured") {
		t.Fatalf("stdout = %s", stdout)
	}

	stdout, _, code = run(t, "model", "list")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(stdout, "no models configured") {
		t.Fatalf("stdout = %s", stdout)
	}
}

func TestAgentAndModelCommandsAreRegistered(t *testing.T) {
	writeRegistryConfig(t, "http://127.0.0.1:1")

	stdout, _, code := run(t, "agent", "--help")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(stdout, "list") || !strings.Contains(stdout, "show") {
		t.Fatalf("help = %s", stdout)
	}

	stdout, _, code = run(t, "model", "--help")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(stdout, "list") || !strings.Contains(stdout, "show") {
		t.Fatalf("help = %s", stdout)
	}
}
