# PawAgents

**A universal external subagent runtime for coding agents.**

PawAgents lets an existing coding agent — OpenAI Codex, Claude Code, and any
other MCP-capable host — delegate a bounded task to an independent subagent that
may run on a completely different model, provider or machine.

```
Codex  ─┐
        ├─ MCP ─→ pagent runtime ─→ LLM Router ─→ provider
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
| Agent runtime, tool runtime, providers, MCP server | Planned |

**Not implemented yet** — do not expect these to work:

`pagent run`, `pagent doctor`, `pagent provider …`, `pagent model …`,
`pagent agent …`, `pagent session …`, `pagent mcp serve`. They are described in
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
| **Session** | The persisted record of one delegated task, replayable and continuable. |

---

## Installation

Requires Go 1.24 or newer.

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

**Provider types** understood by the configuration schema:

| Type | Status |
| --- | --- |
| `ollama` | Planned (native provider, phase 4) |
| `openai-compatible` | Planned (phase 3) |
| `openai-responses` | Planned (phase 8) |
| `openai-chat` | Planned |
| `anthropic` | Planned (phase 8) |
| `gemini` | Roadmap, accepted with a warning |
| `bedrock` | Roadmap, accepted with a warning |

**Validation severity.** Errors make a configuration unusable: an unknown
provider type, a model pointing at a provider that does not exist, an agent
asking for shell access. Warnings describe a configuration that still runs: a
missing API key environment variable, a prompt file that has not been created, a
roadmap feature that is accepted but not yet wired up.

---

## Security model

- External subagents are **read-only by default**. `permissions.filesystem:
  write` and `permissions.shell: allow` are rejected by validation.
- Workspace escape is forbidden: `../` traversal, absolute paths outside the
  workspace and symlink escapes are blocked at the tool boundary.
- Secrets are never exposed to models. Credentials are read from the environment
  at request time, environment variables are not visible to agents by default,
  and log output redacts anything that looks like a credential.
- The host agent stays responsible for applying changes. PawAgents reports;
  Codex or Claude Code acts on the report.

---

## Development

```bash
go test ./...      # unit tests
go vet ./...       # static checks
go build ./...     # compile everything
./scripts/integration-test.sh   # builds the binary and runs the test suite
```

Integration tests that require external services are opt-in, so CI never fails
on a machine without Ollama:

```bash
PAWAGENTS_TEST_OLLAMA=1 ./scripts/integration-test.sh
PAWAGENTS_TEST_MCP=1 ./scripts/integration-test.sh
```

Unit tests live next to the code they cover; cross-cutting integration tests
live under `tests/`.

See [AGENTS.md](AGENTS.md) for the architectural rules that changes must
respect.

---

## Repository layout

```
cmd/pagent            CLI entry point
internal/cli          command tree, flags, rendering
internal/config       configuration schema, loading, defaults, validation
internal/apperrors    error kinds, exit codes
internal/logging      structured logging with redaction
internal/security     workspace guard, permissions, secret handling
internal/version      build metadata
prompts               built-in agent system prompts (embedded in the binary)
scripts               developer scripts
```

---

## Roadmap

| Phase | Content |
| --- | --- |
| 0.1 | Core runtime, OpenAI-compatible, Anthropic, native Ollama, Codex, Claude Code |
| 0.2 | Session continuation, provider fallback, HTTP MCP transport |
| 0.3 | Parallel agent teams, routing, OpenTelemetry |
| 1.0 | Stable provider API, stable MCP contract, plugin ecosystem |

Phase status for this tree is tracked in [Status](#status).

---

## License

[Apache-2.0](LICENSE)
