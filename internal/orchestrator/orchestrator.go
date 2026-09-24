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

// Package orchestrator turns a delegation request into a running agent.
//
// It sits between the hosts and the agent loop: the CLI and, later, the MCP
// server describe *what* they want in a Request, and the orchestrator resolves
// the agent profile, the model alias, the provider instance, the capability set,
// the tool grant and the budget before handing a fully configured runner to the
// agent package. Everything that can be validated without spending a token is
// validated here.
package orchestrator

import (
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/provider"
	"github.com/pawagents/pawagents/internal/security"
	"github.com/pawagents/pawagents/internal/session"
)

// Orchestrator runs agents against a loaded configuration.
type Orchestrator struct {
	cfg      *config.Config
	registry *provider.Registry
	sessions *session.Store
	logger   *slog.Logger
}

// New builds an orchestrator.
func New(cfg *config.Config, registry *provider.Registry, logger *slog.Logger) (*Orchestrator, error) {
	if cfg == nil {
		return nil, apperrors.New(apperrors.KindInternal, "orchestrator.new",
			"the orchestrator needs a configuration")
	}
	if registry == nil {
		return nil, apperrors.New(apperrors.KindInternal, "orchestrator.new",
			"the orchestrator needs a provider registry")
	}
	if logger == nil {
		logger = slog.Default()
	}

	// The store is built here so that a misconfigured sessions directory is
	// reported once, but it touches no disk until a task is recorded: a command
	// that never delegates anything leaves no trace.
	store, err := NewSessionStore(cfg, logger)
	if err != nil {
		return nil, err
	}

	return &Orchestrator{cfg: cfg, registry: registry, sessions: store, logger: logger}, nil
}

// NewSessionStore builds the session store described by the configuration.
//
// It is exported so that the read-only session commands can inspect recorded
// sessions without building a provider registry, and so that the mapping from
// configuration to store options lives in exactly one place.
func NewSessionStore(cfg *config.Config, logger *slog.Logger) (*session.Store, error) {
	if cfg == nil {
		return nil, apperrors.New(apperrors.KindInternal, "orchestrator.new_session_store",
			"the session store needs a configuration")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return session.NewStore(session.Options{
		Directory:        cfg.Sessions.Directory,
		PersistMessages:  cfg.Sessions.ShouldPersistMessages(),
		PersistToolCalls: cfg.Sessions.ShouldPersistToolCalls(),
		Retention:        cfg.Sessions.Retention,
		Logger:           logger,
	})
}

// Sessions returns the session store.
func (o *Orchestrator) Sessions() *session.Store { return o.sessions }

// Config returns the configuration the orchestrator was built from.
func (o *Orchestrator) Config() *config.Config { return o.cfg }

// Request is one delegated task.
//
// The fields mirror what a host agent knows and what the specification
// documents for the delegate_task tool: what to do, why, where to look, what
// rules apply, and which bounds to enforce.
type Request struct {
	// Agent is the configured agent name.
	Agent string
	// Task is the instruction for the agent.
	Task string
	// Background is context the host cannot expect the agent to discover.
	Background string
	// Files are starting points for the investigation.
	Files []string
	// Constraints are rules the host imposes on the answer.
	Constraints []string
	// Workspace is the directory the agent may read. An empty value falls back
	// to security.workspace_root and then to the current directory.
	Workspace string
	// MaxRounds overrides the agent round budget.
	MaxRounds int
	// Timeout overrides the agent timeout.
	Timeout time.Duration
	// OutputMode overrides the configured output mode.
	OutputMode string
}

// Validate checks the request can be dispatched.
func (r Request) Validate() error {
	var problems []error

	if strings.TrimSpace(r.Agent) == "" {
		problems = append(problems, apperrors.New(apperrors.KindInvalidArgument, "orchestrator.request",
			"an agent name is required"))
	}
	if strings.TrimSpace(r.Task) == "" {
		problems = append(problems, apperrors.New(apperrors.KindInvalidArgument, "orchestrator.request",
			"a task is required"))
	}
	if r.MaxRounds < 0 {
		problems = append(problems, apperrors.New(apperrors.KindInvalidArgument, "orchestrator.request",
			"max_rounds must not be negative"))
	}
	if r.Timeout < 0 {
		problems = append(problems, apperrors.New(apperrors.KindInvalidArgument, "orchestrator.request",
			"timeout must not be negative"))
	}
	switch r.OutputMode {
	case "", config.OutputModeText, config.OutputModeStructured:
	default:
		problems = append(problems, apperrors.New(apperrors.KindInvalidArgument, "orchestrator.request",
			"unsupported output mode %q (want text or structured)", r.OutputMode))
	}

	return apperrors.Multi(apperrors.KindInvalidArgument, "orchestrator.request",
		"invalid delegation request", problems...)
}

// AgentSummary describes one configured agent without contacting a provider.
//
// It is what a host agent needs to decide whether to delegate: the name, what
// the agent is for, which model it uses, which tools it may call and how long
// it may run.
type AgentSummary struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Type        string   `json:"type,omitempty"`
	ModelAlias  string   `json:"model"`
	Provider    string   `json:"provider,omitempty"`
	Model       string   `json:"model_name,omitempty"`
	Tools       []string `json:"tools,omitempty"`
	OutputMode  string   `json:"output_mode"`
	MaxRounds   int      `json:"max_rounds"`
	Timeout     string   `json:"timeout,omitempty"`
}

// Agents lists every configured agent, sorted by name.
func (o *Orchestrator) Agents() []AgentSummary {
	names := o.cfg.AgentNames()
	out := make([]AgentSummary, 0, len(names))
	for _, name := range names {
		summary, err := o.Agent(name)
		if err != nil {
			continue
		}
		out = append(out, summary)
	}
	return out
}

// Agent describes one configured agent.
func (o *Orchestrator) Agent(name string) (AgentSummary, error) {
	trimmed := strings.TrimSpace(name)
	agentCfg, ok := o.cfg.Agents[trimmed]
	if !ok || agentCfg == nil {
		return AgentSummary{}, apperrors.New(apperrors.KindNotFound, "orchestrator.agent",
			"agent %q is not defined in the configuration", trimmed).
			WithDetails(map[string]any{"available": o.cfg.AgentNames()})
	}

	summary := AgentSummary{
		Name:        trimmed,
		Description: agentCfg.Description,
		Type:        agentCfg.Type,
		ModelAlias:  agentCfg.Model,
		Tools:       agentCfg.Tools,
		OutputMode:  outputModeOf(agentCfg, ""),
		MaxRounds:   agentCfg.MaxRounds,
	}

	// The first target is reported because it is what a run would reach for
	// first; a fallback is a recovery path, not an alternative plan.
	if targets, _, err := o.cfg.ModelTargets(agentCfg.Model); err == nil && len(targets) > 0 {
		summary.Provider = targets[0].Provider
		summary.Model = targets[0].Model
	}

	if timeout := agentCfg.Timeout.Duration(); timeout > 0 {
		summary.Timeout = timeout.String()
	}
	return summary, nil
}

// workspaceFor resolves the directory an agent may read and builds the guard.
//
// The precedence is the request, then the configured workspace root, then the
// current directory: a host that passes a workspace gets exactly that
// directory, and a host that passes nothing still gets a sane, confined guard.
func (o *Orchestrator) workspaceFor(requested string) (*security.Workspace, error) {
	directory := strings.TrimSpace(requested)
	if directory == "" {
		directory = strings.TrimSpace(o.cfg.Security.WorkspaceRoot)
	}
	if directory == "" {
		current, err := os.Getwd()
		if err != nil {
			return nil, apperrors.Wrap(apperrors.KindWorkspace, "orchestrator.workspace",
				"cannot determine the current directory", err)
		}
		directory = current
	}

	workspace, err := security.NewWorkspace(directory, security.WorkspaceOptions{
		AllowOutside:   o.cfg.Security.AllowOutsideWorkspace,
		FollowSymlinks: o.cfg.Security.FollowSymlinks,
		MaxFileBytes:   o.cfg.Security.MaxFileSize,
		BlockedPaths:   o.cfg.Security.BlockedPaths,
	})
	if err != nil {
		return nil, err
	}
	return workspace, nil
}

// outputModeOf resolves the output mode of an agent.
func outputModeOf(agentCfg *config.Agent, override string) string {
	if mode := strings.TrimSpace(override); mode != "" {
		return mode
	}
	if agentCfg != nil {
		if mode := strings.TrimSpace(agentCfg.OutputMode); mode != "" {
			return mode
		}
	}
	return config.OutputModeText
}

// sortedNames returns the keys of a string set, sorted.
func sortedNames(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// describeTarget renders "provider/model" for diagnostics.
func describeTarget(target config.ModelRef) string {
	if target.Model == "" {
		return target.Provider
	}
	return fmt.Sprintf("%s/%s", target.Provider, target.Model)
}
