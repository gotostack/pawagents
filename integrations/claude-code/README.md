# PawAgents for Claude Code

This directory makes PawAgents available to Claude Code as an external subagent
runtime: Claude Code discovers the `pawagents` MCP server, lists the configured
agents, and delegates a bounded task to one of them.

```
integrations/claude-code/
├── .mcp.json.example              project-scoped MCP configuration
├── CLAUDE.md.example              project memory snippet
├── .claude/skills/pawagents/      the skill, for the project or for you
│   └── SKILL.md
└── README.md
```

## 1. Build `pagent`

Claude Code starts the server as a local process, so `pagent` has to be on
`PATH`:

```bash
go build -o pagent ./cmd/pagent
sudo install pagent /usr/local/bin/pagent
pagent version
```

## 2. Connect the MCP server

For yourself, in every project (user scope, stored in `~/.claude.json`):

```bash
claude mcp add pawagents -- pagent mcp serve --stdio
claude mcp list
```

For one project, shared with everyone who works in it, copy the example to the
project root:

```bash
cp integrations/claude-code/.mcp.json.example /path/to/project/.mcp.json
```

```json
{
  "mcpServers": {
    "pawagents": {
      "type": "stdio",
      "command": "pagent",
      "args": ["mcp", "serve", "--stdio"],
      "timeout": 900000
    }
  }
}
```

Claude Code asks for approval before it connects a server from a project
`.mcp.json`, unless the run is non-interactive (`claude -p`), where it loads the
file without asking. Run `claude mcp reset-project-choices` to review the
approvals again.

`timeout` is per tool call, in milliseconds. A delegation is a whole agent run,
so the example allows fifteen minutes; raise it if you delegate to agents whose
`timeout_sec` is larger. Without the field, a stdio server falls back to
`MCP_TOOL_TIMEOUT`, and an idle window of thirty minutes applies either way.

Both routes name the server `pawagents`, so the tools are callable as
`mcp__pawagents__list_agents`, `mcp__pawagents__get_agent` and
`mcp__pawagents__delegate_task`.

## 3. Install the skill

The skill tells Claude Code when delegating is worth it and how to read the
result. Either copy it into your personal skills directory:

```bash
mkdir -p ~/.claude/skills
cp -R integrations/claude-code/.claude/skills/pawagents ~/.claude/skills/pawagents
```

or commit it to the project, which shares it with the team:

```bash
mkdir -p .claude/skills
cp -R integrations/claude-code/.claude/skills/pawagents .claude/skills/pawagents
```

Claude Code picks up a skill in a watched directory during a running session,
and offers it as `/pawagents` in the `/` menu. To add the project memory
snippet as well, append `CLAUDE.md.example` to your `CLAUDE.md`.

## 4. Use it

```text
Use PawAgents reviewer to independently review my current changes, then verify
its findings before acting on them.
```

```text
/pawagents
```

```text
让另一个模型分析一下这个 bug。
```

An answer names the provider and model that produced it, and a session id. Treat
the findings as evidence: open the cited file and line before changing anything.

## 5. Verify the integration

Start with the checks that do not involve a model:

```bash
pagent config validate          # 0 errors, or a warning you understand
pagent agent list               # the agents Claude Code will see
claude mcp list                 # pawagents is ✔ Connected
claude mcp get pawagents        # the command Claude Code runs
```

Then, in a session, ask for a review of a small change and confirm that Claude
Code calls `list_agents` first, delegates once, and quotes the session id. The
delegation itself is on disk:

```bash
pagent session list
pagent session show <session-id> --messages
```

A non-interactive run is enough to prove the wiring:

```bash
claude -p "Use the pawagents MCP tool list_agents and tell me which agents exist." \
  --mcp-config /path/to/project/.mcp.json --strict-mcp-config \
  --allowedTools mcp__pawagents__list_agents
```

If a delegation fails, the message starts with the classified error kind:
`NotFoundError` means the agent name is not in the configuration,
`ProviderError` or `ConfigurationError` means the model target is not usable,
`WorkspaceError` means the directory is not readable, and `BudgetExceededError`
or `TimeoutError` means the agent hit the limits of its own budget.

## Notes

- PawAgents never writes to the workspace and never runs a shell command, so
  Claude Code stays the only actor that changes files.
- The subagent sees one workspace. State it in `workspace` when the repository is
  not the directory the session started in.
- Credentials are read from the environment at request time. Claude Code passes
  the normal environment to a stdio server, so a provider that reads an API key
  variable works without extra configuration.
- The skill pre-approves the three read-only tools with `allowed-tools`, so an
  invocation does not prompt for a read-only delegation. If you install the
  server through a plugin instead, the tool names gain a plugin prefix and the
  approval falls back to your normal permission rules.
