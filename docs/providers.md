# Providers

A provider is one wire protocol. It translates a PawAgents request into the
vendor's shape, translates the events back into `internal/llm`, and reports what
the model can do. It never retries, never decides policy and never holds budget
state.

```go
type Provider interface {
    Name() string
    Type() string
    Capabilities(ctx context.Context, model string) (llm.ModelCapabilities, error)
    Generate(ctx context.Context, request *llm.GenerateRequest) (llm.Stream, error)
}
```

Two more interfaces are optional and are used by diagnostics:

```go
// HealthReporter probes the endpoint without a generation call.
type HealthReporter interface{ Health(ctx context.Context) (Health, error) }

// ModelLister enumerates the models the endpoint serves.
type ModelLister interface{ ListModels(ctx context.Context) ([]string, error) }
```

## The provider types

| Type | Protocol | Capabilities | Notes |
| --- | --- | --- | --- |
| `ollama` | The Ollama HTTP API | Discovered by probing `/api/show` | A local server can host a model with or without tool calling, so the static table assumes nothing. |
| `openai-compatible` | Chat completions | Assumed conservative subset; tool calling never | The protocol and its model listing carry no capability metadata, so an alias that needs tools declares `tool_calling: true`. |
| `openai-responses` | The Responses API | Declared | Schema-constrained output, reasoning summaries as thinking, `store: false` always. |
| `anthropic` | The Messages API | Declared, without structured output | A completion cannot be constrained to a JSON schema, so the envelope is requested in the prompt. |

A type that is known but not compiled into the build is reported as
`unavailable`; a type that only exists on the roadmap is `planned`. Both are
visible in `pagent provider list`, and a model alias that points at one is
reported by `pagent agent show` rather than failing in the middle of a task.

## Credentials

A credential is read from the environment at the moment of the request:

- `api_key_env` names the variable; `api_key` is an inline fallback.
- A credential never reaches a model, never enters a session file and is masked
  in every log line.
- An environment variable whose name looks like a credential (`KEY`, `TOKEN`,
  `SECRET`, `PASSWORD`, `AUTH`) is invisible to a tool unless
  `security.allowed_env` names it explicitly.

## Capabilities

```bash
pagent provider list                       # types and status, offline
pagent provider show <provider>            # endpoint, models, agents
pagent provider test <provider>            # reachability, models, capabilities
pagent model show <alias> --probe          # resolve an alias, asking the endpoint
```

Capabilities come from one of three places, in this order:

1. **An explicit override** in the configuration: an operator who writes
   `tool_calling: true` for a local model knows something the probe could not
   detect.
2. **A probe** of the endpoint, which is the only way to know what a local model
   supports.
3. **The static table** for the provider type, which is a conservative default
   rather than a fact.

A verdict from the third source is reported as unconfirmed: `pagent doctor`,
`pagent agent show` and the MCP `list_agents` payload all distinguish "this model
cannot" from "nobody has asked the endpoint yet", because the two call for
opposite decisions.

An `openai-compatible` endpoint never reports tool calling, and a probe does not
change that: the Chat Completions protocol defines no capability metadata, and
the `/models` listing that most gateways expose carries model identifiers only.
A model being listed therefore says nothing about function calling. An alias
that grants tools must declare it, otherwise `pagent run` stops before the first
request with a `CapabilityError`:

```yaml
models:
  gateway-coder:
    provider: gateway
    model: strong-model
    capabilities:
      tool_calling: true     # this gateway is a full clone
```

## Writing a provider

1. Create `internal/provider/<name>/` and implement the four methods. Convert
   to `internal/llm` at the boundary; never let a vendor type escape.
2. Register it: `func init() { provider.Register(config.ProviderTypeX, New) }`.
3. Add the package to `internal/provider/all`, the list imported by the binary.
4. Add the type to `provider.StaticCapabilities` and, when the negative answers
   are not authoritative, keep it out of `provider.StaticVerdictAuthoritative`.
5. Run the provider contract tests, which every provider shares: streaming,
   tool calls, usage, error classification and capability reporting.
6. Document the endpoint shape in this file and add an example under
   `examples/`.

Rules that are not negotiable: no retries inside a provider, no policy
decisions, no budget accounting, no vendor type above the provider boundary, and
secrets never written anywhere.
