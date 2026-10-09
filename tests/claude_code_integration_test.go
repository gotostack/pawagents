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

package tests

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawagents/pawagents/internal/mcpserver"
)

// claudeRoot is the Claude Code integration directory, relative to this package.
func claudeRoot() string { return filepath.Join("..", "integrations", "claude-code") }

// claudeSkillPath is the skill as a project would install it.
func claudeSkillPath() string {
	return filepath.Join(claudeRoot(), ".claude", "skills", "pawagents", "SKILL.md")
}

// frontmatter splits a SKILL.md into its YAML frontmatter fields and its body.
//
// The fields are read with a minimal parser rather than a YAML library: the
// frontmatter of a skill is a flat map of scalars, and keeping the test free of
// a dependency means it validates what a host reads, not what a parser accepts.
func frontmatter(t *testing.T, path string) (map[string]string, string) {
	t.Helper()

	content := string(readFile(t, path))
	if !strings.HasPrefix(content, "---\n") {
		t.Fatalf("%s does not open with a frontmatter block: a host reads the "+
			"frontmatter only when the first line is ---", path)
	}

	block, body, ok := strings.Cut(strings.TrimPrefix(content, "---\n"), "\n---\n")
	if !ok {
		t.Fatalf("%s has no closing frontmatter marker", path)
	}

	fields := map[string]string{}
	for _, line := range strings.Split(block, "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, " ") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("%s: cannot read the frontmatter line %q", path, line)
		}
		fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return fields, body
}

// TestClaudeCodeMCPConfiguration checks the project-scoped MCP configuration.
func TestClaudeCodeMCPConfiguration(t *testing.T) {
	path := filepath.Join(claudeRoot(), ".mcp.json.example")

	var document struct {
		MCPServers map[string]struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
			Timeout int      `json:"timeout"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(readFile(t, path), &document); err != nil {
		t.Fatalf("%s is not valid JSON: %v", path, err)
	}

	server, ok := document.MCPServers["pawagents"]
	if !ok {
		t.Fatalf("the example does not define the pawagents server: %+v", document.MCPServers)
	}
	// The name is the namespace the tools are called by, and the skill names
	// them, so it is part of the contract rather than a cosmetic choice.
	if server.Type != "stdio" {
		t.Fatalf("type = %q, want stdio", server.Type)
	}
	if server.Command != "pagent" {
		t.Fatalf("command = %q, want pagent", server.Command)
	}
	if got := strings.Join(server.Args, " "); got != "mcp serve --stdio" {
		t.Fatalf("args = %q, want \"mcp serve --stdio\"", got)
	}

	// The field is milliseconds, and a delegation is a whole agent run.
	if server.Timeout < 600_000 {
		t.Fatalf("timeout = %d ms, which is too short for a delegation", server.Timeout)
	}
}

// TestClaudeCodeSkill checks the skill that decides when Claude Code delegates.
func TestClaudeCodeSkill(t *testing.T) {
	path := claudeSkillPath()
	fields, body := frontmatter(t, path)

	// Only the fields of the Agent Skills specification are allowed, so the
	// skill keeps working when it is uploaded to claude.ai or packaged.
	allowed := map[string]bool{
		"name": true, "description": true, "license": true,
		"compatibility": true, "metadata": true, "allowed-tools": true,
	}
	for key := range fields {
		if !allowed[key] {
			t.Fatalf("frontmatter field %q is a Claude Code extension; keep the "+
				"skill portable by using spec fields only", key)
		}
	}

	if fields["name"] != "pawagents" {
		t.Fatalf("name = %q, want pawagents, which is the /pawagents command and "+
			"the server namespace", fields["name"])
	}
	// A host lists the description to decide whether to load the skill, and
	// truncates the listing entry.
	if len(fields["description"]) > 1536 {
		t.Fatalf("the description is %d characters; a host truncates the listing "+
			"entry at 1536", len(fields["description"]))
	}
	for _, want := range []string{
		"another model", "another provider", "independent", "review",
	} {
		if !strings.Contains(fields["description"], want) {
			t.Fatalf("the description does not mention %q:\n%s", want, fields["description"])
		}
	}

	// The pre-approved tools have to be the real tool names of the server: a
	// rule with a typo silently does nothing and the user gets a prompt.
	granted := strings.Fields(fields["allowed-tools"])
	if len(granted) == 0 {
		t.Fatal("the skill pre-approves no tool, so every delegation prompts")
	}
	known := map[string]bool{}
	for _, tool := range mcpserver.Tools() {
		known["mcp__pawagents__"+tool] = true
	}
	for _, tool := range granted {
		if !known[tool] {
			t.Fatalf("allowed-tools names %q, which is not a tool of the pawagents "+
				"server (%v)", tool, mcpserver.Tools())
		}
	}

	// The four instructions the development document requires, and the advisory
	// rule: a host that forwards a finding as a fact is the failure this
	// prevents.
	for _, want := range []string{
		"independent",
		"Do not delegate trivial operations.",
		"Give the external agent a bounded task.",
		"Verify important findings yourself",
		"PawAgents result is advisory.",
		"Verify critical findings before modifying files.",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("the skill body does not state %q", want)
		}
	}

	// The skill has to name the contract it tells the host to use.
	for _, want := range []string{
		"mcp__pawagents__list_agents", "mcp__pawagents__delegate_task",
		"agent", "task", "workspace", "background", "files", "constraints",
		"output_mode", "budget",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("the skill does not mention %q", want)
		}
	}

	// The document asks for a workflow a user can trigger by name.
	if !strings.Contains(body, "让另一个模型分析一下这个 bug。") {
		t.Fatal("the skill does not carry the example that maps a user request to a delegation")
	}
}

// TestClaudeCodeProjectMemory checks the CLAUDE.md snippet.
func TestClaudeCodeProjectMemory(t *testing.T) {
	content := string(readFile(t, filepath.Join(claudeRoot(), "CLAUDE.md.example")))

	for _, want := range []string{
		"PawAgents", "read-only", "advisory", "Verify critical findings",
		"bounded", "skill",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("the CLAUDE.md example does not mention %q", want)
		}
	}
}

// TestClaudeCodeIntegrationReadme checks the instructions a user follows.
func TestClaudeCodeIntegrationReadme(t *testing.T) {
	content := string(readFile(t, filepath.Join(claudeRoot(), "README.md")))

	for _, want := range []string{
		"claude mcp add pawagents -- pagent mcp serve --stdio",
		"claude mcp list",
		".mcp.json.example",
		"~/.claude/skills",
		".claude/skills",
		"timeout",
		"pagent session show",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("the integration README does not mention %q", want)
		}
	}
}

// TestClaudeSkillAndCodexSkillAgree checks that both hosts are told the same
// thing, which is the point of one runtime behind one contract.
func TestClaudeSkillAndCodexSkillAgree(t *testing.T) {
	claudeFields, claudeBody := frontmatter(t, claudeSkillPath())
	codexFields, codexBody := frontmatter(t, filepath.Join(codexRoot(), "skills", "pawagents", "SKILL.md"))

	for name, pair := range map[string][2]string{
		"name":        {claudeFields["name"], codexFields["name"]},
		"description": {claudeFields["description"], codexFields["description"]},
	} {
		if pair[0] != pair[1] {
			t.Fatalf("the %s differs between the two skills:\nclaude: %s\ncodex:  %s",
				name, pair[0], pair[1])
		}
	}

	// Both bodies must carry the same rules, or the two hosts behave
	// differently for the same request.
	for _, want := range []string{
		"PawAgents result is advisory.",
		"Verify critical findings before modifying files.",
		"Do not delegate trivial work",
		"delegate_task",
	} {
		for _, body := range []string{claudeBody, codexBody} {
			normalized := strings.ReplaceAll(body, "Do not delegate trivial operations.", "Do not delegate trivial work")
			if !strings.Contains(normalized, want) {
				t.Fatalf("one of the skills does not state %q", want)
			}
		}
	}
}
