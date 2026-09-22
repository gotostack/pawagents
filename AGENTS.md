# AGENTS.md

Guidance for coding agents working in this repository. Read this before
changing code; the constraints below are project requirements, not
suggestions.

## What this project is

PawAgents is a universal external subagent runtime. Coding agents such as
OpenAI Codex and Claude Code connect over MCP and delegate a bounded task to a
named subagent. PawAgents owns the agent loop, the tool runtime and the provider
layer. It is **not** another chat CLI and **not** a thin wrapper over a model
API.

## Non-negotiable design rules

1. **Hosts stay outside the core.** No Codex-specific or Claude Code-specific
   code may appear in `internal/agent`, `internal/provider`, `internal/tools` or
   `internal/mcpserver`. Host integration lives in `integrations/` and in the
   MCP server only.
2. **Two hosts, one contract.** Codex and Claude Code must see exactly the same
   MCP tools and the same request/response schema. Never define a
   `CodexDelegateRequest` or a `ClaudeDelegateRequest`.
3. **No vendor types in the runtime.** `openai.ChatCompletionMessage`,
   `anthropic.Message` and `ollama.ChatResponse` must be converted to
   `internal/llm` at the provider boundary. That package is the only shared
   protocol: providers and the agent loop import it, and it imports nothing but
   the standard library and `apperrors`. Never add a vendor import to it.
4. **Capabilities are checked, never assumed.** A model that does not support
   tool calling produces a `CapabilityError`; never fall back to faking tool
   calls with text tags.
5. **Read-only by default.** External subagents analyse and report. They never
   modify files, never run shell commands and never hold write permissions.
   PawAgents is the advisor, the host agent is the executor.
6. **The workspace guard is mandatory.** Every filesystem access goes through
   `internal/security`. Path traversal, absolute path escape and symlink escape
   must be impossible.
7. **Secrets never reach a model.** Credentials are read from the environment at
   request time, are never written into a session file and are redacted in logs.
8. **Budgets are enforced.** Rounds, tool calls, tokens, tool output bytes and
   timeouts are all bounded, and exceeding one produces `BudgetExceededError`.
9. **Classified errors only.** Return `*apperrors.Error`. The kind is part of
   the public contract: it drives the CLI exit code and the MCP error code.
10. **Small, single-purpose files.** No giant single-file implementations. New
    behaviour goes into the package that owns the concept.

## Layout

```
cmd/pagent            CLI entry point
internal/cli          command tree, flags, rendering
internal/config       configuration schema, loading, defaults, validation
internal/llm          vendor neutral protocol: messages, tools, events, usage
internal/agent        agent loop, context management, results
internal/orchestrator delegation, budgets, routing
internal/provider     provider abstraction and concrete providers
internal/tools        tool runtime (repo.*, git.*)
internal/mcpserver    MCP server and MCP tools
internal/session      session persistence
internal/security     workspace guard, permissions, secret redaction
internal/apperrors    error kinds and process exit codes
internal/logging      structured logging with redaction
internal/version      build metadata
prompts               built-in agent system prompts (embedded)
integrations          Codex and Claude Code glue
docs, examples        user facing documentation and sample configuration
```

Unit tests live next to the code they cover. Cross-cutting integration tests
live in `tests/`.

## Conventions

- Go 1.25+. Format with `gofmt -s`; run `go vet ./...` before committing.
- Wrap errors with `apperrors.Wrap(kind, op, ...)`; pass the operation as
  `package.function`.
- Configuration keys are snake_case and use the same name in YAML, JSON and the
  documentation.
- Add a `--help` description for every user-facing flag and command.
- Comment exported identifiers; explain *why*, not *what*.

## Required checks before every commit

```bash
gofmt -s -l .      # must print nothing
go vet ./...       # must pass
go test ./...      # must pass
go build ./...     # must pass
```

The main branch must always build. Never land a change that leaves the tree
uncompilable.

## Commit messages

Write complete English messages. The subject line must be at most 80
characters, every body line at most 80 characters. The body explains what was
developed or fixed, how it was solved, what the implementation does, and which
tests cover it.
