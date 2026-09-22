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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
)

// writeProviderConfig installs a configuration file with a mix of provider
// types and returns the PawAgents home directory.
func writeProviderConfig(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfigPath, "")

	contents := `
version: 1
providers:
  gateway:
    type: openai-compatible
    base_url: https://llm.example.com/v1
    api_key_env: PAWAGENTS_TEST_GATEWAY_KEY
  local-ollama:
    type: ollama
    base_url: http://127.0.0.1:11434
  cloud-anthropic:
    type: anthropic
    base_url: https://api.anthropic.com
    api_key_env: PAWAGENTS_TEST_ANTHROPIC_KEY
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
    model: deepseek-chat
agents:
  local-reviewer:
    model: local-coder
    instructions: review
    tools:
      - repo.read
`
	path := filepath.Join(home, config.ConfigFileName)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("cannot write the test configuration: %v", err)
	}
	return home
}

func TestProviderListText(t *testing.T) {
	writeProviderConfig(t)

	stdout, stderr, code := run(t, "provider", "list")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}

	for _, want := range []string{"PROVIDER", "TYPE", "ENDPOINT", "CREDENTIAL", "STATUS"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("listing is missing the %q column:\n%s", want, stdout)
		}
	}

	lines := map[string]string{}
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			lines[fields[0]] = line
		}
	}

	if !strings.Contains(lines["local-ollama"], providerStatusReady) {
		t.Fatalf("local-ollama line = %q, want a ready status", lines["local-ollama"])
	}
	if !strings.Contains(lines["gateway"], "ready") {
		t.Fatalf("gateway line = %q, want a ready status", lines["gateway"])
	}
	if !strings.Contains(lines["gateway"], "env:PAWAGENTS_TEST_GATEWAY_KEY") {
		t.Fatalf("gateway line = %q, want the credential description", lines["gateway"])
	}
	if !strings.Contains(lines["cloud-anthropic"], providerStatusUnavailable) {
		t.Fatalf("cloud-anthropic line = %q, want an unavailable status",
			lines["cloud-anthropic"])
	}
	if !strings.Contains(lines["gemini"], "planned") {
		t.Fatalf("gemini line = %q, want a planned status", lines["gemini"])
	}
	if !strings.Contains(lines["switched-off"], "disabled") {
		t.Fatalf("switched-off line = %q, want a disabled status", lines["switched-off"])
	}
}

func TestProviderListJSON(t *testing.T) {
	writeProviderConfig(t)

	stdout, _, code := run(t, "provider", "list", "--output", "json")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	var entries []map[string]any
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		t.Fatalf("output is not valid JSON: %v (%s)", err, stdout)
	}
	if len(entries) != 5 {
		t.Fatalf("entries = %d, want 5", len(entries))
	}

	byName := map[string]map[string]any{}
	for _, entry := range entries {
		byName[entry["name"].(string)] = entry
	}
	if byName["gateway"]["status"] != providerStatusReady {
		t.Fatalf("gateway = %v", byName["gateway"])
	}
	if byName["gemini"]["status"] != providerStatusPlanned {
		t.Fatalf("gemini = %v", byName["gemini"])
	}
	if _, ok := byName["gateway"]["credential"]; !ok {
		t.Fatalf("gateway = %v, want a credential description", byName["gateway"])
	}

	// A credential is only ever described, never rendered: the listing must
	// contain the source of the secret, not the secret.
	for _, entry := range entries {
		credential, _ := entry["credential"].(string)
		switch {
		case credential == "" || credential == "none" || credential == "inline" ||
			credential == "missing" || credential == "invalid":
		case strings.HasPrefix(credential, "env:"):
		default:
			t.Fatalf("credential = %q, want a description such as \"env:NAME\"", credential)
		}
	}
}

func TestProviderListRejectsUnknownOutput(t *testing.T) {
	writeProviderConfig(t)

	_, stderr, code := run(t, "provider", "list", "--output", "yaml")
	if code != apperrors.ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, apperrors.ExitUsage)
	}
	if !strings.Contains(stderr, "unsupported output format") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestProviderListWithoutProviders(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfigPath, "")

	stdout, stderr, code := run(t, "provider", "list")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "no providers configured") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestProviderShowText(t *testing.T) {
	writeProviderConfig(t)

	stdout, stderr, code := run(t, "provider", "show", "gateway")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}

	for _, want := range []string{
		"provider:   gateway",
		"type:       openai-compatible",
		"endpoint:   https://llm.example.com/v1",
		"credential: env:PAWAGENTS_TEST_GATEWAY_KEY",
		"status:     ready",
		"routed (deepseek-chat)",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("show output is missing %q:\n%s", want, stdout)
		}
	}
}

func TestProviderShowJSON(t *testing.T) {
	writeProviderConfig(t)

	stdout, _, code := run(t, "provider", "show", "gateway", "-o", "json")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	var entry map[string]any
	if err := json.Unmarshal([]byte(stdout), &entry); err != nil {
		t.Fatalf("output is not valid JSON: %v (%s)", err, stdout)
	}
	if entry["name"] != "gateway" || entry["type"] != "openai-compatible" {
		t.Fatalf("entry = %v", entry)
	}
	if entry["credential"] != "env:PAWAGENTS_TEST_GATEWAY_KEY" {
		t.Fatalf("credential = %v", entry["credential"])
	}
	models, ok := entry["models"].([]any)
	if !ok || len(models) != 1 || models[0] != "routed (deepseek-chat)" {
		t.Fatalf("models = %v", entry["models"])
	}
}

func TestProviderShowAgentsReferencingTheProvider(t *testing.T) {
	writeProviderConfig(t)

	stdout, _, code := run(t, "provider", "show", "local-ollama")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(stdout, "local-coder (qwen3-coder)") {
		t.Fatalf("stdout = %q, want the model alias", stdout)
	}
	if !strings.Contains(stdout, "local-reviewer") {
		t.Fatalf("stdout = %q, want the agent that uses the model", stdout)
	}
}

func TestProviderShowUnknownName(t *testing.T) {
	writeProviderConfig(t)

	_, stderr, code := run(t, "provider", "show", "absent")
	if code != apperrors.ExitNotFound {
		t.Fatalf("exit code = %d, want %d", code, apperrors.ExitNotFound)
	}
	if !strings.Contains(stderr, `provider "absent" is not defined`) {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestProviderShowRequiresExactlyOneArgument(t *testing.T) {
	writeProviderConfig(t)

	if _, _, code := run(t, "provider", "show"); code == apperrors.ExitOK {
		t.Fatal("a missing provider name must fail")
	}
	if _, _, code := run(t, "provider", "show", "a", "b"); code == apperrors.ExitOK {
		t.Fatal("more than one provider name must fail")
	}
}

func TestProviderCommandShowsHelp(t *testing.T) {
	stdout, _, code := run(t, "provider", "--help")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	for _, want := range []string{"list", "show"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("help output is missing %q:\n%s", want, stdout)
		}
	}
}

func TestBuiltinProviderTypesAreRegistered(t *testing.T) {
	// The blank import in this package must register the compiled-in
	// providers, otherwise `provider list` would report every configured type
	// as unavailable.
	writeProviderConfig(t)

	stdout, _, code := run(t, "provider", "list", "--output", "json")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	var entries []map[string]any
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		t.Fatalf("output is not valid JSON: %v (%s)", err, stdout)
	}

	ready := map[string]bool{}
	for _, entry := range entries {
		if entry["status"] == providerStatusReady {
			ready[entry["type"].(string)] = true
		}
	}
	for _, providerType := range []string{"openai-compatible", "ollama"} {
		if !ready[providerType] {
			t.Fatalf("provider type %q must be compiled in, got %v", providerType, ready)
		}
	}
}
