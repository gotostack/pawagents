# Security model

PawAgents is the advisor; the host agent is the executor. Every rule below
follows from that split.

## Read-only by default

An external subagent reads a workspace and reports. It never modifies a file,
never runs a shell command and never holds write permissions:

- `permissions.filesystem: write` and `permissions.shell: allow` are rejected by
  configuration validation, not merely discouraged.
- The tool runtime refuses to build a grant that would allow a write.
- The only tools that exist are readers: `repo.read`, `repo.list`,
  `repo.search`, `repo.stat`, `git.diff`, `git.show`, `git.status`, `git.log`.
- `git.*` runs a fixed subcommand with a validated argument vector; a revision is
  restricted to the characters git uses, a path is passed after `--`, and nothing
  is ever interpolated into a shell.

## Workspace escape is structurally impossible

Every filesystem access goes through one guard in `internal/security`, built on
`os.Root`, the standard library sandbox:

- A name that would resolve outside the workspace is refused, whether it escapes
  through `..`, an absolute path or a symlink target.
- The workspace directory itself is resolved through symlinks first, so a
  workspace reached through one (macOS `/tmp`) behaves identically.
- Symlinks are not followed by default. `security.follow_symlinks: true` allows a
  relative symlink whose target stays inside the workspace; an absolute symlink
  is never followed, even when it points inside.
- The guard always refuses common credential files, including `.env` and
  `.env.*`, private-key/certificate files (`*.pem`, `*.key`, `*.p12`, `*.pfx`,
  `*.p8`, `*.jks`), common SSH private-key names, and credential files such as
  `.netrc`, `.npmrc`, `.pypirc`, `.git-credentials`, cloud credentials, and
  service-account JSON files. These defaults apply even when a caller supplies
  its own `blocked_paths`, and matching is case-insensitive for consistent
  protection on case-insensitive filesystems.
- `security.blocked_paths` adds custom globs to that built-in deny list; it
  cannot remove the built-in credential protections. Blocked files are refused
  by read/stat operations and omitted from directory listings and searches.
- Reads are bounded by `security.max_file_size`, and tool output by the agent
  budget, so no single call can fill the context window.

The guard takes plain options and never imports `internal/config`, so it can be
tested and reasoned about on its own.

## Secrets never reach a model

- Credentials are read from the environment at request time, are never written
  into a session file, and are masked in every log line.
- Environment variables are invisible to the agent runtime unless
  `security.allowed_env` names them *and* the name does not look like a
  credential. A name containing `KEY`, `TOKEN`, `SECRET`, `PASSWORD` or `AUTH` is
  never exposed.
- `pagent config show` redacts credentials; `--show-secrets` is an explicit,
  deliberate opt-in.
- A tool result is data, not instructions: the prompt tells the model that file
  content is evidence, and the runtime never lets a tool result change a
  permission decision.

## Budgets are enforced, not suggested

Rounds, tool calls, input and output tokens, tool output bytes and wall clock are
all bounded. Exceeding one ends the task with a `BudgetExceededError` and a
partial result rather than an unbounded run. The limits belong to the agent, and
a delegation from a host may narrow them but never widen them.

## Classified errors

Failures are `*apperrors.Error` with a kind, and the kind is part of the public
contract:

| Kind | Exit | Typical cause |
| --- | --- | --- |
| `ConfigError` | 3 | A configuration that does not load or does not validate. |
| `AuthenticationError` | 4 | A missing or rejected credential. |
| `CapabilityError` | 5 | The model cannot do what the agent needs. |
| `PermissionError` | 6 | A tool was not granted. |
| `WorkspaceError` | 7 | A path outside the workspace, or an unusable directory. |
| `ProviderError` | 8 | The endpoint failed. |
| `BudgetExceededError` | 11 | A limit was reached. |
| `TimeoutError` | 12 | The deadline passed. |
| `CancelledError` | 13 | The caller cancelled. |
| `NotFoundError` | 14 | An agent or model alias that does not exist. |
| `InvalidArgumentError` | 2 | A malformed request or flag. |

A host sees the same classification as the prefix of an MCP tool error, so a
coding agent can branch on the kind without parsing prose.

## What the user controls

| Control | Effect |
| --- | --- |
| `security.workspace_root` | The default directory a delegation may read. |
| `security.allow_outside_workspace` | Turns the sandbox into "the whole filesystem, read-only". Reported as a warning by `pagent doctor`. |
| `security.blocked_paths` | Additional globs refused even inside the workspace; built-in credential-file rules cannot be removed. |
| `security.allowed_env` | The only environment variables an agent may be told about. |
| `agents.<name>.tools` | The grant. An agent can only call what it lists. |
| `agents.<name>.permissions` | Must stay read-only. |
| `sessions.persist_messages`, `persist_tool_calls` | Whether a transcript is written at all. |
| `sessions.retention` | How many sessions stay on disk. |

## Threat model, honestly

- **A prompt injection inside a file** can mislead the subagent's *report*. It
  cannot make PawAgents write, run a command or read outside the workspace, and
  the host is expected to verify findings before acting. This is why the advisory
  rule is stated in the server instructions and in both host skills.
- **A malicious model response** is just data: tool arguments are validated
  against the tool schema before execution, and a refused argument is a tool
  error, not a crash.
- **A compromised provider endpoint** can return false findings but cannot change
  what PawAgents is allowed to read.
- **A local machine with a hostile configuration** is out of scope: whoever can
  write `config.yaml` can widen `allow_outside_workspace`, which is why
  `pagent doctor` reports that setting explicitly.
