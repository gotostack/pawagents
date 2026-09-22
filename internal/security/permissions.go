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

package security

import (
	"sort"
	"strings"

	"github.com/pawagents/pawagents/internal/apperrors"
)

// Permission levels accepted by the runtime. They mirror the configuration
// values, and are duplicated here as plain strings so that this package does
// not depend on the configuration package.
const (
	// FilesystemRead allows reading inside the workspace.
	FilesystemRead = "read"
	// FilesystemNone grants no filesystem access.
	FilesystemNone = "none"
	// ShellDeny disables shell access, which is the only supported value.
	ShellDeny = "deny"
)

// Grant is the set of tools an agent may call.
//
// A grant is built from the agent profile: the granted tool list plus the
// permission allowances, minus the denials. Denial always wins, so a profile
// can grant a broad list and carve an exception out of it.
//
// An empty grant means no tool at all, which is what an agent without a tool
// list gets: PawAgents never widens a grant implicitly.
type Grant struct {
	allowed map[string]bool
	order   []string
}

// NewGrant builds a grant from an agent profile.
func NewGrant(granted []string, allow []string, deny []string) *Grant {
	allowed := make(map[string]bool, len(granted)+len(allow))

	for _, name := range granted {
		if name = strings.TrimSpace(name); name != "" {
			allowed[name] = true
		}
	}
	for _, name := range allow {
		if name = strings.TrimSpace(name); name != "" {
			allowed[name] = true
		}
	}
	for _, name := range deny {
		delete(allowed, strings.TrimSpace(name))
	}

	order := make([]string, 0, len(allowed))
	for name := range allowed {
		order = append(order, name)
	}
	sort.Strings(order)

	return &Grant{allowed: allowed, order: order}
}

// NewGrantForToolset builds the grant of a toolset and verifies that the
// filesystem and shell permissions stay read-only.
//
// Configuration validation already rejects a writable profile, but the runtime
// checks again because it is the last line of defence before a tool runs: a
// configuration assembled in code never passes through the validator.
func NewGrantForToolset(granted, allow, deny []string, filesystem, shell string) (*Grant, error) {
	switch filesystem {
	case "", FilesystemRead, FilesystemNone:
	default:
		return nil, apperrors.New(apperrors.KindPermission, "agent.permissions",
			"filesystem permission %q is not supported; PawAgents subagents are read-only",
			filesystem)
	}

	switch shell {
	case "", ShellDeny, FilesystemNone:
	default:
		return nil, apperrors.New(apperrors.KindPermission, "agent.permissions",
			"shell permission %q is not supported; PawAgents never executes shell commands",
			shell)
	}

	return NewGrant(granted, allow, deny), nil
}

// Allowed reports whether a tool may be called.
func (g *Grant) Allowed(name string) bool {
	if g == nil {
		return false
	}
	return g.allowed[strings.TrimSpace(name)]
}

// Names returns the granted tool names, sorted.
func (g *Grant) Names() []string {
	if g == nil {
		return nil
	}
	return append([]string(nil), g.order...)
}

// Len returns how many tools are granted.
func (g *Grant) Len() int {
	if g == nil {
		return 0
	}
	return len(g.order)
}

// Empty reports whether the grant grants nothing.
func (g *Grant) Empty() bool { return g.Len() == 0 }

// Deny returns a permission error for a tool that is not granted.
func (g *Grant) Deny(name string) error {
	return apperrors.New(apperrors.KindPermission, "tool.grant",
		"the agent is not allowed to call tool %q", name).
		WithDetails(map[string]any{"tool": name, "granted": g.Names()})
}
