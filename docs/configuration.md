# Configuration

PawAgents reads one YAML file. Its location is resolved in this order:

1. `--config <path>`
2. `$PAWAGENTS_CONFIG`
3. `$PAWAGENTS_HOME/config.yaml`
4. `~/.pawagents/config.yaml`

`pagent config path` prints the file that would be used, and `pagent config
init` writes a starter file plus the built-in prompts. If no file exists, the
built-in defaults are used and `pagent config show` says so.

Unknown keys are rejected rather than ignored: a typo in a key name is a
configuration error, not a silently ignored setting.

## Top level

```yaml
version: 1                    # schema version, currently 1

providers: {}                 # endpoints and credentials
models: {}                    # aliases: the stable names agents use
agents: {}                    # what a host can delegate to

security:
  workspace_root: ""          # default directory a delegation may read
  allow_outside_workspace: false
  follow_symlinks: false
  max_file_size: 1048576      # per read, in bytes
  blocked_paths:              # additional globs; sensitive-file defaults always apply
    - "secrets/*"
  allowed_env: []             # env vars an agent may see

sessions:
  directory: ""               # default: $PAWAGENTS_HOME/sessions
  persist_messages: true      # write messages.jsonl
  persist_tool_calls: true    # write tools.jsonl
  retention: 0                # keep the newest N sessions, 0 = unlimited

telemetry:
  enabled: true               # local metrics only
```

| Environment variable | Effect |
| --- | --- |
| `PAWAGENTS_CONFIG` | Configuration file to load. |
| `PAWAGENTS_HOME` | Base directory instead of `~/.pawagents`. |
| `PAWAGENTS_SESSIONS_DIR` | Overrides `sessions.directory`. |
| `PAWAGENTS_LOG_LEVEL` | `debug`, `info`, `warn`, `error`. |
| `PAWAGENTS_LOG_FORMAT` | `text` or `json`. |
| `PAWAGENTS_TELEMETRY_ENABLED` | Overrides `telemetry.enabled`. |
| `PAWAGENTS_ALLOW_OUTSIDE_WORKSPACE` | Overrides `security.allow_outside_workspace`. |

The Workspace Guard always denies common credential files, including `.env`
and `.env.*`, private-key/certificate files (`*.pem`, `*.key`, `*.p12`, `*.pfx`,
`*.p8`, `*.jks`), common SSH private-key names, and files such as `.netrc`,
`.npmrc`, `.pypirc`, `.git-credentials`, cloud credentials, and service-account
JSON files. `security.blocked_paths` only adds rules; it cannot disable these
built-in protections. Repository tools apply the same deny rules to read, stat,
list, and search, with case-insensitive matching for filesystem portability.

## Providers

A provider names an endpoint and where its credential comes from. Credentials
are read from the environment at request time, never stored in a session and
never written to a log.

```yaml
providers:
  local-ollama:
    type: ollama
    base_url: http://127.0.0.1:11434

  gateway:
    type: openai-compatible
    base_url: https://gateway.internal/v1
    api_key_env: GATEWAY_API_KEY
    headers:
      X-Team: platform

  anthropic:
    type: anthropic
    api_key_env: ANTHROPIC_API_KEY
```

| Field | Meaning |
| --- | --- |
| `type` | Wire protocol: `ollama`, `openai-compatible`, `anthropic`, `openai-responses`. Types this build does not implement are reported by `pagent provider list`, not silently ignored. |
| `base_url` | Endpoint root. Empty uses the type's documented default. |
| `api_key` | An inline credential. Useful for a local gateway; prefer `api_key_env` for anything real. |
| `api_key_env` | Name of the environment variable holding the credential. |
| `headers` | Extra headers merged into every request. |
| `disabled` | Keeps the entry but stops the runtime from using it. |

## Models

A model entry is an alias: the name an agent uses, decoupled from the provider
and the model name behind it.

```yaml
models:
  strong-coder:
    provider: anthropic
    model: claude-sonnet-4-5

  local-coder:
    provider: local-ollama
    model: qwen3.8:27b-mlx
    temperature: 0.2
    max_context_tokens: 262144
    max_output_tokens: 8192
    capabilities:                # only when detection is wrong
      tool_calling: true
```

| Field | Meaning |
| --- | --- |
| `provider` / `model` | The target. A `primary` plus `fallback` list is accepted for a later release and reported as not implemented. |
| `temperature` | Overrides the provider default. |
| `max_context_tokens`, `max_output_tokens` | Override what detection reports, for a model that does not advertise its window. |
| `capabilities` | Corrects the capability table, for example when a local model supports tools without advertising them. An override wins over everything, including a probe. |

## Agents

```yaml
agents:
  reviewer:
    description: Deep source-code review
    model: strong-coder
    prompt: reviewer.md          # a file, resolved next to this configuration
    # instructions: |            # inline text instead of a file
    #   Review the change.
    tools: [repo.read, repo.search, git.diff, git.status]
    output_mode: structured      # or text
    max_rounds: 24
    max_tool_calls: 60
    timeout_sec: 300
    permissions:
      filesystem: read           # write is rejected
      shell: deny                # allow is rejected
```

| Field | Meaning |
| --- | --- |
| `model` | A model alias, never a provider model name. |
| `prompt` | A path to a prompt file. A missing file falls back to the prompt built into the binary for that name, which `pagent agent show` reports. |
| `instructions` | Inline prompt text instead of a file. |
| `tools` | The tool grant. An unknown tool name is a validation warning and a capability failure at run time. |
| `output_mode` | `structured` asks for the finding envelope; `text` returns the answer as written. |
| `max_rounds`, `max_tool_calls`, `timeout_sec` | The budget. A delegation may narrow these per task, never widen them. |
| `permissions.filesystem` | `read` only. External subagents never write. |
| `permissions.shell` | `deny` only. PawAgents never runs a shell command of its own. |

## Validation

```bash
pagent config validate
```

Validation reports every problem, not just the first. Errors stop a run; warnings
do not. A missing credential environment variable is a warning, so a machine that
only runs a local model is never blocked. `pagent config show` prints the
effective configuration — defaults merged with the file, paths expanded,
credentials redacted — which is the fastest way to answer "what is it actually
using?".
