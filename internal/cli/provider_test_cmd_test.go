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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
)

// ollamaFixture writes a configuration whose local-ollama provider points at
// the given test server.
func ollamaFixture(t *testing.T, endpoint string) {
	t.Helper()

	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfigPath, "")

	contents := `
version: 1
providers:
  local-ollama:
    type: ollama
    base_url: ` + endpoint + `
models:
  local-coder:
    provider: local-ollama
    model: qwen3-coder
agents:
  local-reviewer:
    model: local-coder
    instructions: review the code
    tools:
      - repo.read
      - repo.search
`
	path := filepath.Join(home, config.ConfigFileName)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("cannot write the test configuration: %v", err)
	}
}

// ollamaServer replies to the endpoints the provider test command probes.
func ollamaServer(t *testing.T, capabilities []string, contextLength int, installed []string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/version":
			_, _ = io.WriteString(w, `{"version":"0.12.3"}`)
		case "/api/tags":
			type entry struct {
				Name string `json:"name"`
			}
			models := make([]entry, 0, len(installed))
			for _, name := range installed {
				models = append(models, entry{Name: name})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
		case "/api/show":
			encoded, _ := json.Marshal(capabilities)
			_, _ = io.WriteString(w, `{"details":{"family":"qwen2"},"model_info":{"qwen2.context_length":`+
				json.Number(itoa(contextLength)).String()+`},"capabilities":`+string(encoded)+`}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

func TestProviderTestHappyPath(t *testing.T) {
	server := ollamaServer(t, []string{"completion", "tools"}, 32768, []string{"qwen3-coder:latest"})
	ollamaFixture(t, server.URL)

	stdout, stderr, code := run(t, "provider", "test", "local-ollama")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, stdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	for _, want := range []string{
		"provider: local-ollama",
		"type:     ollama",
		"Ollama 0.12.3",
		"local-coder → qwen3-coder",
		"tool_calling",
		"max_context_tokens=32768",
		"every check passed",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("report is missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "✗") {
		t.Fatalf("a passing report must not contain failures:\n%s", stdout)
	}
}

func TestProviderTestJSON(t *testing.T) {
	server := ollamaServer(t, []string{"completion", "tools"}, 8192, []string{"qwen3-coder:latest"})
	ollamaFixture(t, server.URL)

	stdout, _, code := run(t, "provider", "test", "local-ollama", "--output", "json")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("output is not valid JSON: %v (%s)", err, stdout)
	}
	if report["errors"] != float64(0) {
		t.Fatalf("errors = %v", report["errors"])
	}
	serverReport, ok := report["server"].(map[string]any)
	if !ok || serverReport["reachable"] != true {
		t.Fatalf("server = %v", report["server"])
	}
	models, ok := report["models"].([]any)
	if !ok || len(models) != 1 {
		t.Fatalf("models = %v", report["models"])
	}
	first := models[0].(map[string]any)
	if first["alias"] != "local-coder" || first["ok"] != true {
		t.Fatalf("models[0] = %v", first)
	}
}

func TestProviderTestReportsAMissingModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/version":
			_, _ = io.WriteString(w, `{"version":"0.12.3"}`)
		case "/api/tags":
			_, _ = io.WriteString(w, `{"models":[]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"model 'qwen3-coder' not found"}`)
		}
	}))
	t.Cleanup(server.Close)
	ollamaFixture(t, server.URL)

	stdout, _, code := run(t, "provider", "test", "local-ollama")
	if code != apperrors.ExitCapability {
		t.Fatalf("exit code = %d, want %d\n%s", code, apperrors.ExitCapability, stdout)
	}
	if !strings.Contains(stdout, "check(s) failed") {
		t.Fatalf("stdout = %q", stdout)
	}
	if !strings.Contains(stdout, "ollama pull qwen3-coder") {
		t.Fatalf("stdout = %q, want an actionable hint", stdout)
	}
}

func TestProviderTestReportsMissingToolSupport(t *testing.T) {
	// The agent grants tools, so a model without the tools capability must be
	// reported instead of silently degrading.
	server := ollamaServer(t, []string{"completion"}, 8192, []string{"qwen3-coder:latest"})
	ollamaFixture(t, server.URL)

	stdout, _, code := run(t, "provider", "test", "local-ollama")
	if code != apperrors.ExitCapability {
		t.Fatalf("exit code = %d, want %d", code, apperrors.ExitCapability)
	}
	for _, want := range []string{"✗", "local-coder → qwen3-coder", "tool_calling"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("report is missing %q:\n%s", want, stdout)
		}
	}
}

func TestProviderTestReportsAnUnreachableServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := server.URL
	server.Close()
	ollamaFixture(t, endpoint)

	stdout, _, code := run(t, "provider", "test", "local-ollama")
	if code != apperrors.ExitCapability {
		t.Fatalf("exit code = %d, want %d", code, apperrors.ExitCapability)
	}
	if !strings.Contains(stdout, "not reachable") {
		t.Fatalf("stdout = %q", stdout)
	}
	if !strings.Contains(stdout, "ollama serve") {
		t.Fatalf("stdout = %q, want an actionable hint", stdout)
	}
}

func TestProviderTestUnknownProvider(t *testing.T) {
	server := ollamaServer(t, []string{"completion"}, 8192, nil)
	ollamaFixture(t, server.URL)

	_, stderr, code := run(t, "provider", "test", "absent")
	if code != apperrors.ExitNotFound {
		t.Fatalf("exit code = %d, want %d", code, apperrors.ExitNotFound)
	}
	if !strings.Contains(stderr, `provider "absent" is not defined`) {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestProviderTestRejectsUnknownOutput(t *testing.T) {
	server := ollamaServer(t, []string{"completion"}, 8192, nil)
	ollamaFixture(t, server.URL)

	if _, _, code := run(t, "provider", "test", "local-ollama", "-o", "yaml"); code != apperrors.ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, apperrors.ExitUsage)
	}
}

func TestProviderTestRequiresExactlyOneArgument(t *testing.T) {
	server := ollamaServer(t, []string{"completion"}, 8192, nil)
	ollamaFixture(t, server.URL)

	if _, _, code := run(t, "provider", "test"); code == apperrors.ExitOK {
		t.Fatal("a missing provider name must fail")
	}
}
