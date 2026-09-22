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
| Internal LLM protocol (messages, tools, events, capabilities) | Done |
| HTTP transport with TLS, proxy, credential and header handling | Done |
| Provider registry with lazy construction | Done |
| OpenAI-compatible provider (chat completions, streaming, tools) | Done |
| Native Ollama provider with capability detection | Done |
| `pagent provider list`, `pagent provider show`, `pagent provider test` | Done |
| Workspace guard: traversal, symlink, blocked path and size enforcement | Done |
| Read-only repository tools and git tools | Done |
| Agent loop and `pagent run` | Next |
| MCP server, Codex and Claude Code integration | Planned |

**Not implemented yet** — do not expect these to work:

`pagent run`, `pagent doctor`, `pagent model …`, `pagent agent …`,
`pagent session …`, `pagent mcp serve`. They are described in the roadmap
below, and each one is documented here as soon as it lands.

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

**Provider types** understood by the configuration schema."Status" describes
this build, not the roadmap:

| Type | Status |
| --- | --- |
| `ollama` | Implemented (native API, capability detection, tool calls) |
| `openai-compatible` | Implemented (Chat Completions, streaming, tools) |
| `openai-responses` | Known, not implemented yet |
| `openai-chat` | Known, not implemented yet |
| `anthropic` | Known, not implemented yet |
| `gemini` | Roadmap, accepted with a warning |
| `bedrock` | Roadmap, accepted with a warning |

`pagent provider list` reports the same distinction as a status column:
`ready`, `planned`, `unavailable` or `disabled`.

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
internal/llm          vendor neutral protocol: messages, tools, events, usage
internal/provider     provider interface, transport, registry, capabilities
internal/provider/*   concrete providers (ollama, openaicompat, ...)
internal/tools        tool runtime: registry, executor, argument decoding
internal/tools/repo   repository readers (read, list, search, stat)
internal/tools/git    git readers (diff, show, status, log)
internal/security     workspace guard, permissions, secret handling
internal/apperrors    error kinds and process exit codes
internal/logging      structured logging with redaction
internal/version      build metadata
prompts               built-in agent system prompts (embedded in the binary)
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
| 0.2 | Session continuation, provider fallback, HTTP MCP transport |
| 0.3 | Parallel agent teams, routing, OpenTelemetry |
| 1.0 | Stable provider API, stable MCP contract, plugin ecosystem |

Phase status for this tree is tracked in [Status](#status).

---

## License

[Apache-2.0](LICENSE)
