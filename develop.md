# PawAgents 实现任务说明

请创建一个 Golang 开源项目：

`pawagents`

项目提供的主命令行为：

`pagent`

PawAgents 是一个面向 Coding Agent 的 **Universal External SubAgent Runtime**。

它的目标不是实现另外一个普通聊天 CLI，也不是简单封装几个 LLM API，而是提供统一的外部子 Agent 执行框架，使 OpenAI Codex、Claude Code，以及未来其他支持 MCP 的 Coding Agent，可以把明确的子任务委派给 PawAgents。

PawAgents 中的 SubAgent 可以使用：

* OpenAI
* Anthropic
* Gemini
* DeepSeek
* Qwen / DashScope
* OpenRouter
* LiteLLM
* AWS Bedrock
* 企业内部 LLM Gateway
* OpenAI-compatible API
* 本地 Ollama 模型
* 未来其他任意模型 Provider

核心目标是：

```text
                      ┌─────────────────┐
                      │   OpenAI Codex  │
                      └────────┬────────┘
                               │
                               │ MCP
                               │
                      ┌────────▼────────┐
                      │                 │
                      │    PawAgents    │
                      │                 │
                      │ pagent runtime  │
                      │                 │
                      └────────▲────────┘
                               │
                               │ MCP
                               │
                     ┌─────────┴─────────┐
                     │    Claude Code    │
                     └───────────────────┘
```

PawAgents 自身再通过统一 Provider 层访问不同模型：

```text
                       PawAgents
                           │
                   Agent Orchestrator
                           │
                       LLM Router
                           │
        ┌──────────────────┼────────────────────┐
        │                  │                    │
        ▼                  ▼                    ▼
     OpenAI            Anthropic             Ollama
        │                  │                    │
        ▼                  ▼                    ▼
     GPT/...            Claude/...       Local Models
                                             │
                           ┌─────────────────┼───────────────┐
                           ▼                 ▼               ▼
                      qwen3-coder        gpt-oss         glm/...
```

PawAgents 必须把：

```text
Host
Agent
Model
Provider
Tool
Session
```

六个概念严格解耦。

---

# 1. 项目定位

PawAgents 应定位为：

> A universal external subagent runtime for coding agents.

典型使用场景：

```text
Codex
   ↓
PawAgents reviewer
   ↓
Claude / Qwen / Ollama
```

或者：

```text
Claude Code
   ↓
PawAgents debugger
   ↓
OpenAI / DeepSeek / Ollama
```

甚至：

```text
Codex
   ↓
PawAgents architect
   ↓
Local Ollama qwen3-coder
```

Host Agent 不需要知道底层到底使用哪个模型。

Host 只需要：

```text
delegate_task(
    agent="reviewer",
    task="review current changes"
)
```

---

# 2. 核心设计原则

以下原则属于项目的强约束。

## 2.1 Host 与 PawAgents 解耦

PawAgents 不允许在核心 Agent Runtime 中依赖：

```text
Codex API
Claude Code API
```

Codex 和 Claude Code 都只是 PawAgents 的客户端。

统一通过：

```text
MCP
```

连接。

架构必须是：

```text
Codex ─────┐
           │
           ├── MCP ── PawAgents
           │
Claude ────┘
```

未来才能继续支持：

```text
Cursor
VS Code Agent
OpenCode
JetBrains Agent
自研 Coding Agent
```

而无需修改 Agent Runtime。

---

# 3. CLI 名称

项目：

```text
pawagents
```

可执行文件：

```text
pagent
```

必须支持：

```bash
pagent version

pagent doctor

pagent config show
pagent config validate

pagent provider list
pagent provider show <provider>
pagent provider test <provider>

pagent model list
pagent model show <model>

pagent agent list
pagent agent show <agent>

pagent run \
    --agent reviewer \
    --task "Review current git changes"

pagent session list
pagent session show <session-id>

pagent mcp serve --stdio
pagent mcp serve --http
```

其中 HTTP MCP 可以第二阶段完成，但接口设计第一版就应预留。

---

# 4. 总体架构

实现以下分层：

```text
┌───────────────────────────────────────────────┐
│                    HOSTS                      │
│                                               │
│       OpenAI Codex          Claude Code       │
└───────────────┬───────────────────┬───────────┘
                │                   │
                └────────MCP────────┘
                            │
                            ▼
┌───────────────────────────────────────────────┐
│               PawAgents MCP Server            │
│                                               │
│ list_agents                                   │
│ get_agent                                     │
│ delegate_task                                 │
│ continue_task                                 │
│ get_task                                      │
│ cancel_task                                   │
└────────────────────────┬──────────────────────┘
                         │
                         ▼
┌───────────────────────────────────────────────┐
│               Agent Orchestrator              │
│                                               │
│ Agent Profile                                 │
│ Prompt Builder                                │
│ Context Manager                               │
│ Agent Loop                                    │
│ Budget Manager                                │
│ Permission Manager                            │
│ Session Manager                               │
└────────────┬───────────────────┬──────────────┘
             │                   │
             ▼                   ▼
┌────────────────────┐  ┌───────────────────────┐
│     LLM Router     │  │     Tool Runtime      │
│                    │  │                       │
│ model alias        │  │ repo.read             │
│ provider routing   │  │ repo.search           │
│ capabilities       │  │ repo.list             │
│ fallback           │  │ git.diff              │
└──────────┬─────────┘  │ git.show              │
           │            │ git.status            │
           ▼            │ git.log               │
┌────────────────────┐  └───────────────────────┘
│ Provider Registry  │
├────────────────────┤
│ OpenAI             │
│ Anthropic          │
│ OpenAI-Compatible  │
│ Ollama Native      │
│ Gemini             │
│ Bedrock            │
└────────────────────┘
```

---

# 5. 推荐代码目录

必须保持模块边界清晰。

建议：

```text
pawagents/
│
├── cmd/
│   └── pagent/
│       └── main.go
│
├── internal/
│
│   ├── agent/
│   │   ├── agent.go
│   │   ├── runner.go
│   │   ├── loop.go
│   │   ├── context.go
│   │   ├── message.go
│   │   ├── result.go
│   │   └── compaction.go
│   │
│   ├── orchestrator/
│   │   ├── orchestrator.go
│   │   ├── delegation.go
│   │   ├── budget.go
│   │   └── routing.go
│   │
│   ├── provider/
│   │   ├── provider.go
│   │   ├── registry.go
│   │   ├── capabilities.go
│   │   │
│   │   ├── openai/
│   │   │   ├── responses.go
│   │   │   └── chat.go
│   │   │
│   │   ├── anthropic/
│   │   │   └── anthropic.go
│   │   │
│   │   ├── ollama/
│   │   │   ├── ollama.go
│   │   │   ├── client.go
│   │   │   └── models.go
│   │   │
│   │   ├── openaicompat/
│   │   │   └── openai_compat.go
│   │   │
│   │   ├── gemini/
│   │   │   └── gemini.go
│   │   └── bedrock/
│   │       └── bedrock.go
│   │
│   ├── tools/
│   │   ├── registry.go
│   │   ├── executor.go
│   │   ├── schema.go
│   │   │
│   │   ├── repo/
│   │   │   ├── read.go
│   │   │   ├── search.go
│   │   │   ├── list.go
│   │   │   └── stat.go
│   │   │
│   │   └── git/
│   │       ├── diff.go
│   │       ├── show.go
│   │       ├── status.go
│   │       └── log.go
│   │
│   ├── mcpserver/
│   │   ├── server.go
│   │   ├── tools.go
│   │   ├── delegate.go
│   │   ├── agents.go
│   │   ├── sessions.go
│   │   └── transport.go
│   │
│   ├── config/
│   │   ├── config.go
│   │   ├── loader.go
│   │   ├── defaults.go
│   │   ├── schema.go
│   │   └── validation.go
│   │
│   ├── profile/
│   │   ├── profile.go
│   │   └── loader.go
│   │
│   ├── session/
│   │   ├── session.go
│   │   ├── store.go
│   │   └── jsonl.go
│   │
│   ├── security/
│   │   ├── workspace.go
│   │   ├── permissions.go
│   │   ├── secrets.go
│   │   └── paths.go
│   │
│   └── telemetry/
│       ├── telemetry.go
│       └── otel.go
│
├── prompts/
│   ├── reviewer.md
│   ├── debugger.md
│   ├── architect.md
│   ├── security.md
│   └── performance.md
│
├── integrations/
│
│   ├── codex/
│   │   ├── .codex-plugin/
│   │   │   └── plugin.json
│   │   ├── skills/
│   │   │   └── pawagents/
│   │   │       └── SKILL.md
│   │   └── README.md
│   │
│   └── claude-code/
│       ├── .mcp.json.example
│       ├── CLAUDE.md.example
│       ├── .claude/
│       │   └── skills/
│       │       └── pawagents/
│       │           └── SKILL.md
│       └── README.md
│
├── examples/
│   ├── config.yaml
│   ├── ollama.yaml
│   ├── codex.yaml
│   └── claude-code.yaml
│
├── docs/
│   ├── architecture.md
│   ├── providers.md
│   ├── agents.md
│   ├── mcp.md
│   ├── security.md
│   └── configuration.md
│
├── tests/
│   ├── agent/
│   ├── providers/
│   ├── tools/
│   ├── mcp/
│   └── integration/
│
├── README.md
├── AGENTS.md
├── LICENSE
├── go.mod
├── go.sum
└── Makefile
```

不要创建巨大单文件实现。

每个模块必须保持职责单一。

---

# 6. Internal LLM Protocol

PawAgents 内部必须定义自己的 LLM Protocol。

禁止让：

```text
openai.ChatCompletionMessage
anthropic.Message
ollama.ChatResponse
```

直接进入 Agent Runtime。

必须统一转换。

例如：

```go
type Provider interface {
    Name() string

    Capabilities(
        ctx context.Context,
        model string,
    ) (ModelCapabilities, error)

    Generate(
        ctx context.Context,
        req *GenerateRequest,
    ) (Stream, error)
}
```

统一请求：

```go
type GenerateRequest struct {
    Model string

    Messages []Message

    Tools []ToolDefinition

    MaxTokens int

    Temperature *float64

    ResponseSchema *JSONSchema

    Extra map[string]any
}
```

统一 Message：

```go
type Message struct {
    Role Role

    Content []ContentPart

    ToolCalls []ToolCall

    ToolResult *ToolResult
}
```

统一流式 Event：

```go
type Event struct {
    Type EventType

    TextDelta string

    ToolCall *ToolCall

    Usage *Usage

    FinishReason string
}
```

---

# 7. Provider 实现

第一阶段至少实现四种 Provider。

## 7.1 OpenAI Responses

支持：

```text
OpenAI Responses API
```

---

## 7.2 Anthropic Messages

支持：

```text
Anthropic Messages API
```

---

## 7.3 OpenAI-Compatible

这是非常重要的 Provider。

允许配置：

```yaml
providers:

  deepseek:
    type: openai-compatible
    base_url: https://api.deepseek.com/v1
    api_key_env: DEEPSEEK_API_KEY

  dashscope:
    type: openai-compatible
    base_url: https://dashscope.aliyuncs.com/compatible-mode/v1
    api_key_env: DASHSCOPE_API_KEY

  openrouter:
    type: openai-compatible
    base_url: https://openrouter.ai/api/v1
    api_key_env: OPENROUTER_API_KEY

  company:
    type: openai-compatible
    base_url: https://llm.example.com/v1
    api_key_env: COMPANY_LLM_API_KEY
```

必须支持：

```text
base_url
api_key
api_key_env
headers
extra_body
timeout
TLS options
proxy
```

---

# 8. Native Ollama Provider

必须将 Ollama 作为一等 Provider 实现，而不是要求用户自己配置 OpenAI-compatible URL。

例如：

```yaml
providers:

  local-ollama:
    type: ollama

    base_url: http://127.0.0.1:11434
```

模型：

```yaml
models:

  local-qwen:
    provider: local-ollama
    model: qwen3-coder

  local-gpt:
    provider: local-ollama
    model: gpt-oss:20b
```

Agent：

```yaml
agents:

  local-reviewer:
    model: local-qwen

    prompt: ~/.pawagents/prompts/reviewer.md

    tools:
      - repo.read
      - repo.search
      - repo.list
      - git.diff
      - git.show

    max_rounds: 20
```

然后：

```bash
pagent run \
    --agent local-reviewer \
    --task "Review current git changes"
```

必须完全不依赖外部云 API。

---

# 9. Ollama Provider 功能

Native Ollama Provider 至少支持：

```text
model discovery
chat
streaming
tool calling
tool results
context configuration
timeout
connection diagnostics
```

例如：

```bash
pagent provider test local-ollama
```

应该检查：

```text
✓ Ollama server reachable

✓ API available

✓ Model qwen3-coder installed

✓ Chat supported

✓ Tool calling supported

✓ Context window configured
```

如果模型不支持 tool calling：

```text
tool_calling = false
```

必须明确返回 capability 信息。

不能假设所有 Ollama 模型都具有相同能力。

---

# 10. Model Capabilities

必须定义：

```go
type ModelCapabilities struct {
    ToolCalling      bool

    ParallelTools    bool

    Streaming        bool

    StructuredOutput bool

    Vision           bool

    Reasoning        bool

    SystemMessage    bool

    MaxContextTokens int
}
```

Agent 执行前必须验证模型能力。

例如 Agent Profile 要求：

```text
repo.read
repo.search
git.diff
```

就意味着：

```text
tool_calling = true
```

如果模型不支持：

```text
return capability error
```

不要伪造：

```text
<tool_call>
...
</tool_call>
```

这种文本工具协议作为默认实现。

---

# 11. Agent Profile

Agent 和模型严格解耦。

配置：

```yaml
agents:

  reviewer:

    description: >
      Perform deep code review and identify
      correctness, concurrency and maintainability issues.

    model: strong-coder

    prompt: ~/.pawagents/prompts/reviewer.md

    tools:
      - repo.read
      - repo.search
      - repo.list
      - git.diff
      - git.show
      - git.status

    max_rounds: 20

    max_tool_calls: 50

    timeout_sec: 300

    permissions:

      filesystem: read

      shell: deny


  debugger:

    model: reasoning-model

    prompt: ~/.pawagents/prompts/debugger.md

    tools:
      - repo.read
      - repo.search
      - git.diff
      - git.log

    max_rounds: 30


  local-reviewer:

    model: local-qwen

    prompt: ~/.pawagents/prompts/reviewer.md

    tools:
      - repo.read
      - repo.search
      - git.diff
```

模型：

```yaml
models:

  strong-coder:

    provider: anthropic

    model: claude-sonnet


  reasoning-model:

    provider: openai

    model: gpt-reasoning


  local-qwen:

    provider: local-ollama

    model: qwen3-coder
```

以后修改：

```yaml
strong-coder:
    provider: local-ollama
    model: qwen3-coder
```

不应该需要修改：

```text
reviewer
```

Agent 定义。

---

# 12. Agent Runtime

实现真正的 Agent Loop。

不能只是：

```text
prompt
  ↓
HTTP
  ↓
answer
```

必须是：

```text
Task
 ↓

Build Context
 ↓

Call Model
 ↓

Tool Call?
 │
 ├── no
 │     ↓
 │   Final
 │
 └── yes
       ↓
 Validate Tool
       ↓
 Permission Check
       ↓
 Execute
       ↓
 Tool Result
       ↓
 Append Context
       ↓
 Call Model Again
```

基本逻辑：

```go
for round := 0; round < maxRounds; round++ {

    response, err := model.Generate(...)

    if err != nil {
        return err
    }

    if !response.HasToolCalls() {
        return finalResult(response)
    }

    for _, call := range response.ToolCalls {

        if !registry.IsAllowed(call.Name) {
            return ErrToolDenied
        }

        result, err := registry.Execute(
            ctx,
            call,
        )

        messages = append(
            messages,
            ToolResultMessage(result),
        )
    }
}
```

---

# 13. Agent Budget

每个 Agent 必须支持：

```text
max_rounds
max_tool_calls
max_input_tokens
max_output_tokens
max_context_tokens
max_tool_output_bytes
timeout
```

防止：

```text
无限 tool loop
超大文件
无限 search
无限 token
```

---

# 14. Tool Runtime

MVP 内置：

```text
repo.read
repo.list
repo.search
repo.stat

git.diff
git.show
git.status
git.log
```

第一版 External Agent 必须默认为：

```text
READ ONLY
```

不允许默认提供：

```text
rm
mv
git commit
git push
sudo
ssh
curl
arbitrary shell
```

设计原则：

```text
PawAgents SubAgent
        │
        ▼
     Analyze
        │
        ▼
Recommendation / Patch Suggestion
        │
        ▼
Host Agent
        │
        ▼
Codex / Claude Code modifies files
```

即：

```text
PawAgents = Advisor

Host Agent = Executor
```

---

# 15. Workspace Security

所有 repo tool 必须经过 Workspace Guard。

禁止：

```text
../../../etc/passwd
```

必须防止：

```text
path traversal
symlink escape
absolute-path escape
workspace bypass
```

同时：

```text
API KEY
TOKEN
PASSWORD
SECRET
```

不得自动加入模型上下文。

环境变量默认不可见。

只有显式允许的变量才可以传给 Provider。

---

# 16. Context Manager

不要由 Host 把整个 Repository 输入给 PawAgents。

应该：

```text
Codex / Claude Code

       ↓

task
background
selected files
constraints

       ↓

PawAgents

       ↓

repo.search
repo.read
git.diff

       ↓

模型动态查找需要的内容
```

这能减少：

```text
token usage
context pollution
unnecessary code exposure
```

---

# 17. Context Compaction

必须为 Context Manager 预留 Compaction。

例如达到：

```text
70% context window
```

后：

```text
Old Tool Results
       ↓
Summarize
       ↓
Compact Context
```

第一版允许采用简单实现：

```text
discard/reduce old large tool output

+

keep structured summary
```

但是接口设计必须允许后续升级。

---

# 18. MCP Server

使用官方 Go MCP SDK。

MCP Server：

```bash
pagent mcp serve --stdio
```

至少暴露：

```text
list_agents
get_agent
delegate_task
```

推荐同时设计：

```text
continue_task
get_task
cancel_task
```

其中后三个允许第二阶段实现。

---

# 19. list_agents

示例：

```json
[
  {
    "name": "reviewer",
    "description": "Deep source-code review",
    "model": "strong-coder"
  },
  {
    "name": "local-reviewer",
    "description": "Local code review using Ollama",
    "model": "local-qwen"
  },
  {
    "name": "architect",
    "description": "Architecture analysis",
    "model": "reasoning-model"
  }
]
```

---

# 20. delegate_task Contract

不能简单设计成：

```json
{
  "prompt": "..."
}
```

必须设计成明确的 Delegation Contract。

例如：

```json
{
  "agent": "reviewer",

  "task":
    "Review the current OVS changes for concurrency problems.",

  "workspace":
    "/home/user/openvswitch",

  "background":
    "The patch changes PMD scheduling behavior.",

  "files": [
    "lib/dpif-netdev.c",
    "lib/netdev-dpdk.c"
  ],

  "constraints": [
    "Do not modify files",
    "Only report evidence-backed findings",
    "Include file and line numbers"
  ],

  "output_mode":
    "structured",

  "budget": {
    "max_rounds": 20,
    "timeout_sec": 300
  }
}
```

---

# 21. Structured Result

返回不能只有自然语言。

至少：

```json
{
  "status": "completed",

  "agent": "reviewer",

  "provider": "ollama",

  "model": "qwen3-coder",

  "summary": "...",

  "findings": [
    {
      "severity": "high",

      "category": "concurrency",

      "file": "lib/dpif-netdev.c",

      "line": 3184,

      "title": "...",

      "description": "...",

      "evidence": "...",

      "suggestion": "...",

      "confidence": 0.91
    }
  ],

  "usage": {
    "input_tokens": 14325,
    "output_tokens": 3120,
    "tool_calls": 17,
    "rounds": 8
  },

  "session_id": "agt_xxxxxx"
}
```

不同 Agent 可以有不同业务输出，但外围 Envelope 必须统一。

---

# 22. OpenAI Codex 集成

PawAgents 必须提供 Codex 集成。

开发模式：

```bash
codex mcp add pawagents -- \
    pagent mcp serve --stdio
```

然后 Codex 可以调用：

```text
pawagents.list_agents

pawagents.delegate_task
```

---

# 23. Codex Skill

提供：

```text
integrations/codex/
```

至少包含：

```text
.codex-plugin/plugin.json

skills/pawagents/SKILL.md
```

Skill 应告诉 Codex：

当用户需要：

```text
independent code review
debugging
architecture analysis
security analysis
performance analysis
second-model opinion
cross-model verification
```

时，可以调用 PawAgents。

例如用户说：

```text
让另一个模型分析一下这个 bug。
```

Codex 可以调用：

```text
pawagents.delegate_task
```

Skill 同时必须要求：

```text
PawAgents result is advisory.

Verify critical findings before modifying files.
```

---

# 24. Claude Code 集成

PawAgents 还必须作为 Claude Code 的外部 SubAgent Runtime 使用。

连接方式：

```bash
claude mcp add pawagents -- \
    pagent mcp serve --stdio
```

也需要提供项目配置示例：

```text
integrations/claude-code/.mcp.json.example
```

例如描述：

```json
{
  "mcpServers": {
    "pawagents": {
      "type": "stdio",
      "command": "pagent",
      "args": [
        "mcp",
        "serve",
        "--stdio"
      ]
    }
  }
}
```

实际 schema 必须按照当前 Claude Code MCP 格式实现和测试。

---

# 25. Claude Code Skill

创建：

```text
integrations/claude-code/.claude/skills/pawagents/SKILL.md
```

其作用类似 Codex Skill。

Claude Code 用户应该能够表达：

```text
Use PawAgents reviewer to independently
review my current changes.
```

或者通过 Skill：

```text
/pawagents
```

调用对应工作流。

Skill 应说明：

```text
Use PawAgents when an independent model,
different provider, or specialized external
reviewer would improve the task.

Do not delegate trivial operations.

Give the external agent a bounded task.

Verify important findings yourself.
```

---

# 26. Codex / Claude Code 的行为应保持一致

必须保证：

```text
Codex → PawAgents
```

和：

```text
Claude Code → PawAgents
```

获得同一 MCP Contract。

禁止写：

```text
CodexDelegateRequest
ClaudeDelegateRequest
```

两个不同核心协议。

正确实现：

```text
Host
 │
 ▼
MCP
 │
 ▼
DelegateRequest
```

Host-specific 逻辑只允许存在：

```text
integrations/
```

而不能污染：

```text
internal/agent/
internal/provider/
```

---

# 27. Configuration

默认配置：

```text
~/.pawagents/config.yaml
```

允许通过环境变量覆盖。

示例：

```yaml
version: 1


providers:

  openai:

    type: openai-responses

    base_url: https://api.openai.com/v1

    api_key_env: OPENAI_API_KEY


  anthropic:

    type: anthropic

    base_url: https://api.anthropic.com

    api_key_env: ANTHROPIC_API_KEY


  deepseek:

    type: openai-compatible

    base_url: https://api.deepseek.com/v1

    api_key_env: DEEPSEEK_API_KEY


  local-ollama:

    type: ollama

    base_url: http://127.0.0.1:11434


models:

  cloud-reviewer:

    provider: anthropic

    model: claude-sonnet


  reasoning:

    provider: openai

    model: gpt-reasoning


  cheap-coder:

    provider: deepseek

    model: deepseek-chat


  local-coder:

    provider: local-ollama

    model: qwen3-coder


agents:

  reviewer:

    model: cloud-reviewer

    prompt: ~/.pawagents/prompts/reviewer.md

    tools:

      - repo.read
      - repo.search
      - repo.list
      - git.diff
      - git.show

    max_rounds: 20

    timeout_sec: 300


  local-reviewer:

    model: local-coder

    prompt: ~/.pawagents/prompts/reviewer.md

    tools:

      - repo.read
      - repo.search
      - git.diff

    max_rounds: 20

    timeout_sec: 600


security:

  allow_outside_workspace: false

  max_file_size: 1048576

  redact_env:

    - "*_TOKEN"
    - "*_KEY"
    - "*_SECRET"
    - "*_PASSWORD"


sessions:

  directory: ~/.pawagents/sessions

  persist_messages: true

  persist_tool_calls: true


telemetry:

  enabled: false
```

---

# 28. Session

每一次：

```text
delegate_task
```

必须生成：

```text
session_id
```

默认存储：

```text
~/.pawagents/sessions/<session-id>/
```

结构：

```text
metadata.json

messages.jsonl

tools.jsonl

result.json
```

metadata 至少：

```json
{
  "session_id": "agt_xxx",
  "agent": "reviewer",
  "provider": "ollama",
  "model": "qwen3-coder",
  "status": "completed",
  "started_at": "...",
  "finished_at": "..."
}
```

这样未来可支持：

```text
continue_task
```

例如：

```text
继续问刚才那个 reviewer：

你为什么认为这里存在 race condition？
```

---

# 29. Future Multi-Agent

MVP 不实现复杂 Nested Agent。

第一阶段：

```text
Host
 ↓
PawAgents
 ↓
One Agent
 ↓
One Model
```

第二阶段可以支持：

```text
Host
       ↓
PawAgents Orchestrator
       │
 ┌─────┼─────┐
 ▼     ▼     ▼
Agent Agent Agent
 │     │     │
Claude Qwen Ollama
```

例如：

```yaml
agents:

  review-team:

    type: team

    strategy: parallel

    members:

      - correctness-reviewer

      - security-reviewer

      - performance-reviewer
```

但不能为了未来功能破坏 MVP 简洁性。

---

# 30. Provider Fallback

接口预留：

```yaml
models:

  strong-coder:

    primary:

      provider: anthropic

      model: claude-sonnet

    fallback:

      - provider: openai
        model: gpt-codex

      - provider: local-ollama
        model: qwen3-coder
```

第一版可只做：

```text
one model → one provider
```

但数据结构不能阻止后续实现 fallback。

---

# 31. Observability

至少记录：

```text
session_id
agent
provider
model
duration
rounds
tool calls
input tokens
output tokens
errors
```

日志不得包含：

```text
API keys
authorization headers
passwords
secrets
```

未来兼容：

```text
OpenTelemetry
```

但 MVP 不要求部署外部 telemetry backend。

---

# 32. README.md 属于强制交付物

项目实现完成时，必须生成一份**完整、可直接发布到 GitHub 的 `README.md`**。

README 不是简单几段说明，而必须成为 PawAgents 的完整入门文档。

至少包含以下章节。

## README：Project Introduction

说明：

```text
What is PawAgents?

Why PawAgents?

What problem does it solve?
```

明确说明：

```text
PawAgents is not another coding agent.

PawAgents is an external subagent runtime
that existing coding agents can delegate work to.
```

---

## README：Architecture

必须有 ASCII 架构图。

例如：

```text
          OpenAI Codex
               │
               │ MCP
               ▼
        ┌──────────────┐
        │              │
        │  PawAgents   │
        │              │
        └──────┬───────┘
               │
               │
       ┌───────┼─────────┐
       ▼       ▼         ▼
   Anthropic OpenAI    Ollama
                         │
                         ▼
                    Local Model
```

同时解释：

```text
Host
MCP
Agent
Provider
Model
Tool Runtime
```

分别是什么。

---

## README：Features

至少包含：

```text
Universal SubAgent Runtime

Codex Integration

Claude Code Integration

Multi-provider support

Native Ollama support

OpenAI-compatible APIs

Agent Profiles

Read-only repository tools

Session persistence

Structured results

Model capability detection

Extensible Provider API
```

---

## README：Installation

包含：

```bash
git clone ...

cd pawagents

go build -o pagent ./cmd/pagent

sudo install pagent /usr/local/bin/pagent
```

以及：

```bash
pagent version
```

---

## README：Quick Start

从零配置一个 Agent。

例如：

```yaml
providers:

  local:
    type: ollama
    base_url: http://127.0.0.1:11434

models:

  coder:
    provider: local
    model: qwen3-coder

agents:

  reviewer:
    model: coder
    tools:
      - repo.read
      - repo.search
      - git.diff
```

然后：

```bash
pagent run \
    --agent reviewer \
    --task "Review current changes"
```

---

## README：Using Ollama

必须单独有完整章节。

至少包含：

```bash
ollama pull qwen3-coder
```

检查模型：

```bash
ollama list
```

然后配置 PawAgents。

展示：

```bash
pagent provider test local-ollama
```

以及：

```bash
pagent run \
    --agent local-reviewer \
    --task "Review my current git changes"
```

说明：

```text
No cloud model API is required.
```

---

## README：Using with Codex

完整展示：

```bash
codex mcp add pawagents -- \
    pagent mcp serve --stdio
```

然后验证 MCP。

再给出使用案例：

```text
Ask the PawAgents reviewer to independently
review my current changes.

Then verify whether its findings are correct.
```

---

## README：Using with Claude Code

完整展示：

```bash
claude mcp add pawagents -- \
    pagent mcp serve --stdio
```

给出 `.mcp.json` 项目配置示例。

然后给出：

```text
Use the PawAgents reviewer to independently
analyze the current patch.
```

以及 Skill 使用方式。

---

## README：Providers

建立表格：

```text
Provider             Status

OpenAI Responses     Supported
Anthropic            Supported
OpenAI Compatible    Supported
Ollama               Supported
Gemini               Planned
Bedrock              Planned
```

状态必须与实际实现保持一致。

不能 README 说 Supported，而代码没有实现。

---

## README：Configuration

完整解释：

```text
providers
models
agents
tools
security
sessions
telemetry
```

给出完整 YAML。

---

## README：Creating an Agent

示例：

```yaml
agents:

  ovs-reviewer:

    description:
      Review Open vSwitch networking code.

    model:
      strong-coder

    tools:

      - repo.read
      - repo.search
      - git.diff

    max_rounds:
      30
```

---

## README：Creating a Provider

介绍：

```go
type Provider interface
```

说明第三方开发者如何扩展新的 Provider。

---

## README：Security Model

必须说明：

```text
External agents are read-only by default.

Workspace escape is forbidden.

Secrets are not exposed to models.

Shell access is disabled by default.

Host agents remain responsible for applying changes.
```

---

## README：Development

包含：

```bash
go test ./...

go vet ./...

go build ./...
```

---

## README：Testing

说明：

```text
unit tests

provider contract tests

agent tests

security tests

MCP integration tests
```

---

## README：Roadmap

例如：

```text
0.1

Core Runtime
OpenAI Compatible
Anthropic
Ollama
Codex
Claude Code


0.2

Session continuation
Provider fallback
HTTP MCP


0.3

Parallel agent teams
Routing
OpenTelemetry


1.0

Stable Provider API
Stable MCP Contract
Plugin ecosystem
```

---

# 33. Additional Documentation

README 之外必须至少提供：

```text
docs/architecture.md

docs/configuration.md

docs/providers.md

docs/agents.md

docs/mcp.md

docs/security.md
```

README 面向第一次接触项目的用户。

docs 面向需要深入使用和开发 PawAgents 的用户。

---

# 34. Testing

所有 Provider 必须运行统一 Contract Test。

## Provider Tests

```text
completion

streaming

tool_call

multiple_tool_calls

tool_result

unicode

timeout

cancellation

429

500

invalid response

large context
```

---

# 35. Ollama Tests

额外：

```text
ollama_not_running

ollama_model_missing

ollama_model_available

ollama_chat

ollama_stream

ollama_tool_call

ollama_tool_result

ollama_without_tool_support

ollama_timeout
```

Integration Tests 应允许通过环境变量：

```text
PAWAGENTS_TEST_OLLAMA=1
```

决定是否执行真实 Ollama 测试。

CI 默认不能因为机器没有 Ollama 而失败。

---

# 36. Agent Tests

```text
direct_final_response

single_tool_call

multiple_tool_rounds

parallel_tool_calls

tool_error

tool_denied

max_rounds

max_tool_calls

timeout

cancellation

context_compaction
```

---

# 37. Security Tests

必须包括：

```text
../ path traversal

absolute path escape

symlink escape

outside workspace

large file protection

secret redaction

environment leakage
```

---

# 38. MCP Tests

至少：

```text
initialize

tool discovery

list_agents

get_agent

delegate_task

invalid agent

invalid workspace

timeout

structured result
```

---

# 39. Codex Integration Test

需要至少提供人工或自动验证步骤。

例如：

```bash
codex mcp add pawagents -- \
    ./pagent mcp serve --stdio
```

然后确认：

```text
list_agents
```

和：

```text
delegate_task
```

可以工作。

---

# 40. Claude Code Integration Test

验证：

```bash
claude mcp add pawagents -- \
    ./pagent mcp serve --stdio
```

Claude Code 能发现：

```text
list_agents
delegate_task
```

并完成一次真实 Agent 任务。

---

# 41. Build Commands

Makefile 至少：

```text
make build

make test

make vet

make fmt

make lint

make install

make integration-test
```

输出：

```text
bin/pagent
```

---

# 42. Doctor Command

实现：

```bash
pagent doctor
```

输出类似：

```text
PawAgents diagnostics

Config
  ✓ ~/.pawagents/config.yaml

Providers
  ✓ local-ollama
  ✓ anthropic
  ✗ openai: OPENAI_API_KEY not set

Ollama
  ✓ http://127.0.0.1:11434
  ✓ qwen3-coder

Agents
  ✓ reviewer
  ✓ local-reviewer

Workspace
  ✓ valid Git repository

MCP
  ✓ stdio transport available
```

这对排查 Codex / Claude Code / Ollama 集成问题非常重要。

---

# 43. Error Model

不要到处返回字符串 error。

定义错误类别：

```text
ConfigError

ProviderError

AuthenticationError

CapabilityError

AgentError

ToolError

PermissionError

WorkspaceError

BudgetExceededError

TimeoutError

CancelledError
```

MCP 返回错误时应保持：

```text
machine-readable error code

+

human-readable message
```

---

# 44. Implementation Order

不要一次实现所有功能。

严格按照以下阶段推进。

## Phase 1

创建：

```text
project skeleton
CLI
logging
config
README skeleton
```

必须能够：

```bash
pagent version
pagent config validate
```

---

## Phase 2

实现 Internal LLM Protocol：

```text
Message
ContentPart
ToolCall
ToolResult
GenerateRequest
Event
Usage
Capabilities
```

---

## Phase 3

实现：

```text
OpenAI-Compatible Provider
```

---

## Phase 4

实现：

```text
Native Ollama Provider
```

首先让：

```bash
pagent provider test local-ollama
```

工作。

---

## Phase 5

实现 Repository Tools：

```text
repo.read
repo.search
repo.list

git.diff
git.show
git.status
git.log
```

---

## Phase 6

实现 Agent Loop。

完成：

```bash
pagent run \
    --agent local-reviewer \
    --task "Review current changes"
```

且可以完全使用本机 Ollama 完成。

这是第一个重要里程碑。

---

## Phase 7

实现：

```text
Agent Profiles
Model Registry
Capability Validation
```

---

## Phase 8

实现：

```text
Anthropic Provider

OpenAI Responses Provider
```

---

## Phase 9

实现：

```text
Session persistence
Structured Result
Context compaction
```

---

## Phase 10

实现 MCP Server：

```text
list_agents

get_agent

delegate_task
```

---

## Phase 11

实现 Codex Integration：

```text
Codex MCP configuration

Codex Skill

Codex Plugin manifest
```

---

## Phase 12

实现 Claude Code Integration：

```text
Claude MCP configuration

.mcp.json example

Claude Skill
```

---

## Phase 13

补齐 README。

README 中所有命令必须经过实际验证。

不允许 README 出现没有实现的命令。

---

## Phase 14

Integration Tests。

验证：

```text
Codex
   ↓
PawAgents
   ↓
Ollama
```

以及：

```text
Claude Code
   ↓
PawAgents
   ↓
Ollama
```

---

# 45. 每阶段质量要求

每完成一个 Phase：

```bash
gofmt

go vet ./...

go test ./...

go build ./...
```

必须保持：

```text
main branch buildable
```

禁止：

```text
先写几万行再统一修编译错误
```

---

# 46. MVP Acceptance Test

项目 MVP 的最终验收流程如下。

首先安装并启动 Ollama。

例如准备：

```text
qwen3-coder
```

然后：

```bash
pagent provider test local-ollama
```

必须成功。

接着进入任意 Git Repository：

```bash
pagent run \
    --agent local-reviewer \
    --task "Review my current git changes"
```

Agent 必须能够自行调用：

```text
git.diff

repo.read

repo.search
```

并输出 Structured Result。

---

然后测试 Codex：

```bash
codex mcp add pawagents -- \
    pagent mcp serve --stdio
```

在 Codex 中：

```text
让 PawAgents 的 local-reviewer
使用本地模型 review 当前修改。

然后你自己检查它报告的问题是否真实。
```

必须工作。

---

然后测试 Claude Code：

```bash
claude mcp add pawagents -- \
    pagent mcp serve --stdio
```

在 Claude Code 中：

```text
Use PawAgents local-reviewer to independently
review my current changes and then verify
its findings yourself.
```

必须工作。

---

# 47. MVP 最重要的完整链路

项目第一阶段最终必须打通：

```text
                         Codex
                           │
                           ▼
                          MCP
                           │
                           ▼
                     ┌──────────┐
                     │ PawAgents│
                     └────┬─────┘
                          │
                          ▼
                       Ollama
                          │
                          ▼
                    qwen3-coder
```

以及：

```text
                      Claude Code
                           │
                           ▼
                          MCP
                           │
                           ▼
                     ┌──────────┐
                     │ PawAgents│
                     └────┬─────┘
                          │
                          ▼
                       Ollama
                          │
                          ▼
                    qwen3-coder
```

这两条链路优先级高于加入更多云模型。

---

# 48. 非目标

MVP 不要做：

```text
Web UI

distributed scheduler

Kubernetes deployment

complex workflow DSL

nested agents

vector database

RAG platform

browser automation

arbitrary shell

automatic code modification
```

避免项目早期过度复杂。

---

# 49. 核心成功标准

PawAgents 成功的标准不是：

> 能请求一个 LLM API。

而是：

> 任意 Coding Agent 可以通过标准 MCP，把一个有边界的任务委派给一个使用不同模型、不同 Provider，甚至完全本地模型运行的独立 Agent。

最终：

```text
Host Agent
     │
     │ delegate
     ▼
PawAgents
     │
     ├── Claude
     ├── GPT
     ├── Gemini
     ├── Qwen
     ├── DeepSeek
     └── Local Ollama
```

而 Host 不需要知道 Provider API 的任何实现细节。

---

# 50. 最终交付物

任务完成时必须交付：

```text
1. 完整可编译 Golang 源码

2. pagent CLI

3. Agent Runtime

4. Provider abstraction

5. OpenAI-compatible Provider

6. Anthropic Provider

7. OpenAI Responses Provider

8. Native Ollama Provider

9. Read-only repository tools

10. MCP Server

11. Codex integration

12. Claude Code integration

13. Codex Skill

14. Claude Code Skill

15. Session subsystem

16. Security subsystem

17. Unit Tests

18. Provider Contract Tests

19. MCP Integration Tests

20. Ollama Integration Tests

21. 完整 README.md

22. docs/ 下的详细设计文档

23. examples/ 下的可运行配置
```

最终用户应能够仅阅读 README，就完成：

```text
安装 PawAgents

→ 配置 Ollama

→ 创建 Agent

→ 本地执行 Agent

→ 接入 Codex

→ 接入 Claude Code

→ 调用 PawAgents SubAgent
```

而不需要阅读源代码才能完成基本配置。
