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

// Command pagent is the PawAgents single binary. It exposes both the
// human-facing CLI and, in later phases, the MCP server used by Codex and
// Claude Code.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/pawagents/pawagents/internal/cli"
)

func main() {
	// Interrupt and SIGTERM cancel the context, which lets a running agent
	// loop stop at the next checkpoint instead of being killed mid-flight.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(cli.Execute(ctx))
}
