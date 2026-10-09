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
	"github.com/pawagents/pawagents/internal/doctor"
)

// writeDoctorConfig installs a configuration whose only provider is a local
// endpoint that no test reaches: the checks that need the network are the ones
// this file does not assert on.
func writeDoctorConfig(t *testing.T, contents string) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfigPath, "")

	path := filepath.Join(home, config.ConfigFileName)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("cannot write the test configuration: %v", err)
	}
	return path
}

// healthyConfig needs no credential and no reachable endpoint: the agent has no
// tools, and the provider is one this build implements.
func healthyConfig(t *testing.T) string {
	t.Helper()

	return writeDoctorConfig(t, `version: 1
providers:
  cloud:
    type: anthropic
    api_key: sk-test
models:
  strong-coder:
    provider: cloud
    model: claude-sonnet-4-5
agents:
  reviewer:
    model: strong-coder
    instructions: Review carefully.
`)
}

func TestDoctorReportsAHealthyInstallation(t *testing.T) {
	healthyConfig(t)

	stdout, stderr, code := run(t, "doctor", "--workspace", t.TempDir())
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, want 0; stderr = %s\n%s", code, stderr, stdout)
	}

	for _, want := range []string{
		"PawAgents diagnostics",
		"Config",
		"Providers",
		"Agents",
		"Workspace",
		"MCP",
		"✓ reviewer (strong-coder)",
		"stdio transport available",
		"list_agents, get_agent, delegate_task",
		"passed,",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("the report does not contain %q:\n%s", want, stdout)
		}
	}
	// A healthy run must not mention a failure.
	if strings.Contains(stdout, "✗") {
		t.Fatalf("a healthy installation reports a failure:\n%s", stdout)
	}
}

func TestDoctorReportsAMissingCredential(t *testing.T) {
	// The variable is exported but empty, which is a misconfiguration and must
	// be reported rather than treated as a credential.
	t.Setenv("PAWAGENTS_DOCTOR_KEY", "")
	writeDoctorConfig(t, `version: 1
providers:
  cloud:
    type: anthropic
    api_key_env: PAWAGENTS_DOCTOR_KEY
models:
  strong-coder:
    provider: cloud
    model: claude-sonnet-4-5
agents:
  reviewer:
    model: strong-coder
    instructions: Review carefully.
`)

	stdout, stderr, code := run(t, "doctor", "--workspace", t.TempDir())
	if code != apperrors.ExitAuth {
		t.Fatalf("exit code = %d, want %d\n%s\n%s", code, apperrors.ExitAuth, stdout, stderr)
	}
	for _, want := range []string{
		"✗ cloud (anthropic)",
		"PAWAGENTS_DOCTOR_KEY is not set",
		"✗ reviewer (strong-coder)",
		"no credential for cloud",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("the report does not contain %q:\n%s", want, stdout)
		}
	}
}

func TestDoctorReportsABrokenConfiguration(t *testing.T) {
	path := writeDoctorConfig(t, `version: 1
agents:
  reviewer:
    model: nope
    instructions: Review carefully.
    tools:
      - repo.write
`)

	stdout, stderr, code := run(t, "doctor")
	if code != apperrors.ExitConfig {
		t.Fatalf("exit code = %d, want %d\n%s\n%s", code, apperrors.ExitConfig, stdout, stderr)
	}
	for _, want := range []string{
		path,
		`model "nope" is not defined`,
		"repo.write",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("the report does not contain %q:\n%s", want, stdout)
		}
	}
}

func TestDoctorReportsAnUnusableWorkspace(t *testing.T) {
	healthyConfig(t)
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	stdout, stderr, code := run(t, "doctor", "--workspace", missing)
	if code != apperrors.ExitWorkspace {
		t.Fatalf("exit code = %d, want %d\n%s\n%s", code, apperrors.ExitWorkspace, stdout, stderr)
	}
	if !strings.Contains(stdout, "✗ "+missing) {
		t.Fatalf("the report does not name the unusable workspace:\n%s", stdout)
	}
}

func TestDoctorJSONOutput(t *testing.T) {
	healthyConfig(t)

	stdout, stderr, code := run(t, "doctor", "-o", "json", "--workspace", t.TempDir())
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d, want 0; stderr = %s", code, stderr)
	}

	var report doctor.Report
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("cannot decode the report: %v\n%s", err, stdout)
	}
	if report.Failed() || report.Summary.OK == 0 {
		t.Fatalf("unexpected summary: %+v", report.Summary)
	}

	labels := map[string]bool{}
	for _, section := range report.Sections {
		for _, check := range section.Checks {
			labels[check.Label] = true
		}
	}
	if !labels["stdio transport available"] || !labels["reviewer (strong-coder)"] {
		t.Fatalf("the JSON report is missing checks: %v", labels)
	}
}

func TestDoctorRejectsAnUnknownOutputFormat(t *testing.T) {
	healthyConfig(t)

	_, stderr, code := run(t, "doctor", "--output", "yaml")
	if code != apperrors.ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, apperrors.ExitUsage)
	}
	if !strings.Contains(stderr, "unsupported output format") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestDoctorUsesTheConfiguredWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	writeDoctorConfig(t, fmt.Sprintf(`version: 1
security:
  workspace_root: %s
providers:
  cloud:
    type: anthropic
    api_key: sk-test
models:
  strong-coder:
    provider: cloud
    model: claude-sonnet-4-5
agents:
  reviewer:
    model: strong-coder
    instructions: Review carefully.
`, root))

	stdout, _, code := run(t, "doctor")
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d\n%s", code, stdout)
	}
	if !strings.Contains(stdout, root) {
		t.Fatalf("the report does not use the configured root:\n%s", stdout)
	}
}

func TestDoctorAcceptsAnExplicitWorkspaceFlag(t *testing.T) {
	healthyConfig(t)

	// The flag wins over the configured root, which is what makes the command
	// usable against a directory nobody configured yet.
	explicit := t.TempDir()
	stdout, _, code := run(t, "doctor", "--workspace", explicit)
	if code != apperrors.ExitOK {
		t.Fatalf("exit code = %d\n%s", code, stdout)
	}
	if !strings.Contains(stdout, explicit) {
		t.Fatalf("the report does not use the explicit workspace:\n%s", stdout)
	}
}
