---
name: pawagents
description: Delegate one bounded task to an independent read-only subagent that runs on another model, another provider, or a local model, and get back structured, evidence-backed findings. Use for an independent code review, debugging help, architecture analysis, security analysis, performance analysis, a second-model opinion, or cross-model verification of your own conclusions.
metadata:
  short-description: Delegate a task to an independent subagent
---

# PawAgents

PawAgents runs one bounded task in an independent, read-only subagent that uses
a different model or provider than you, and returns a structured result: status,
summary, findings with file and line references, evidence, confidence, usage and
a session id.

It is a second point of view, not a second pair of hands: it reads a workspace
and reports, and it cannot write files, run commands or fix anything.

## When to use it

Use PawAgents when the user asks for, or the task genuinely benefits from, one
of these:

- **independent code review** — a review that is not the author's own opinion;
- **debugging** — a second model reasoning about a failure you cannot explain;
- **architecture analysis** — how a subsystem fits together, or what an
  alternative design would cost;
- **security analysis** — a pass over injection, path escape, secret handling
  and access-control mistakes;
- **performance analysis** — hot paths, allocation and concurrency questions;
- **second-model opinion** — the same question answered by a different model;
- **cross-model verification** — checking your own findings, especially before
  changing critical code.

Do not delegate trivial work. Reading one file, renaming a symbol or answering
from the conversation adds latency and cost without adding a second point of
view.

## How to delegate

1. `pawagents.list_agents` — see the configured agents. Choose by purpose: the
   description says what an agent is for. `ready` tells you whether the runtime
   expects it to run, and `confirmed` whether that verdict rests on evidence; a
   `ready` that is not `confirmed` means the capabilities are unknown until the
   endpoint is asked, not that the agent is unsuitable.
2. `pawagents.get_agent` — the detail, when you need it before spending a
   delegation: model targets, budget, permissions, missing capabilities.
3. `pawagents.delegate_task` — one bounded task, as a contract rather than a
   prompt:
   - `agent` — a name from `list_agents`;
   - `task` — one bounded question, stated as an instruction;
   - `workspace` — the directory the agent may read; state it explicitly;
   - `background` — what the agent cannot discover by reading files, such as the
     intent of a change or the symptom the user reported;
   - `files` — the files worth starting from;
   - `constraints` — rules the answer must respect, for example "only report
     evidence-backed findings" or "cite path:line";
   - `output_mode` — `text` or `structured`; omit it to use the agent default;
   - `budget` — `max_rounds` and `timeout_sec`, which narrow the agent budget
     for this delegation only.

## After the result

> PawAgents result is advisory.
>
> Verify critical findings before modifying files.

Every finding carries the file, the line, the evidence the agent read and a
confidence score. Before acting on one:

- open the cited location and confirm it is real in the current tree;
- discard or downgrade claims the evidence does not support;
- say which findings you verified and which you did not.

A failed delegation is still informative: the status and `error_kind` separate a
budget or timeout from a provider, permission or workspace problem, and the
envelope keeps the partial answer and the session id. Quote the session id in
your report so the transcript can be re-read with `pagent session show`.

## Examples

| The user says | Do this |
| --- | --- |
| "Let another model review my current changes." | `delegate_task` with the review agent, the changed files, and a constraint to cite `path:line`. |
| "让另一个模型分析一下这个 bug。" | `delegate_task` with the debugging agent, the failing file in `files`, and the observed symptom in `background`. |
| "Is this design going to hold up?" | `delegate_task` with the architecture agent and the packages involved. |
| "Are we sure this is safe to expose?" | `delegate_task` with the security agent and the request-handling path. |
| "Double-check what you just concluded." | `delegate_task` with the same question and constraints that state what to verify. |
