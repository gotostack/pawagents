// Copyright (c) 2026 PawAgents Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package mcpserver exposes the PawAgents runtime over the Model Context
// Protocol.
//
// It is the only place in the runtime that knows about hosts, and it knows
// nothing about any particular one: Codex and Claude Code connect to the same
// server, call the same three tools and receive the same schema. Everything
// below the server is host independent, which is what lets a second host be
// added without touching the agent loop.
//
// The package depends on the runtime through an interface it defines itself
// (Delegator), so the tools can be tested without a provider, an endpoint or a
// model.
package mcpserver

import (
	"context"
	"log/slog"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pawagents/pawagents/internal/agent"
	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/orchestrator"
	"github.com/pawagents/pawagents/internal/session"
)

// Tool names. They are part of the public contract: a host names them in its
// own configuration and a skill refers to them by name.
const (
	// ToolListAgents lists the agent profiles a host may delegate to.
	ToolListAgents = "list_agents"
	// ToolGetAgent describes one agent, including whether it can run.
	ToolGetAgent = "get_agent"
	// ToolDelegateTask runs one task with one agent.
	ToolDelegateTask = "delegate_task"
)

// Tools lists the tool names this server exposes, in registration order.
func Tools() []string {
	return []string{ToolListAgents, ToolGetAgent, ToolDelegateTask}
}

// Delegator is the part of the runtime the MCP tools use.
//
// It is an interface so that the tools can be exercised without a provider,
// and so that the server cannot reach into the runtime beyond what it needs.
// Adding a capability to the server therefore requires extending this
// interface, which keeps the host facing surface explicit.
type Delegator interface {
	// Run executes one delegated task.
	Run(ctx context.Context, request orchestrator.Request) (*agent.Result, error)
	// Profiles lists every configured agent.
	Profiles() []orchestrator.AgentProfile
	// Profile describes one configured agent.
	Profile(name string) (orchestrator.AgentProfile, error)
	// CheckAgent reports whether an agent can run, without contacting the
	// provider unless probe is set.
	CheckAgent(ctx context.Context, name string, probe bool) (orchestrator.CapabilityReport, error)
	// Sessions returns the session store, which is where a delegation records
	// its transcript.
	Sessions() *session.Store
}

// Options configures the server.
type Options struct {
	// Runtime is the delegation entry point. It is required.
	Runtime Delegator
	// Config is the loaded configuration, used for the delegable defaults of a
	// request (output mode and budget limits).
	Config *config.Config
	// Logger receives server diagnostics. It must write to standard error:
	// standard output carries the protocol.
	Logger *slog.Logger
	// Version is the build version reported in server metadata.
	Version string
	// Instructions are sent to a client that initializes a session. They are
	// the only place a host is told what this server is for.
	Instructions string
}

// Server is the MCP server.
type Server struct {
	server  *mcp.Server
	runtime Delegator
	cfg     *config.Config
	logger  *slog.Logger
}

// New builds the server and registers its tools.
//
// Registration is offline and cheap: listing an agent never contacts a
// provider, so a host can discover what is available before deciding whether a
// task is worth delegating.
func New(options Options) (*Server, error) {
	if options.Runtime == nil {
		return nil, apperrors.New(apperrors.KindInternal, "mcpserver.new",
			"the MCP server needs the delegation runtime")
	}
	if options.Config == nil {
		return nil, apperrors.New(apperrors.KindInternal, "mcpserver.new",
			"the MCP server needs the configuration")
	}

	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}

	instructions := strings.TrimSpace(options.Instructions)
	if instructions == "" {
		instructions = defaultInstructions
	}

	server := &Server{
		server: mcp.NewServer(&mcp.Implementation{
			Name:       "pawagents",
			Title:      "PawAgents",
			Version:    versionOrUnknown(options.Version),
			WebsiteURL: "https://github.com/pawagents/pawagents",
		}, &mcp.ServerOptions{
			Instructions: instructions,
			Logger:       logger,
		}),
		runtime: options.Runtime,
		cfg:     options.Config,
		logger:  logger,
	}

	server.registerTools()

	return server, nil
}

// MCP returns the underlying SDK server, which is what a transport connects to.
func (s *Server) MCP() *mcp.Server { return s.server }

// Run serves the protocol until the client disconnects or ctx is cancelled.
func (s *Server) Run(ctx context.Context, transport mcp.Transport) error {
	if transport == nil {
		return apperrors.New(apperrors.KindInternal, "mcpserver.run",
			"the MCP server needs a transport")
	}

	s.logger.Info("serving MCP on stdio")
	if err := s.server.Run(ctx, transport); err != nil {
		// A closed connection is how a host normally ends the session, so it is
		// reported as cancellation rather than as a server failure.
		if ctx.Err() != nil {
			return apperrors.Wrap(apperrors.KindCancelled, "mcpserver.run",
				"the MCP session was interrupted", err)
		}
		return apperrors.Wrap(apperrors.KindInternal, "mcpserver.run",
			"the MCP session ended with an error", err)
	}
	return nil
}

// defaultInstructions is sent to a client when it initializes a session.
//
// It states the advisory rule explicitly, because a host that reads it early is
// less likely to treat a finding as an applied change.
const defaultInstructions = `PawAgents runs an independent read-only subagent on a task you
delegate, typically using a different model or provider than you.

Use list_agents to see what is available, get_agent before delegating if you need
to know whether an agent can run, and delegate_task to run one bounded task.

The result is advisory. Verify critical findings before modifying files:
PawAgents never writes to the workspace and never runs a shell command.

Give the agent a bounded question, and start a new delegation for a new question
rather than continuing a long one.`

// versionOrUnknown keeps the MCP metadata valid when no version was injected at
// build time.
func versionOrUnknown(version string) string {
	trimmed := strings.TrimSpace(version)
	if trimmed == "" {
		return "devel"
	}
	return trimmed
}
