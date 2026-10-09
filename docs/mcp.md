# MCP integration

PawAgents is an MCP server. A coding agent connects to it, lists the configured
agents and delegates one bounded task. The contract is identical for every host:
that is the point of the design, and it is why host-specific code lives in
`integrations/` rather than in the runtime.

## Serving

```bash
pagent mcp serve --stdio
```

Standard output carries the protocol, so diagnostics go to standard error. The
transport is a flag rather than a separate command, so adding an HTTP transport
later changes nothing a host sees.

## The tools

| Tool | Input | Output |
| --- | --- | --- |
| `list_agents` | — | `{"agents": [...]}` with name, description, model alias, tools, output mode, `ready` and `confirmed`. |
| `get_agent` | `{"name": "..."}` | The full profile: targets, budget, permissions, prompt source, missing capabilities. |
| `delegate_task` | The delegation contract | The structured result envelope, plus `error_kind` when the task did not complete. |

Every tool is annotated read-only, which is what lets a host call one without
asking its user for permission.

## The delegation contract

```json
{
  "agent": "reviewer",
  "task": "Review the current changes for concurrency problems.",
  "workspace": "/home/user/openvswitch",
  "background": "The patch changes PMD scheduling behavior.",
  "files": ["lib/dpif-netdev.c"],
  "constraints": ["Only report evidence-backed findings"],
  "output_mode": "structured",
  "budget": { "max_rounds": 20, "timeout_sec": 300 }
}
```

It is deliberately not a single prompt. A delegation needs a question, a place to
look, the context the agent cannot discover, the rules the answer must respect
and a budget; a host that can only send a prompt has no way to state any of that,
and the agent then guesses. The budget may narrow the agent's own limits and can
never widen them, and the tool grant is not part of the contract at all: a host
cannot give a subagent more access than its configuration allows.

## Reading a result

```json
{
  "status": "completed",
  "agent": "reviewer",
  "provider": "local-ollama",
  "model": "qwen3.8:27b-mlx",
  "summary": "...",
  "findings": [{ "severity": "high", "file": "lib/dpif-netdev.c", "line": 3184, "evidence": "...", "confidence": 0.91 }],
  "usage": { "input_tokens": 14325, "output_tokens": 3120, "tool_calls": 17, "rounds": 8 },
  "session_id": "agt_5ccd1911c434718d"
}
```

Three outcomes, reported differently on purpose:

| Outcome | Call | Payload |
| --- | --- | --- |
| The task completed | succeeds | The envelope. |
| The task ran and failed (budget, timeout, provider) | fails | The envelope, still carrying the partial answer and the session id. |
| The task never started (unknown agent, unusable workspace) | fails | No envelope; the message starts with the classified error kind. |

The `session_id` is what makes a finding verifiable after the fact:
`pagent session show <id> --messages` replays what the agent read.

## Readiness

`list_agents` and `get_agent` report two booleans rather than one verdict:

- `ready` — the runtime expects the agent to run;
- `confirmed` — that answer rests on evidence: the endpoint was probed, or the
  provider contract documents the capability.

`ready: false, confirmed: false` means the capabilities are **unknown**, not
missing. An Ollama server hosts models with and without tool calling, so only the
endpoint can answer, and the runtime asks it when the task starts. A host that
treats the two cases alike refuses to delegate to local models that would have
worked.

## Failures

MCP reports a tool failure as a flag plus text, so the classification travels as
the prefix of the message: `NotFoundError: orchestrator.agent: agent "typo" is
not defined in the configuration`. Invalid arguments are rejected against the
tool schema before the handler runs, which is the cheapest possible refusal.

## Hosts

| Host | Connect | Skill |
| --- | --- | --- |
| Codex | `codex mcp add pawagents -- pagent mcp serve --stdio` | `integrations/codex/` (also a plugin manifest and a bundled MCP server) |
| Claude Code | `claude mcp add pawagents -- pagent mcp serve --stdio` | `integrations/claude-code/` (also a project `.mcp.json` example) |

Both hosts are told the same thing by their skill, including the two rules that
matter most: the result is advisory, and critical findings must be verified
before files are modified.

## Tool timeouts

A delegation is a whole agent run, which outlives the default tool timeout of
both hosts. Set it explicitly:

- Codex: `tool_timeout_sec` in `~/.codex/config.toml`, default 60 seconds.
- Claude Code: `timeout` in the `.mcp.json` entry, in milliseconds.

Set it above the `timeout_sec` of the agents you delegate to, or a task that the
runtime would have completed inside its own budget is cut off by the host.

## Debugging an integration

```bash
pagent doctor                 # configuration, providers, agents, workspace, MCP
codex mcp list                # is the server registered and connected?
claude mcp list               # the same, plus the tools fetch
pagent mcp serve --stdio      # run it by hand and speak JSON-RPC to it
```

`claude mcp list` showing the server as connected is not the whole story: a host
also validates the schemas it is handed, and rejects a tool whose root schema
type is not a plain object. `pagent doctor` and the MCP tests in
`internal/mcpserver` pin that rule.
