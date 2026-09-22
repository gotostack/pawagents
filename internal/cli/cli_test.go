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
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
)

// run executes the CLI with the given arguments and returns stdout, stderr and
// the exit status.
func run(t *testing.T, args ...string) (string, string, int) {
	t.Helper()

	var stdout, stderr bytes.Buffer
	app := NewApp()
	app.SetWriters(&stdout, &stderr)

	root := newRootCmd(app)
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)

	// The tests drive the same execution path as the binary so that they
	// observe the same rendering and the same exit status as a user would.
	code := runRoot(context.Background(), app, root)
	return stdout.String(), stderr.String(), code
}

func TestVersionTextOutput(t *testing.T) {
	stdout, _, code := run(t, "version")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, want 0", code)
	}
	for _, want := range []string{"pagent", "commit:", "built:", "go:", "platform:"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("version output = %q, want it to contain %q", stdout, want)
		}
	}
}

func TestVersionShortAndJSONOutput(t *testing.T) {
	short, _, code := run(t, "version", "--output", "short")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	if len(strings.Fields(short)) != 1 {
		t.Fatalf("short output = %q, want a single field", short)
	}

	stdout, _, code := run(t, "version", "-o", "json")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	var info map[string]any
	if err := json.Unmarshal([]byte(stdout), &info); err != nil {
		t.Fatalf("json output is invalid: %v (%s)", err, stdout)
	}
	if info["binary"] != "pagent" {
		t.Fatalf("binary = %v", info["binary"])
	}
}

func TestVersionRejectsUnknownOutput(t *testing.T) {
	_, stderr, code := run(t, "version", "--output", "yaml")
	if code != apperrors.ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, apperrors.ExitUsage)
	}
	if !strings.Contains(stderr, "unsupported output format") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestConfigPathUsesHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfigPath, "")

	stdout, _, code := run(t, "config", "path")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	if got := strings.TrimSpace(stdout); got != filepath.Join(home, config.ConfigFileName) {
		t.Fatalf("path = %q", got)
	}
}

func TestConfigInitThenValidate(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfigPath, "")

	stdout, _, code := run(t, "config", "init")
	if code != apperrors.ExitOK {
		t.Fatalf("config init exit code = %d, stdout = %s", code, stdout)
	}
	if !strings.Contains(stdout, "wrote") {
		t.Fatalf("config init output = %q", stdout)
	}

	configPath := filepath.Join(home, config.ConfigFileName)
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("configuration file was not created: %v", err)
	}
	promptPath := filepath.Join(home, "prompts", "reviewer.md")
	if _, err := os.Stat(promptPath); err != nil {
		t.Fatalf("prompt file was not created: %v", err)
	}

	stdout, stderr, code := run(t, "config", "validate")
	if code != apperrors.ExitOK {
		t.Fatalf("validate exit code = %d, stdout = %s, stderr = %s", code, stdout, stderr)
	}
	if strings.Contains(stdout, "✗") {
		t.Fatalf("generated configuration should not contain errors:\n%s", stdout)
	}
	if !strings.Contains(stdout, "no problems found") && !strings.Contains(stdout, "warning") {
		t.Fatalf("validate output = %q", stdout)
	}

	// A second init must not silently overwrite the file.
	_, stderr, code = run(t, "config", "init")
	if code == apperrors.ExitOK {
		t.Fatal("config init should refuse to overwrite an existing file")
	}
	if !strings.Contains(stderr, "already exists") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestConfigValidateReportsEveryProblem(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfigPath, "")

	broken := `
version: 1
providers:
  gateway:
    type: openai-compatible
models:
  coder:
    provider: missing
    model: qwen3-coder
agents:
  reviewer:
    model: ghost
    permissions:
      shell: allow
`
	path := filepath.Join(home, config.ConfigFileName)
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatalf("cannot write the test configuration: %v", err)
	}

	stdout, _, code := run(t, "config", "validate")
	if code != apperrors.ExitConfig {
		t.Fatalf("exit code = %d, want %d", code, apperrors.ExitConfig)
	}
	for _, want := range []string{
		"base_url is required",
		`provider "missing" is not defined`,
		`model "ghost" is not defined`,
		"never executes shell commands",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("validate output is missing %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stdout, "error") {
		t.Fatalf("validate output should summarise the problems:\n%s", stdout)
	}
}

func TestConfigValidateRejectsUnknownField(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfigPath, "")

	path := filepath.Join(home, config.ConfigFileName)
	if err := os.WriteFile(path, []byte("version: 1\nproviders:\n  local:\n    type: ollama\n    base_ur: http://x\n"), 0o600); err != nil {
		t.Fatalf("cannot write the test configuration: %v", err)
	}

	_, stderr, code := run(t, "config", "validate")
	if code != apperrors.ExitConfig {
		t.Fatalf("exit code = %d, want %d", code, apperrors.ExitConfig)
	}
	if !strings.Contains(stderr, `unknown field "base_ur"`) {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestConfigShowRedactsSecrets(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfigPath, "")

	path := filepath.Join(home, config.ConfigFileName)
	contents := `
version: 1
providers:
  gateway:
    type: openai-compatible
    base_url: https://llm.example.com/v1
    api_key: sk-must-not-be-printed
    headers:
      Authorization: Bearer must-not-be-printed
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("cannot write the test configuration: %v", err)
	}

	stdout, _, code := run(t, "config", "show")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	if strings.Contains(stdout, "must-not-be-printed") {
		t.Fatalf("config show leaked a credential:\n%s", stdout)
	}
	if !strings.Contains(stdout, "REDACTED") {
		t.Fatalf("config show did not mark the credential:\n%s", stdout)
	}

	stdout, _, code = run(t, "config", "show", "--show-secrets")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(stdout, "sk-must-not-be-printed") {
		t.Fatalf("--show-secrets should print the credential:\n%s", stdout)
	}
}

func TestConfigShowUsesDefaultsWhenFileIsMissing(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv(config.EnvConfigPath, "")

	stdout, stderr, code := run(t, "config", "show")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "showing built-in defaults") {
		t.Fatalf("stdout = %q, want a defaults notice", stdout)
	}
	if !strings.Contains(stdout, "version: 1") {
		t.Fatalf("stdout = %q, want the default version", stdout)
	}

	stdout, stderr, code = run(t, "config", "show", "--output", "json")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(stderr, "showing built-in defaults") {
		t.Fatalf("stderr = %q, want the notice outside the JSON document", stderr)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("json output is invalid: %v (%s)", err, stdout)
	}
}

func TestConfigShowRejectsUnknownFormat(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv(config.EnvConfigPath, "")

	_, stderr, code := run(t, "config", "show", "--output", "toml")
	if code != apperrors.ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, apperrors.ExitUsage)
	}
	if !strings.Contains(stderr, "unsupported output format") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestRootCommandShowsHelp(t *testing.T) {
	stdout, _, code := run(t)
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	for _, want := range []string{"PawAgents", "config", "version", "--config"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("help output is missing %q:\n%s", want, stdout)
		}
	}
}

func TestVerboseFlagEnablesDebugLogging(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv(config.EnvConfigPath, "")

	stdout, _, code := run(t, "config", "init", "--verbose")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(stdout, "wrote") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestGlobalConfigFlagIsHonoured(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.yaml")
	if err := os.WriteFile(path, []byte("version: 1\n"), 0o600); err != nil {
		t.Fatalf("cannot write the test configuration: %v", err)
	}

	stdout, _, code := run(t, "--config", path, "config", "path")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	if got := strings.TrimSpace(stdout); got != path {
		t.Fatalf("path = %q, want %q", got, path)
	}
}
