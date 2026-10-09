# Agents

An agent is the reusable half of a delegation: a prompt, a model alias, a tool
grant, a budget and permissions. The host supplies the question; the agent
supplies the point of view.

## What an agent is made of

```yaml
agents:
  reviewer:
    description: Independent review of a change        # shown to the host
    model: strong-coder                               # an alias
    prompt: reviewer.md                               # or inline instructions
    tools: [repo.read, repo.search, git.diff]
    output_mode: structured
    max_rounds: 24
    max_tool_calls: 60
    timeout_sec: 300
```

| Part | Why it exists |
| --- | --- |
| `description` | A host picks an agent by purpose. It is the first thing `list_agents` reports. |
| `model` | An alias, so the agent does not move when the model changes. |
| `prompt` | The standing instruction. The task is what varies between delegations. |
| `tools` | The grant. An agent can only call what it lists, and read-only tools are the only ones that exist. |
| `output_mode` | `structured` asks for the finding envelope; `text` returns the answer as written. |
| budget | Rounds, tool calls and wall clock. A delegation can narrow them, never widen them. |
| permissions | `filesystem: read`, `shell: deny`. Writes are rejected by validation. |

## Writing a good prompt

A useful external subagent prompt has four parts, and the built-in prompts under
`prompts/` are organised that way:

1. **Role** — what kind of reviewer this is, in one or two sentences.
2. **Method** — how to gather evidence: read the file, search for the symbol,
   look at the diff, instead of describing what code like this usually does.
3. **Output contract** — the exact envelope, including what an empty finding list
   means.
4. **Restraint** — no invented file names or line numbers, no style padding, no
   claims that were not verified.

The last one is what separates a useful report from a plausible one. A model that
invents a line number costs the host more time than no review at all, so the
prompt asks for evidence and lowers confidence when evidence is missing.

## The built-in prompts

| Prompt | For |
| --- | --- |
| `reviewer.md` | An independent review of a change: correctness, clarity, tests. |
| `debugger.md` | A failure someone cannot explain, with the symptom as background. |
| `architect.md` | How a subsystem fits together, and what an alternative would cost. |
| `security.md` | Injection, path escape, secret handling, access control. |
| `performance.md` | Hot paths, allocation, concurrency. |

An agent with no `prompt` and no `instructions` runs with the built-in prompt for
its name when one exists, and with no guidance otherwise — which `pagent agent
show` reports, because a model answering without instructions is worth knowing
about.

## The structured result

Every agent returns the same envelope, whatever model produced it:

```json
{
  "status": "completed",
  "agent": "reviewer",
  "provider": "anthropic",
  "model": "claude-sonnet-4-5",
  "summary": "one paragraph on what was reviewed and concluded",
  "findings": [
    {
      "severity": "high",
      "category": "concurrency",
      "file": "lib/dpif-netdev.c",
      "line": 3184,
      "title": "short statement of the problem",
      "description": "why it is a problem",
      "evidence": "the code that was actually read",
      "suggestion": "what to change",
      "confidence": 0.91
    }
  ],
  "usage": {"input_tokens": 14325, "output_tokens": 3120, "tool_calls": 17, "rounds": 8},
  "session_id": "agt_5ccd1911c434718d"
}
```

An empty `findings` list is a valid answer: a review that found nothing is a
result, not a failure. `status` says what happened — `completed`, `failed`,
`budget_exceeded`, `timeout`, `cancelled` — and a delegation that ran and failed
still carries its partial answer and its session id.

## Inspecting an agent

```bash
pagent agent list                  # every agent, its model, tools and budget
pagent agent show reviewer         # the resolved profile and a capability check
pagent agent show reviewer --probe # the same, asking the endpoint
```

The capability section answers the question a failed run raises: which
requirement is not met, by which model, and what to change. It reports `ready`
and, separately, whether that verdict is confirmed — an unprobed local model is
unknown, not unsuitable.

## Testing an agent before a host uses it

```bash
pagent run --agent reviewer --task "Review internal/agent/loop.go" --workspace .
pagent session show <session-id> --messages
```

`pagent run` uses the identical runtime the MCP server uses, so a delegation that
works here works from Codex or Claude Code. The session transcript afterwards is
what shows why the model answered the way it did, including any compaction that
happened along the way.
