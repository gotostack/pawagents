# PawAgents for OpenAI Codex

This directory makes PawAgents available to Codex as an external subagent
runtime: Codex discovers the `pawagents` MCP server, lists the configured agents
and delegates a bounded task to one of them.

```
integrations/codex/
├── .codex-plugin/plugin.json      plugin manifest (Codex compatibility layout)
├── .mcp.json                      the bundled MCP server
├── skills/pawagents/SKILL.md      when and how Codex should delegate
└── README.md
```

## 1. Build `pagent`

Codex starts the server as a local process, so `pagent` has to be on `PATH`:

```bash
go build -o pagent ./cmd/pagent
sudo install pagent /usr/local/bin/pagent
pagent version
```

## 2. Register the MCP server

```bash
codex mcp add pawagents -- pagent mcp serve --stdio
codex mcp list
```

`codex mcp add` writes an `[mcp_servers.pawagents]` table into
`~/.codex/config.toml`. Codex reads the server instructions and the three tools
from the handshake, so no other configuration is required.

Two settings are worth knowing about:

- **`tool_timeout_sec` defaults to 60 seconds in Codex.** A delegation is a
  whole agent run and usually takes longer than that. Add the setting to
  `~/.codex/config.toml` and raise it to at least the `timeout_sec` of the
  agents you delegate to:

  ```toml
  [mcp_servers.pawagents]
  command = "pagent"
  args = ["mcp", "serve", "--stdio"]
  tool_timeout_sec = 1800
  ```

- **Approvals.** Every PawAgents tool is read-only and advertises that in its
  annotations, so Codex does not prompt for a write. If you set
  `default_tools_approval_mode = "writes"` or `"prompt"`, the read-only hint is
  what keeps a delegation from asking for permission it does not need.

## 3. Install the skill

The skill tells Codex *when* delegating is worth it and *how* to read the
result. Install it as a personal skill:

```bash
mkdir -p ~/.codex/skills
cp -R skills/pawagents ~/.codex/skills/pawagents
```

Restart Codex, then ask for something that benefits from a second model, for
example:

```text
Ask PawAgents to independently review my current changes, then verify its
findings before you act on them.
```

## 4. Use it as a plugin (optional)

A plugin bundles the MCP server and the skill together and lets them be
installed from a marketplace. The layout here already is the Codex plugin
layout, so a personal marketplace can point at it directly:

```bash
mkdir -p ~/.agents/plugins ~/.codex/plugins
cp -R integrations/codex ~/.codex/plugins/pawagents
```

`~/.agents/plugins/marketplace.json`:

```json
{
  "name": "pawagents-local",
  "interface": {
    "displayName": "PawAgents (local)"
  },
  "plugins": [
    {
      "name": "pawagents",
      "source": {
        "source": "local",
        "path": "./.codex/plugins/pawagents"
      },
      "policy": {
        "installation": "AVAILABLE",
        "authentication": "ON_INSTALL"
      },
      "category": "Developer Tools"
    }
  ]
}
```

Then install it from the Plugins Directory, or from the CLI:

```bash
codex plugin marketplace add ~/.agents/plugins
codex plugin marketplace list
```

The bundled server in `.mcp.json` is started for you once the plugin is
enabled, so step 2 is not needed in this route.

## 5. Verify the integration

Start with the checks that do not involve a model:

```bash
pagent config validate          # 0 errors, or a warning you understand
pagent agent list               # the agents Codex will see
codex mcp list                  # pawagents is registered
```

Then ask Codex for a review of a small change and confirm:

- Codex calls `pawagents.list_agents` before delegating;
- the delegation returns `status: completed` with findings that cite
  `path:line`;
- the answer names the provider and model that produced it, and a session id.

Finally, re-read the delegation on disk:

```bash
pagent session list
pagent session show <session-id> --messages
```

If a delegation fails, the message starts with the classified error kind, which
tells you where to look: `NotFoundError` means the agent name is not in the
configuration, `ConfigurationError` or `ProviderError` means the model target is
not usable, `WorkspaceError` means the directory the host asked for is not
readable, and `BudgetExceededError` or `TimeoutError` means the agent ran into
the limits of its own budget.

## Notes

- PawAgents never writes to the workspace and never runs a shell command, so
  Codex remains the only actor that changes files.
- The subagent sees one workspace. State it in `workspace` when the repository
  is not the directory Codex was started in.
- Credentials are read from the environment at request time. If your Codex
  configuration starts MCP servers with a restricted environment, forward the
  variables your configuration needs, such as `PAWAGENTS_HOME`,
  `PAWAGENTS_CONFIG` and the provider API key variables.
