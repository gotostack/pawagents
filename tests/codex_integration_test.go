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

// Package tests holds cross-cutting tests: the host integrations, which exist
// as files rather than as Go code, and the end to end runs that need a real
// binary.
package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// codexRoot is the Codex integration directory, relative to this package.
func codexRoot() string { return filepath.Join("..", "integrations", "codex") }

// readFile reads a file of the integration and fails the test when it is
// missing, so that a moved or renamed file is reported as the breakage it is.
func readFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	return data
}

// TestCodexPluginManifest checks the plugin manifest Codex reads.
//
// The manifest is a contract with a host: a field Codex does not know about, a
// path that does not exist or a missing description each break the integration
// in a way that only shows up inside Codex, which is exactly why it is checked
// here.
func TestCodexPluginManifest(t *testing.T) {
	root := codexRoot()
	path := filepath.Join(root, ".codex-plugin", "plugin.json")

	var manifest struct {
		Name        string `json:"name"`
		Version     string `json:"version"`
		Description string `json:"description"`
		Author      struct {
			Name string `json:"name"`
		} `json:"author"`
		License    string   `json:"license"`
		Keywords   []string `json:"keywords"`
		MCPServers string   `json:"mcpServers"`
		Skills     string   `json:"skills"`
		Interface  struct {
			DisplayName      string   `json:"displayName"`
			ShortDescription string   `json:"shortDescription"`
			LongDescription  string   `json:"longDescription"`
			DeveloperName    string   `json:"developerName"`
			Category         string   `json:"category"`
			Capabilities     []string `json:"capabilities"`
			WebsiteURL       string   `json:"websiteURL"`
			DefaultPrompt    []string `json:"defaultPrompt"`
		} `json:"interface"`
	}
	if err := json.Unmarshal(readFile(t, path), &manifest); err != nil {
		t.Fatalf("%s is not valid JSON: %v", path, err)
	}

	if manifest.Name != "pawagents" {
		t.Fatalf("name = %q, want pawagents: the plugin name is the namespace a "+
			"host exposes the tools under", manifest.Name)
	}
	if manifest.Version == "" || manifest.Description == "" {
		t.Fatalf("the manifest is missing its version or description: %+v", manifest)
	}
	if manifest.Author.Name == "" || manifest.License != "Apache-2.0" {
		t.Fatalf("unexpected author or license: %+v", manifest)
	}
	if len(manifest.Keywords) == 0 {
		t.Fatal("the manifest has no keywords")
	}

	// The two references have to resolve, and both are relative to the plugin
	// root and prefixed with ./ as the host requires.
	for name, reference := range map[string]string{
		"mcpServers": manifest.MCPServers,
		"skills":     manifest.Skills,
	} {
		if !strings.HasPrefix(reference, "./") {
			t.Fatalf("%s = %q, want a ./ prefixed path relative to the plugin root", name, reference)
		}
		if _, err := os.Stat(filepath.Join(root, strings.TrimPrefix(reference, "./"))); err != nil {
			t.Fatalf("%s points at a path that does not exist: %v", name, err)
		}
	}

	// The interface block is what a user sees in the plugin directory.
	ui := manifest.Interface
	if ui.DisplayName == "" || ui.ShortDescription == "" || ui.LongDescription == "" {
		t.Fatalf("the interface block is incomplete: %+v", ui)
	}
	if ui.DeveloperName == "" || ui.Category == "" || ui.WebsiteURL == "" {
		t.Fatalf("the interface block is incomplete: %+v", ui)
	}
	if len(ui.DefaultPrompt) == 0 {
		t.Fatal("the interface block offers no default prompt")
	}
	// PawAgents is read-only, so the plugin must not claim a write capability.
	for _, capability := range ui.Capabilities {
		if capability != "Read" {
			t.Fatalf("capability %q is not read-only; PawAgents never writes", capability)
		}
	}
	if len(ui.Capabilities) == 0 {
		t.Fatal("the plugin claims no capability")
	}
}

// TestCodexMCPServerConfiguration checks the bundled MCP server.
func TestCodexMCPServerConfiguration(t *testing.T) {
	path := filepath.Join(codexRoot(), ".mcp.json")

	var document struct {
		MCPServers map[string]struct {
			Command        string   `json:"command"`
			Args           []string `json:"args"`
			ToolTimeoutSec int      `json:"tool_timeout_sec"`
			Env            any      `json:"env"`
			EnvVars        any      `json:"env_vars"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(readFile(t, path), &document); err != nil {
		t.Fatalf("%s is not valid JSON: %v", path, err)
	}

	server, ok := document.MCPServers["pawagents"]
	if !ok {
		t.Fatalf("the document does not define the pawagents server: %+v", document.MCPServers)
	}
	if server.Command != "pagent" {
		t.Fatalf("command = %q, want pagent", server.Command)
	}
	if got := strings.Join(server.Args, " "); got != "mcp serve --stdio" {
		t.Fatalf("args = %q, want \"mcp serve --stdio\"", got)
	}

	// A delegation is a whole agent run, and a host that keeps its default tool
	// timeout would cut one off. The bundled configuration has to say so.
	if server.ToolTimeoutSec < 600 {
		t.Fatalf("tool_timeout_sec = %d, which is too short for a delegation",
			server.ToolTimeoutSec)
	}

	// The child process must inherit the normal environment: forwarding a named
	// subset would hide the credentials a provider needs.
	if server.Env != nil || server.EnvVars != nil {
		t.Fatal("the bundled server restricts the environment, which can hide " +
			"the credentials a provider needs")
	}
}

// TestCodexSkill checks the skill that decides when Codex delegates.
func TestCodexSkill(t *testing.T) {
	path := filepath.Join(codexRoot(), "skills", "pawagents", "SKILL.md")
	content := string(readFile(t, path))

	frontmatter, body, ok := strings.Cut(strings.TrimPrefix(content, "---\n"), "\n---\n")
	if !ok {
		t.Fatalf("%s does not start with a frontmatter block", path)
	}
	if !strings.Contains(frontmatter, "name: pawagents") {
		t.Fatalf("the skill frontmatter does not name the skill:\n%s", frontmatter)
	}
	if !strings.Contains(frontmatter, "description: ") {
		t.Fatal("the skill frontmatter has no description, so the host cannot " +
			"decide when to load it")
	}

	// The description is what a host routes on, so it has to name the situations
	// the skill exists for.
	for _, want := range []string{
		"code review", "debugging", "architecture", "security",
		"performance", "second-model", "cross-model verification",
	} {
		if !strings.Contains(frontmatter, want) {
			t.Fatalf("the description does not mention %q:\n%s", want, frontmatter)
		}
	}

	// The advisory rule is a requirement of the development document, not a
	// suggestion: a host that forwards a finding as a fact is the failure mode
	// this exists to prevent.
	for _, want := range []string{
		"PawAgents result is advisory.",
		"Verify critical findings before modifying files.",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("the skill does not state %q", want)
		}
	}

	// The skill has to name the contract and the tools it tells the host to
	// call, or it describes a capability the host cannot use.
	for _, want := range []string{
		"pawagents.list_agents", "pawagents.get_agent", "pawagents.delegate_task",
		"agent", "task", "workspace", "background", "files", "constraints",
		"output_mode", "budget",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("the skill does not mention %q", want)
		}
	}
}

// TestCodexIntegrationReadme checks that the instructions a user follows cover
// the steps the integration needs.
func TestCodexIntegrationReadme(t *testing.T) {
	content := string(readFile(t, filepath.Join(codexRoot(), "README.md")))

	for _, want := range []string{
		"go build -o pagent ./cmd/pagent",
		"codex mcp add pawagents -- pagent mcp serve --stdio",
		"codex mcp list",
		"tool_timeout_sec",
		"pagent session show",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("the integration README does not mention %q", want)
		}
	}
}
