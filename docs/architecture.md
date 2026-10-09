# Architecture

PawAgents is an external subagent runtime. A coding agent such as OpenAI Codex
or Claude Code connects to it over the Model Context Protocol (MCP), picks one
of the configured agents, and delegates a bounded task. PawAgents owns the agent
loop, the tool runtime and the provider layer; the host owns the workspace and
applies whatever the report suggests.

```
        Host (Codex, Claude Code, ...)
                    │  MCP over stdio
                    ▼
        internal/mcpserver    list_agents, get_agent, delegate_task
                    │
                    ▼
        internal/orchestrator profile, routing, budget, delegation, sessions
                    │
        ┌───────────┴────────────┐
        ▼                        ▼
  internal/agent           internal/tools
  loop, context, result    repo.*, git.*  → internal/security
        │
        ▼
  internal/provider   ollama, openaicompat, anthropic, openairesponses
        │
        ▼
  internal/llm   the vendor neutral protocol every layer passes around
```

## The layers

| Package | Responsibility | Must not |
| --- | --- | --- |
| `internal/llm` | Messages, tools, events, usage, capabilities. The only shared protocol. | Import a vendor SDK, or anything but the standard library and `apperrors`. |
| `internal/provider` | One adapter per wire protocol: translate a request out, translate events back, report capabilities. | Retry, decide policy, hold budget state. |
| `internal/agent` | The loop: build the prompt, call the model, run tools, compact, produce the structured result. | Know about files, transports or hosts. |
| `internal/tools` | The read-only tool runtime: registry, argument decoding, execution, truncation. | Assume the workspace is trusted; every path goes through the guard. |
| `internal/security` | The workspace guard, permissions, secret redaction. | Import `internal/config`; it takes plain options. |
| `internal/orchestrator` | Resolve a config into a runnable profile, route the alias, validate capabilities, own the budget and the session. | Contain host-specific code. |
| `internal/mcpserver` | The host-facing contract. | Let a host reach past the delegation contract. |
| `internal/session` | The on-disk record of a task. | Fail a task because a transcript could not be written. |
| `internal/doctor` | Diagnostics: what is wrong with this installation. | Change anything. |

## Why the boundaries are where they are

**One protocol, no vendor types.** A provider converts to `internal/llm` at the
boundary. Nothing above the provider layer ever sees an `openai.ChatCompletion`
or an `anthropic.Message`, which is what makes a second provider cheap and a
provider bug contained.

**Interfaces belong to the consumer.** The agent loop declares the `Model`,
`Recorder` and `Compactor` interfaces it needs; the orchestrator satisfies them
structurally. That keeps the dependency arrows pointing one way and lets the
loop be tested with a scripted model.

**The host is a client, not a participant.** `internal/mcpserver` defines the
only interface it needs from the runtime (`Delegator`), so the host-facing
surface is explicit and the same for every host. Host glue lives in
`integrations/`.

**Failures are classified.** Every failure is an `*apperrors.Error` with a kind.
The kind drives the CLI exit status, the MCP error payload and the logging
level, so a script and a coding agent can both branch on it without parsing
prose.

## The life of one delegation

1. A host calls `delegate_task` with the contract: agent, task, workspace,
   background, files, constraints, output mode and a budget.
2. `internal/mcpserver` validates the contract and maps it onto an
   `orchestrator.Request`.
3. The orchestrator loads the agent profile, resolves the model alias to a
   provider target (primary, then fallbacks), checks the requirements against
   the target's capabilities, and opens the workspace guard.
4. A session starts: metadata is written before the first model call.
5. The agent loop builds the system prompt, runs rounds of model calls and tool
   calls, compacts the conversation when it approaches the window, and stops on
   an answer or on a budget.
6. The result is written, the session is finished, and the envelope travels back
   over MCP: status, agent, provider, model, summary, findings, usage and session
   id.

## Concurrency and limits

A single process serves one MCP session on stdio; a delegation runs one agent
loop with one model call in flight at a time. Parallelism exists inside the
runtime (concurrent provider probing, concurrent session writes are serialised
by a mutex), not in the shape of a task. Budgets — rounds, tool calls, tokens,
tool output bytes and wall clock — are enforced by the loop, and exceeding one
produces a `BudgetExceededError` with a partial result rather than a crash.

## Extending PawAgents

| To add | Do this |
| --- | --- |
| A provider | Implement four methods in a new package under `internal/provider/`, register it in `init()`, and add it to `internal/provider/all`. |
| A tool | Add a handler in `internal/tools/`, declare its permission requirements, and grant it to an agent in the configuration. It is read-only unless the whole design changes. |
| An agent | Configuration only: a prompt, a model alias, a tool grant and a budget. See [agents.md](agents.md). |
| A host | Add the MCP server to the host's configuration and ship a skill in `integrations/`. Both hosts see the identical contract. |
