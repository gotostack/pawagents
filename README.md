# PawAgents

**A universal external subagent runtime for coding agents.**

PawAgents lets an existing coding agent — OpenAI Codex, Claude Code, and any
other MCP-capable host — delegate a bounded task to an independent subagent that
may run on a completely different model, provider or machine.

```
Codex  ─┐
        ├─ MCP ─→ subagent runtime ─→ LLM Router ─→ provider
Claude ─┘                                  ├── OpenAI
                                           ├── Anthropic
                                           ├── OpenAI-compatible
                                           └── Ollama (local)
```

> PawAgents is **not** another coding agent, and it is **not** a wrapper around
> a model API. It is a runtime that existing coding agents delegate work to.

---

## Status

PawAgents is being built in phases. This section always reflects what the
current tree actually implements; the [Roadmap](#roadmap) lists what is next.

**Implemented today**

| Area | Status |
| --- | --- |
| `pagent` CLI skeleton with classified exit codes | Done |
| Structured logging with secret redaction | Done |
| Configuration schema, defaults, normalization, validation | Done |
| `pagent version`, `pagent config show/validate/init/path` | Done |
| Built-in agent prompts, embedded in the binary | Done |
| Internal LLM protocol (messages, tools, events, capabilities) | Done |
| HTTP transport with TLS, proxy, credential and header handling | Done |
| Provider registry with lazy construction | Done |
| OpenAI-compatible provider (chat completions, streaming, tools) | Done |
| Native Ollama provider with capability detection | Done |
| Native Anthropic Messages provider (tool use, streaming, reasoning) | Done |
| OpenAI Responses provider (schema constrained output, reasoning) | Done |
| `pagent provider list`, `pagent provider show`, `pagent provider test` | Done |
| Workspace guard: traversal, symlink, blocked path and size enforcement | Done |
| Read-only repository tools and git tools | Done |
| Agent loop: prompt building, tool rounds, structured result, budgets | Done |
| Orchestrator: agent profile, model routing, capability validation, grant | Done |
| `pagent run` | Done |
| Agent and model registry commands with offline capability checks | Done |
| Session persistence: `pagent session list`, `pagent session show` | Done |
| Context compaction with a deterministic summary | Done |
| MCP server: `list_agents`, `get_agent`, `delegate_task` over stdio | Done |
| Codex integration: plugin manifest, bundled MCP server, skill | Done |
| Claude Code integration: MCP configuration, project memory, skill | Done |
| `pagent doctor`: offline diagnostics with local model probes | Done |
| Acceptance chains: Codex / Claude Code to MCP to PawAgents to Ollama | Done |

**Not implemented yet** — do not expect these to work:

An HTTP transport for `pagent mcp serve`, `pagent session continue`, and the
`continue_task`, `get_task` and `cancel_task` MCP tools. They are described in
the roadmap below, and each one is documented here as soon as it lands.

---

## Why PawAgents

A single coding agent is a single point of view. When the task matters you want
a second opinion from a different model, a different vendor or an entirely local
model — without teaching your host agent every provider API in existence.

PawAgents solves three problems:

1. **Host independence.** Codex, Claude Code and future hosts all speak one MCP
   contract. PawAgents never contains host-specific logic in its core.
2. **Provider independence.** A model alias ("strong-coder", "local-coder") is
   resolved at run time. Switching from a cloud model to a local Ollama model is
   a one-line configuration change; the agent definition does not move.
3. **Safe delegation.** Subagents are read-only by default. They inspect a
   workspace with repository and git tools and return evidence-backed findings.
   The host agent remains responsible for applying any change.

---

## How it works

```
┌──────────────────────────────────────────────────────────────┐
│                            HOSTS                             │
│            OpenAI Codex              Claude Code             │
└──────────────┬────────────────────────────┬──────────────────┘
               │                            │
               └────────── MCP ─────────────┘
                            │
                            ▼
┌──────────────────────────────────────────────────────────────┐
│                    PawAgents MCP Server                      │
│   list_agents · get_agent · delegate_task · …                │
└───────────────────────────┬──────────────────────────────────┘
                            ▼
┌──────────────────────────────────────────────────────────────┐
│                      Agent Orchestrator                      │
│   profile · prompt builder · context manager · agent loop    │
│   budget manager · permission manager · session manager      │
└───────────────┬────────────────────────────┬─────────────────┘
                ▼                            ▼
        ┌───────────────┐            ┌──────────────────┐
        │  LLM Router   │            │   Tool Runtime   │
        │ model alias   │            │ repo.read/write  │
        │ capabilities  │            │ repo.search/list │
        └───────┬───────┘            │ git.diff/show/…  │
                ▼                    └──────────────────┘
        ┌───────────────┐
        │  Providers    │
        │ OpenAI        │
        │ Anthropic     │
        │ Compatible    │
        │ Ollama        │
        └───────────────┘
```

The six concepts PawAgents keeps strictly separate:

| Concept | Meaning |
| --- | --- |
| **Host** | The coding agent that delegates work (Codex, Claude Code, …). It knows nothing about providers. |
| **Agent** | A named, reusable profile: prompt, tool grant, budget, permissions. |
| **Model** | An alias such as `strong-coder`, bound to a provider target. |
| **Provider** | The wire protocol and endpoint: OpenAI, Anthropic, OpenAI-compatible, Ollama. |
| **Tool** | A capability granted to an agent, e.g. `repo.search` or `git.diff`. |
| **Session** | The persisted record of one delegated task: what was asked, what was read, what it cost and what it answered. |

---

## Installation

Requires Go 1.25 or newer.

```bash
git clone https://github.com/pawagents/pawagents.git
cd pawagents

go build -o pagent ./cmd/pagent
sudo install pagent /usr/local/bin/pagent
```

Verify the installation:

```bash
pagent version
```

The `Makefile` wraps the common tasks:

```bash
make build      # bin/pagent
make test       # go test ./...
make vet        # go vet ./...
make lint       # golangci-lint when installed, otherwise go vet
make install    # go install ./cmd/pagent
```

---

## Quick start

PawAgents reads its configuration from `~/.pawagents/config.yaml`
(override with `--config` or `PAWAGENTS_CONFIG`).

Create a starter configuration and the built-in agent prompts:

```bash
pagent config init
```

Inspect the effective configuration — the file merged with the built-in
defaults, with every path expanded and every credential redacted:

```bash
pagent config show
pagent config show --output json
```

Check it:

```bash
pagent config validate
```

A freshly generated configuration validates cleanly. Missing cloud API key
environment variables are reported as *warnings*, not errors, so a machine that
only runs a local model is never blocked.

---

## Commands

| Command | Purpose |
| --- | --- |
| `pagent version` | Print build metadata. `--output text\|short\|json`. |
| `pagent config show` | Print the effective configuration. `--show-secrets` disables redaction. |
| `pagent config validate` | Validate and report **every** problem, not just the first. |
| `pagent config init` | Write a starter `config.yaml` plus the built-in prompts. |
| `pagent config path` | Print the configuration path in use. |
| `pagent provider list` | List the configured providers and whether this build can use them. |
| `pagent provider show <name>` | Describe one provider, the models bound to it and the agents using those models. |
| `pagent provider test <name>` | Check reachability, model installation and per-model capabilities. Exits 5 when a check fails. |
| `pagent model list` | List the model aliases, their targets and the agents bound to them. |
| `pagent model show <alias>` | Describe one alias target by target. `--probe` asks every endpoint. |
| `pagent agent list` | List the agent profiles, their model, tools, budget and output mode. |
| `pagent agent show <agent>` | Describe one agent and whether its model can run it. `--probe` asks the endpoint. |
| `pagent run` | Run one task with a configured agent and print the structured result. |
| `pagent session list` | List the recorded sessions, newest first. `--output text\|json`. |
| `pagent session show <id>` | Describe one recorded session. `--messages` adds the transcript. |
| `pagent mcp serve --stdio` | Serve `list_agents`, `get_agent` and `delegate_task` over MCP on stdio. |
| `pagent doctor` | Diagnose the installation. `--probe` asks remote endpoints too. |

Global flags:

| Flag | Environment variable | Meaning |
| --- | --- | --- |
| `--config` | `PAWAGENTS_CONFIG` | Configuration file to load. |
| `--log-level` | `PAWAGENTS_LOG_LEVEL` | `debug`, `info`, `warn`, `error`. |
| `--log-format` | `PAWAGENTS_LOG_FORMAT` | `text` or `json`. |
| `--verbose` | — | Shorthand for `--log-level debug`. |

Other environment variables:

| Variable | Effect |
| --- | --- |
| `PAWAGENTS_HOME` | Base directory instead of `~/.pawagents`. |
| `PAWAGENTS_SESSIONS_DIR` | Overrides `sessions.directory`. |
| `PAWAGENTS_TELEMETRY_ENABLED` | Overrides `telemetry.enabled`. |
| `PAWAGENTS_ALLOW_OUTSIDE_WORKSPACE` | Overrides `security.allow_outside_workspace`. |

### Exit codes

Errors are classified, so scripts can branch on the exit status without parsing
messages:

| Code | Meaning | Code | Meaning |
| --- | --- | --- | --- |
| 0 | Success | 8 | Provider error |
| 1 | Unclassified error | 9 | Agent error |
| 2 | Usage error | 10 | Tool error |
| 3 | Configuration error | 11 | Budget exceeded |
| 4 | Authentication error | 12 | Timeout |
| 5 | Capability error | 13 | Cancelled |
| 6 | Permission error | 14 | Not found |
| 7 | Workspace error | | |

---

## Configuration

The configuration has six top-level sections. Unknown keys are rejected, so a
typo fails loudly instead of being silently ignored.

```yaml
version: 1

providers:          # endpoints and credentials
  local-ollama:
    type: ollama
    base_url: http://127.0.0.1:11434
    timeout: 120s

models:             # aliases bound to provider targets
  local-coder:
    provider: local-ollama
    model: qwen3-coder

agents:             # reusable delegation targets
  local-reviewer:
    description: Fully local code review using Ollama.
    model: local-coder
    prompt: ~/.pawagents/prompts/reviewer.md
    tools:
      - repo.read
      - repo.search
      - git.diff
    max_rounds: 20
    timeout: 10m
    permissions:
      filesystem: read
      shell: deny

security:           # workspace and secret policy
  allow_outside_workspace: false
  follow_symlinks: false
  max_file_size: 1048576
  redact_env:
    - "*_TOKEN"
    - "*_KEY"
    - "*_SECRET"
    - "*_PASSWORD"

sessions:           # persistence
  directory: ~/.pawagents/sessions
  persist_messages: true
  persist_tool_calls: true

telemetry:          # logging and (later) metrics
  enabled: false
  log_level: info
  log_format: text
```

**Provider types** understood by the configuration schema. "Status" describes
this build, not the roadmap:

| Type | Status |
| --- | --- |
| `ollama` | Implemented (native API, capability detection, tool calls) |
| `openai-compatible` | Implemented (Chat Completions, streaming, tools) |
| `anthropic` | Implemented (Messages API, native tool use, streaming) |
| `openai-responses` | Implemented (Responses API, schema constrained output) |
| `openai-chat` | Known, not implemented yet |
| `gemini` | Roadmap, accepted with a warning |
| `bedrock` | Roadmap, accepted with a warning |

`pagent provider list` reports the same distinction as a status column:
`ready`, `planned`, `unavailable` or `disabled`.

### Agents, models and capabilities

An agent never names a provider. The agent names a model **alias**, the alias
names a provider target, and one alias change moves every agent that uses it:

```yaml
models:
  strong-coder:
    primary:
      provider: cloud-anthropic
      model: claude-sonnet
    fallback:
      - provider: local-ollama
        model: qwen3-coder
```

Two commands describe that indirection. Neither contacts a network unless you
afterwards pass `--probe`:

```bash
pagent agent list
```

```
AGENT           TYPE    MODEL         TARGET                              TOOLS  OUTPUT      ROUNDS  TIMEOUT
cloud-reviewer  single  strong-coder  cloud-anthropic/claude-sonnet (+1)  2      structured  20      5m0s
local-reviewer  single  local-coder   local-ollama/qwen3.8:27b-mlx        6      structured  12      10m0s
```

```bash
pagent agent show local-reviewer
```

```
agent:       local-reviewer
description: Fully local code review using Ollama
type:        single

Model
  alias:     local-coder
  primary:   local-ollama/qwen3.8:27b-mlx

Tools
  repo.read    known
  git.diff     known
  permissions: filesystem=read shell=deny

Budget
  rounds:     12
  tool calls: 20
  timeout:    10m0s
  output mode: structured

Capabilities
  target:     local-ollama/qwen3.8:27b-mlx (static)
  model:      tool_calling:no  streaming  structured_output:no  max_context_tokens=8192
  needs:      tool_calling, system_message
  result:     ✗ missing: tool_calling
  note:       static capabilities; pass --probe to ask the endpoint
```

That last section is what makes the command worth running. An agent that grants
tools needs a model with tool calling; an agent with a prompt needs a system
role; an agent with a context budget needs a window large enough for it. Those
requirements are derived from the profile, checked against the model **before**
a task starts, and every unmet one is named, so a run never fails half way
through a conversation. `pagent run` refuses the same way, with the same
message.

The static table is conservative: it never assumes that a local model supports
tool calling, because an Ollama server can host one that does not. `--probe`
asks the endpoint what it actually supports — and an endpoint that does not
answer is then reported as a failure rather than being dressed up as a static
answer.

```bash
pagent agent show local-reviewer --probe
```

```
Capabilities
  target:     local-ollama/qwen3.8:27b-mlx (probed)
  model:      tool_calling  streaming  structured_output  vision  reasoning  max_context_tokens=262144
  needs:      tool_calling, system_message
  result:     ✓ the model satisfies every requirement
```

`pagent model show <alias>` describes the other direction: every target of an
alias, which one a run would use, and why the ones before it did not.

```bash
pagent model show strong-coder
```

```
alias:       strong-coder
status:      ready

Targets
  ✗ primary: cloud-anthropic/claude-sonnet
      status: unavailable
      error:  no implementation of anthropic is compiled into this build
  ✓ fallback 1: local-ollama/qwen3.8:27b-mlx
      status: ready
```

### OpenAI-compatible endpoints

The `openai-compatible` type covers everything that speaks the OpenAI Chat
Completions protocol: DeepSeek, DashScope, OpenRouter, LiteLLM, vLLM and a
company gateway.

```yaml
providers:
  deepseek:
    type: openai-compatible
    base_url: https://api.deepseek.com/v1
    api_key_env: DEEPSEEK_API_KEY
    headers:
      X-Tenant: acme
    extra_body:
      top_k: 40
    timeout: 120s
```

Supported knobs: `base_url`, `api_key`, `api_key_env`, `headers`,
`extra_body`, `timeout`, `proxy` and the `tls` block (`ca_cert_file`,
`client_cert_file`, `client_key_file`, `server_name`, `insecure_skip_verify`).
`extra_body` is merged into every request body, so a vendor specific flag never
needs a code change; a per-request `extra` wins over it.

### Anthropic

The `anthropic` type speaks the Messages API natively rather than through a
compatibility layer, because the protocol differs in ways that matter: content
is a list of typed blocks, the system prompt is a top level field, a tool result
travels back inside a user message, and a streaming answer reports its input
tokens once at the start and its output tokens cumulatively.

```yaml
providers:
  cloud-anthropic:
    type: anthropic
    base_url: https://api.anthropic.com
    api_key_env: ANTHROPIC_API_KEY
```

`base_url` defaults to `https://api.anthropic.com` and the credential is sent as
`x-api-key`. Extended thinking is surfaced as reasoning text rather than mixed
into the answer, and a reasoning trace is never replayed to the API: an
unsigned thinking block cannot be sent back, and inventing a signature would be
worse than dropping it.

One honest limitation: the Messages API has no `response_format`, so
`pagent agent show` reports `structured_output:no` for it. An agent with
`output_mode: structured` still works — the runtime asks for the finding
envelope in the prompt instead of constraining the completion, and a schema is
never sent to an endpoint that would reject it.

### OpenAI Responses

The `openai-responses` type speaks the Responses API, where the conversation is
a flat list of typed input items and a completion can be constrained by a JSON
schema.

```yaml
providers:
  cloud-openai:
    type: openai-responses
    base_url: https://api.openai.com/v1
    api_key_env: OPENAI_API_KEY
    organization: org_123
```

`store` is always sent as `false`. PawAgents is a read-only advisor that reads a
workspace on behalf of a host agent; persisting the conversation in the vendor's
account would be a side effect nobody asked for, and the session file is the
record. Reasoning summaries arrive as reasoning text, cached input tokens and
reasoning tokens are reported in the usage, and a schema is requested without
strict mode: the API only accepts a strict schema in which every property is
required and additional properties are forbidden, which the finding envelope is
not.

### Using Ollama

Ollama is a first class provider. Install it, pull a model and point PawAgents
at it — no cloud API key and no network access are required.

```bash
ollama pull qwen3-coder
ollama list
```

```yaml
providers:
  local-ollama:
    type: ollama
    base_url: http://127.0.0.1:11434
    timeout: 120s

models:
  local-coder:
    provider: local-ollama
    model: qwen3-coder

agents:
  local-reviewer:
    model: local-coder
    prompt: ~/.pawagents/prompts/reviewer.md
    tools:
      - repo.read
      - repo.search
      - git.diff
```

Check the whole path before running anything:

```bash
pagent provider test local-ollama
```

```
provider: local-ollama
endpoint: http://127.0.0.1:11434

Server
  ✓ Ollama 0.12.3

Installed models: 2

Models
  ✓ local-coder → qwen3-coder
      tool_calling  streaming  structured_output  reasoning\
  max_context_tokens=262144

result: ✓ every check passed
```

Each check corresponds to a failure that would otherwise only appear at run
time: the server is not running, the model was never pulled, or the model does
not support tool calling while its agent grants tools. Capabilities are read
from `/api/show`, so tool support is detected instead of assumed — a model that
reports no `tools` capability produces a capability error rather than a silent
degradation.

**Validation severity.** Errors make a configuration unusable: an unknown
provider type, a model pointing at a provider that does not exist, an agent
asking for shell access. Warnings describe a configuration that still runs: a
missing API key environment variable, a prompt file that has not been created, a
roadmap feature that is accepted but not yet wired up.

Then run a task. **No cloud model API is required** at any point:

```bash
pagent run \
    --agent local-reviewer \
    --task "Review my current git changes"
```

```
status:   completed
agent:    local-reviewer
model:    local-ollama/qwen3-coder
duration: 1m30s
usage:    7411 input tokens, 600 output tokens, 1 tool calls, 2 rounds

summary
  I read the diff and the surrounding code. One issue, in the retry path.

findings (1)

1. [high] The retry drops the request body
   where:      internal/provider/transport.go:88
   category:   correctness
   detail:     The second attempt reuses a consumed reader, so it sends an empty body.
   evidence:   req.Body is read once by the first attempt.
   suggestion: Buffer the body before the first attempt.
   confidence: 0.80
```

Useful flags:

| Flag | Purpose |
| --- | --- |
| `--agent` | The agent profile to run (required). |
| `--task` | The instruction, or `-` to read it from standard input. |
| `--workspace` | Directory the agent may read. Defaults to the working directory. |
| `--file` | Starting point for the investigation. Repeatable. |
| `--constraint` | A rule the answer must respect. Repeatable. |
| `--background` | Context the agent cannot discover for itself. |
| `--max-rounds`, `--timeout` | Override the agent budget for one run. |
| `--output` | `text` (default) or `json` for the full envelope. |

The result is always the same envelope — status, agent, model, summary, findings
and usage — whatever model produced it, and the exit code is derived from the
classified error, so a budget failure (11), a timeout (12) or a denied tool (6)
can be handled by a script without parsing prose. Structured output is a
*request*: PawAgents binds the model to a JSON schema when the provider can
enforce one, and otherwise asks for the envelope in the prompt. An empty finding
list is a valid answer.

---

## Sessions

Every delegated task is recorded, so a result can be re-read after the machine
that produced it is gone. `pagent run` prints the identifier, and `pagent
session list` finds it again:

```bash
pagent session list
pagent session show agt_5ccd1911c434718d --messages
```

A session is a directory under `sessions.directory`
(`~/.pawagents/sessions` by default):

| File | Contents |
| --- | --- |
| `metadata.json` | Identity and outcome: agent, model alias, resolved provider and model, workspace, status, timestamps, usage, counters, build version. |
| `result.json` | The structured result envelope the host agent received. |
| `messages.jsonl` | One conversation turn per line, plus one line per compaction. Images are recorded by MIME type, size and URL — their bytes are never copied. |
| `tools.jsonl` | One line per tool call: tool, arguments, size, duration, whether it failed and how. |

`metadata.json` is written **before** the first model call and rewritten when
the task finishes, so an interrupted run stays visible with the status
`running` instead of disappearing. Messages and tool calls share one sequence
counter, so the two files merge into a single timeline. Records of a session
are append-only; `result.json` and `metadata.json` are replaced atomically, so
a reader never sees half a document. `sessions.retention` keeps the newest N
sessions and prunes the rest when a task finishes; `0` means unlimited.
`persist_messages: false` and `persist_tool_calls: false` keep the metadata and
the result but write no transcript, for deployments that must not keep one.

### Context compaction

Delegations can outgrow a context window: a dozen tool calls on a large file
are enough. Before a request, the runner estimates the size of the
conversation and, once it passes **70 %** of the model's window, compacts it
instead of failing:

- the system message, the delegated task and the last six messages are kept;
- a tool result larger than 2000 characters is replaced by a placeholder that
  names the tool, its size and the first line, and tells the model to call the
  tool again with a narrower request;
- a long assistant message without tool calls is replaced by a marker.

The model is then told what happened: a short deterministic summary — how many
results were elided, which tools were used, which paths were seen — is appended
to the system prompt, so it knows that history was reduced and that it must not
guess at what it can no longer see. The summary is deterministic because a
paraphrase costs a model call that the compaction is trying to avoid. Every
compaction is recorded in the transcript. The result envelope reports the
compaction count in the session metadata. A compaction failure is logged and
the run continues with the uncompacted conversation.

---

## MCP server

A coding agent reaches PawAgents over the Model Context Protocol. The server is
host neutral: Codex, Claude Code and any other MCP client see the same three
tools, the same request schema and the same result envelope.

```bash
pagent mcp serve --stdio
```

Standard output carries the protocol, so diagnostics go to standard error. The
transport is a flag so that a later HTTP transport changes nothing a host sees.

| Tool | Purpose |
| --- | --- |
| `list_agents` | The configured agents with their model alias, tool grant and a readiness verdict. |
| `get_agent` | One agent in detail: targets, budget, permissions, prompt source, missing capabilities. |
| `delegate_task` | Run one bounded task and return the structured result. |

### The delegation contract

`delegate_task` is not a prompt wrapper. A delegated task states the agent, the
question, the workspace it may read, the background the agent cannot discover
for itself, the constraints the answer must respect, the output mode and a
budget that can only narrow the agent's own limits:

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

The reply is the same envelope `pagent run` prints — `status`, `agent`,
`provider`, `model`, `summary`, `findings`, `usage` and `session_id` — whatever
model produced it, plus `error_kind` when the task did not complete. A failure
is reported in one of three ways:

- **the task never started** (an unknown agent, an unusable workspace): the call
  fails and the message starts with the classified error kind;
- **the task ran and failed** (a budget, a timeout, a provider error): the call
  fails, and the envelope still carries the partial answer and the session id;
- **the task ran and completed**: the envelope is the result.

### Readiness

`list_agents` and `get_agent` report `ready` and `confirmed` rather than a bare
verdict. `ready` says whether the runtime expects the agent to run, and
`confirmed` says whether that answer rests on evidence: a probe of the endpoint,
or a provider contract that documents the capability. An agent that is not
ready and not confirmed has **unknown** capabilities, not missing ones — an
Ollama server hosts models with and without tool calling, so only the endpoint
can answer, and the runtime asks it when the task starts. A host that treats
those two cases alike would refuse to delegate to local models that work.

---

## Using with Codex

Codex starts the server itself, so `pagent` only has to be on `PATH`:

```bash
go build -o pagent ./cmd/pagent
sudo install pagent /usr/local/bin/pagent

codex mcp add pawagents -- pagent mcp serve --stdio
codex mcp list
```

One setting matters in practice: **Codex caps a tool call at 60 seconds by
default**, and a delegation is a whole agent run. Raise the limit in
`~/.codex/config.toml` to at least the `timeout_sec` of the agents you delegate
to:

```toml
[mcp_servers.pawagents]
command = "pagent"
args = ["mcp", "serve", "--stdio"]
tool_timeout_sec = 1800
```

Then ask for something that benefits from a second model:

```text
Ask PawAgents to independently review my current changes, using the local
reviewer. Then verify its findings before you act on them.
```

`integrations/codex/` packages the same wiring as a Codex plugin: the manifest,
the bundled MCP server and a skill that tells Codex when a delegation is worth
it. See [integrations/codex/README.md](integrations/codex/README.md) for the
plugin route, the skill install and the verification steps.

Two things to remember when reading a result:

- **The result is advisory.** PawAgents never writes to the workspace and never
  runs a shell command; verify critical findings before changing files.
- **A failure is classified.** The message starts with the error kind, and a
  delegation that ran but failed still reports the partial answer and the
  session id, which `pagent session show` can re-read.

---

## Using with Claude Code

Register the server once for yourself:

```bash
claude mcp add pawagents -- pagent mcp serve --stdio
claude mcp list
```

Or share it with a team through the project configuration, which Claude Code
approves on first use:

```bash
cp integrations/claude-code/.mcp.json.example /path/to/project/.mcp.json
```

The entry sets a per-call `timeout` of fifteen minutes, because a delegation is
a whole agent run: the field is milliseconds, and without it a stdio server falls
back to `MCP_TOOL_TIMEOUT`. Raise it for agents whose `timeout_sec` is larger.

Both routes name the server `pawagents`, so the tools are callable as
`mcp__pawagents__list_agents`, `mcp__pawagents__get_agent` and
`mcp__pawagents__delegate_task`.

Install the skill so Claude Code knows when delegating is worth it, and ask:

```bash
cp -R integrations/claude-code/.claude/skills/pawagents ~/.claude/skills/pawagents
```

```text
Use PawAgents reviewer to independently review my current changes, then verify
its findings before acting on them.

/pawagents
```

`integrations/claude-code/` holds the skill, the project `.mcp.json` example, a
`CLAUDE.md` snippet and a README with the verification and troubleshooting
steps. The skill states the same rules as the Codex skill, so both hosts behave
the same way for the same request.

---

## Security model

- External subagents are **read-only by default**. `permissions.filesystem:
  write` and `permissions.shell: allow` are rejected by validation, and the
  runtime refuses to build a tool grant that grants them.
- **Workspace escape is structurally impossible.** Every filesystem access goes
  through a single guard built on `os.Root`, the standard library sandbox: a
  name that would resolve outside the workspace is refused, whether it escapes
  through `..`, an absolute path or a symlink target. The workspace directory
  itself is resolved first, so a workspace reached through a symlink (macOS
  `/tmp`) behaves identically.
- **Symlinks are not followed by default.** `security.follow_symlinks: true`
  allows a symlink whose target stays inside the workspace. An absolute symlink
  is never followed, even when it points inside; use a relative symlink.
- **Blocked paths** (`security.blocked_paths`) are refused even inside the
  workspace, for example `*/.env` or `**/*.pem`.
- **Reads are bounded** by `security.max_file_size`, and tool results by the
  agent budget, so no single call can fill the context window.
- **No shell.** PawAgents never executes a command a model produced. The `git.*`
  tools run a fixed git subcommand with a validated argument vector: a revision
  is restricted to the characters git uses, a path is passed after `--`, and
  nothing is ever interpolated into a shell.
- **Secrets are never exposed to models.** Credentials are read from the
  environment at request time, environment variables are invisible to agents
  unless `security.allowed_env` names them *and* they do not look like a
  credential, and log output redacts anything that does.
- The host agent stays responsible for applying changes. PawAgents reports;
  Codex or Claude Code acts on the report.

### Tools

| Tool | Purpose |
| --- | --- |
| `repo.read` | Read a file with line numbers, optionally a line range. |
| `repo.list` | List a directory, optionally several levels deep. |
| `repo.search` | Find text or a regular expression, returning `path:line: text`. |
| `repo.stat` | Type, size, permissions, modification time and line count. |
| `git.diff` | Unified diff of working tree, index or against a revision. |
| `git.show` | One commit with its message and diff. |
| `git.status` | Branch and every changed path. |
| `git.log` | Recent commits, optionally following one path. |

---

## Diagnosing a broken installation

When a coding agent cannot delegate, `pagent doctor` says which part of the
installation is wrong:

```bash
pagent doctor
pagent doctor --probe          # ask remote endpoints too
pagent doctor --workspace /path/to/repo
pagent doctor --output json
```

The report covers the pieces a delegation depends on:

| Section | What it answers |
| --- | --- |
| Config | The file in use, plus every validation error and warning. |
| Providers | Type supported by this build, credential present, endpoint reachable. |
| Ollama | The local model server, and whether the models the aliases name are pulled. |
| Agents | Whether each agent can run against its model, and why not. |
| Workspace | Readable, inside the limits, and a Git repository the git tools accept. |
| MCP | The transport, the tools a host will see, and the commands to register it. |

Every check that can be answered from the configuration is offline. A local
model server is probed because that is where the interesting failures live and
the call never leaves the machine; `--probe` adds remote endpoints, which costs
a round trip but catches a wrong API key or a wrong base URL before a task does.

The exit status is the classification of the first failed check, so a script can
tell a missing credential (`4`) from an unusable workspace (`7`) or a broken
configuration (`3`). Warnings do not fail the command, and a check that only
concerns a provider nobody uses is a warning, not a failure.

```text
Providers
  ✗ cloud-anthropic (anthropic)
      environment variable ANTHROPIC_API_KEY is not set
  ✓ local-ollama (ollama)
      http://127.0.0.1:11434

Ollama
  ✓ qwen3.8:27b-mlx
      used by local-coder

Agents
  ✗ cloud-reviewer (strong-coder)
      no credential for cloud-anthropic (set ANTHROPIC_API_KEY)
  ! local-reviewer (local-coder)
      the endpoint decides whether this model can run the agent
```

The `!` line is the one distinction worth knowing: a readiness verdict that is
not `confirmed` means nobody has asked the endpoint yet, not that the model is
unsuitable. `pagent agent show <agent> --probe` answers it for one agent.

---

## Creating an agent

An agent is the reusable half of a delegation: a prompt, a model alias, a tool
grant, a budget and permissions. Add one to the configuration and it is
immediately visible to `pagent agent list` and to a host through `list_agents`:

```yaml
agents:
  reviewer:
    description: Deep source-code review
    model: strong-coder          # an alias, not a provider model name
    prompt: reviewer.md          # a file, or inline instructions
    tools: [repo.read, repo.search, git.diff]
    output_mode: structured
    max_rounds: 24
    timeout_sec: 300
```

```bash
pagent config validate      # every problem, not just the first
pagent agent show reviewer  # the resolved profile and whether it can run
```

Details that matter:

- **The prompt is a path or inline text.** A path is resolved relative to the
  configuration file; a missing file falls back to the prompt built into the
  binary for that name, which is reported by `pagent agent show`.
- **The alias decouples the agent from the model.** Switching `strong-coder`
  from a cloud model to a local one is a one-line change, and the agent does
  not move.
- **Tools are a grant, not a menu.** An agent may only call what it lists, and
  the runtime refuses a tool the model cannot use with `CapabilityError`
  instead of pretending.
- **Writes are rejected by validation.** `permissions.filesystem: write` and
  `permissions.shell: allow` do not load, because external subagents are
  read-only by design.
- **`output_mode: structured` asks for the finding envelope.** Providers that
  can enforce a JSON schema do; the others are asked for it in the prompt.

---

## Development

```bash
make build         # bin/pagent
make test          # go test ./...
make vet           # go vet ./...
make fmt-check     # fail when a file is not gofmt-clean
make lint          # golangci-lint when installed, otherwise go vet
make install       # go install ./cmd/pagent
make integration-test   # build the binary and run the scripted checks
make docs-check    # run every command this README documents
```

The same commands without the `Makefile`:

```bash
go test ./...      # unit tests
go vet ./...       # static checks
go build ./...     # compile everything
./scripts/integration-test.sh   # builds the binary and runs the test suite
./scripts/check-docs.sh         # executes the documented commands
```

Every command this README shows is executed by `scripts/check-docs.sh`: it
builds the binary, creates a configuration in a scratch home, runs each
invocation and asserts the exit status the documentation promises. A unit test
(`internal/cli/readme_test.go`) checks the other direction, so the README can
neither document a command that does not exist nor hide one that does. Commands
that need a model endpoint are gated behind `PAWAGENTS_TEST_OLLAMA=1`.

Integration tests that require external services are opt-in, so CI never fails
on a machine without Ollama:

```bash
PAWAGENTS_TEST_OLLAMA=1 ./scripts/integration-test.sh   # a real model
PAWAGENTS_TEST_MCP=1 ./scripts/integration-test.sh      # a real MCP client
PAWAGENTS_TEST_CLAUDE=1 ./scripts/integration-test.sh   # the real Claude Code CLI
PAWAGENTS_TEST_CODEX=1 ./scripts/integration-test.sh    # the real Codex CLI
```

A gated test skips with the reason it could not run, and the script prints every
skip, so a green run never hides an unverified chain:

| Gate | What it proves |
| --- | --- |
| `PAWAGENTS_TEST_OLLAMA=1` | `pagent provider test` passes against the real server, `pagent run` reviews a real uncommitted change with the read-only tools and reports locations that exist in the repository, the session is readable back, and a model that is not installed fails with the action that fixes it. |
| `PAWAGENTS_TEST_MCP=1` | A real MCP client spawns `pagent mcp serve --stdio`, completes the handshake, validates every schema root, lists and describes the agents, reads a classified failure, and delegates a task that ends in a structured result. |
| `PAWAGENTS_TEST_CLAUDE=1` | The Claude Code CLI accepts the registration and its own `mcp list` reports the server as connected, which is the host validating the handshake and the tool schemas. |
| `PAWAGENTS_TEST_CODEX=1` | The Codex CLI accepts the registration and lists the server. |

The model the review tests use defaults to the first model the server has
installed; set `PAWAGENTS_TEST_MODEL` to choose one.

The two chains the MVP promises are covered by these gates together:

```
Codex / Claude Code  ->  MCP  ->  PawAgents  ->  Ollama  ->  structured result
     host gate + MCP gate + Ollama gate
```

A host's own model configuration is outside this project, so the last hop is
verified by asking the local model through MCP rather than by driving the host's
model loop. [Using with Claude Code](#using-with-claude-code) describes the
interactive run.

### What the tests cover

| Layer | What it pins down |
| --- | --- |
| Unit tests, next to the code | Parsing, validation, the workspace guard, tool arguments, compaction, the session store, routing and budgets. |
| Provider contract tests | The same behaviours for every provider: streaming, tool calls, usage, error classification and capability reporting. |
| Agent tests | The loop: rounds, budgets, tool rounds, the structured result and a failing compactor. |
| Security tests | Path traversal, absolute path escape, symlink escape, blocked paths and size limits. |
| MCP tests | A real MCP client over an in-memory transport: discovery, the schemas a host validates, the delegation contract and classified failures. |
| Documentation tests | The README's command list against the command tree, and every command's help text. |
| `tests/` | The host integrations as data, and the end-to-end runs that need a built binary. |
| `tests/integration`, `tests/mcp` | The acceptance chains above, behind the `integration` build tag. |
| `scripts/check-docs.sh` | Every documented invocation, executed against a generated configuration. |

See [AGENTS.md](AGENTS.md) for the architectural rules that changes must
respect, and [docs/](docs/) for the longer explanations.

---

## Documentation

| Document | Content |
| --- | --- |
| [`docs/architecture.md`](docs/architecture.md) | package boundaries, the agent loop, the data flow |
| [`docs/configuration.md`](docs/configuration.md) | every configuration key, its default and its validation |
| [`docs/providers.md`](docs/providers.md) | provider types, capability checks, adding a provider |
| [`docs/agents.md`](docs/agents.md) | agent profiles, budgets, prompts, results |
| [`docs/mcp.md`](docs/mcp.md) | the MCP tools, the delegation contract, host wiring |
| [`docs/security.md`](docs/security.md) | workspace guard, permissions, secrets, threat model |

## Repository layout

```
cmd/pagent            CLI entry point
internal/cli          command tree, flags, rendering
internal/config       configuration schema, loading, defaults, validation
internal/llm          vendor neutral protocol: messages, tools, events, usage
internal/provider     provider interface, transport, registry, capabilities
internal/provider/*   concrete providers (ollama, openaicompat, ...)
internal/orchestrator agent profiles, model routing, capability validation, budget
internal/agent        agent loop: prompt building, tool rounds, structured result
internal/tools        tool runtime: registry, executor, argument decoding
internal/tools/repo   repository readers (read, list, search, stat)
internal/tools/git    git readers (diff, show, status, log)
internal/security     workspace guard, permissions, secret handling
internal/session      session store: metadata, result, transcript, tool calls
internal/mcpserver    MCP server and the three host facing tools
internal/doctor       the diagnostics behind `pagent doctor`
integrations          host glue: Codex and Claude Code, never part of the core
internal/apperrors    error kinds and process exit codes
internal/logging      structured logging with redaction
internal/version      build metadata
prompts               built-in agent system prompts (embedded in the binary)
docs                  long-form documentation
examples              starter configurations, validated by the tests
scripts               developer scripts
```

### Adding a provider

A provider implements four methods and registers itself:

```go
func init() {
    provider.Register(config.ProviderTypeMyVendor, New)
}

type Provider struct{ /* endpoint, credential, client */ }

func (p *Provider) Name() string { return p.name }
func (p *Provider) Type() string { return config.ProviderTypeMyVendor }
func (p *Provider) Capabilities(ctx context.Context, model string) (llm.ModelCapabilities, error)
func (p *Provider) Generate(ctx context.Context, request *llm.GenerateRequest) (llm.Stream, error)
```

The contract is deliberately small. A provider translates in both directions
and reports capabilities; it never retries, never decides policy and never
holds budget state, because those belong to the agent loop and the
orchestrator.

---

## Roadmap

| Phase | Content |
| --- | --- |
| 0.1 | Core runtime, OpenAI-compatible, Anthropic, native Ollama, Codex, Claude Code |
| 0.2 | Session continuation, provider fallback, HTTP MCP transport, the `get_task`, `continue_task` and `cancel_task` tools |
| 0.3 | Parallel agent teams, routing, OpenTelemetry |
| 1.0 | Stable provider API, stable MCP contract, plugin ecosystem |

Phase status for this tree is tracked in [Status](#status).

---

## License

[Apache-2.0](LICENSE)
