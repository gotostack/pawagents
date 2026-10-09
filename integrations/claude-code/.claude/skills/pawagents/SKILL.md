---
name: pawagents
description: Delegate one bounded task to an independent read-only subagent that runs on another model, another provider, or a local model, and get back structured, evidence-backed findings. Use for an independent code review, debugging help, architecture analysis, security analysis, performance analysis, a second-model opinion, or cross-model verification of your own conclusions.
license: Apache-2.0
allowed-tools: mcp__pawagents__list_agents mcp__pawagents__get_agent mcp__pawagents__delegate_task
---

# PawAgents

PawAgents runs one bounded task in an independent, read-only subagent on a
different model or provider than you, and returns a structured result: status,
summary, findings with file, line and evidence, confidence, usage and a session
id.

It is a second point of view, not a second pair of hands: it reads a workspace
and reports, and it cannot write files, run commands or fix anything.

## When to use it

- **independent code review** — a review that is not the author's own opinion;
- **debugging** — a second model reasoning about a failure you cannot explain;
- **architecture analysis** — how a subsystem fits together, or what an
  alternative design would cost;
- **security analysis** — injection, path escape, secret handling and
  access-control mistakes;
- **performance analysis** — hot paths, allocation and concurrency questions;
- **second-model opinion** — the same question answered by a different model;
- **cross-model verification** — checking your own findings, especially before
  changing critical code.

Do not delegate trivial operations. Reading one file, renaming a symbol or
answering from the conversation adds latency and cost without adding a second
point of view.

## How to delegate

1. `mcp__pawagents__list_agents` — the configured agents. Choose by purpose:
   the description says what an agent is for. `ready` says whether the runtime
   expects it to run and `confirmed` whether that verdict rests on evidence; a
   `ready` that is not `confirmed` means the capabilities are unknown until the
   endpoint is asked, not that the agent is unsuitable.
2. `mcp__pawagents__delegate_task` — one bounded task:
   - `agent` — a name from `list_agents`, such as the reviewer;
   - `task` — one bounded question, stated as an instruction;
   - `workspace` — the directory the agent may read; state it explicitly;
   - `background` — what the agent cannot discover by reading files;
   - `files` — the files worth starting from;
   - `constraints` — rules the answer must respect, such as "cite path:line";
   - `output_mode`, `budget` — optional; they use the agent configuration.

Give the external agent a bounded task. One question per delegation beats a
paragraph of instructions, and starting a new delegation beats continuing a long
one.

## After the result

> PawAgents result is advisory.
>
> Verify critical findings before modifying files.

Every finding carries the file, the line, the evidence the agent read and a
confidence score. Verify important findings yourself before acting:

- open the cited location and confirm it is real in the current tree;
- discard or downgrade claims the evidence does not support;
- say which findings you verified and which you did not.

A failed delegation is still informative: the status and `error_kind` separate a
budget or timeout from a provider, permission or workspace problem, and the
envelope keeps the partial answer and the session id. Quote the session id, so
the transcript can be re-read with `pagent session show`.

## Examples

```text
Use PawAgents reviewer to independently review my current changes, then verify
its findings before acting on them.

Ask PawAgents to check whether my conclusion about the locking bug is right.

让另一个模型分析一下这个 bug。
```
