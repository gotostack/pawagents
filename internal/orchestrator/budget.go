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

package orchestrator

import (
	"time"

	"github.com/pawagents/pawagents/internal/agent"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/llm"
)

// budgetFor builds the budget of one task.
//
// The budget is where an agent stops being a helpful assistant and starts being
// a runaway process, so every limit resolves to a concrete positive number:
// an unconfigured value falls back to the documented default rather than to
// "unlimited".
func budgetFor(agentCfg *config.Agent, request Request, capabilities llm.ModelCapabilities) agent.Budget {
	rounds := agentCfg.MaxRounds
	if request.MaxRounds > 0 {
		rounds = request.MaxRounds
	}

	timeout := agentCfg.Timeout.Duration()
	if request.Timeout > 0 {
		timeout = request.Timeout
	}

	// The context window is the model's, not the agent's: the agent declares a
	// minimum it needs (validated earlier), while the window it actually has
	// is what the provider reported or what the operator wrote for the alias.
	contextTokens := capabilities.MaxContextTokens
	if contextTokens <= 0 {
		contextTokens = agentCfg.MaxContextTokens
	}

	toolOutputBytes := agentCfg.MaxToolOutputBytes
	if toolOutputBytes <= 0 {
		toolOutputBytes = config.DefaultMaxToolOutputBytes
	}

	return agent.Budget{
		MaxRounds:          rounds,
		MaxToolCalls:       agentCfg.MaxToolCalls,
		MaxInputTokens:     agentCfg.MaxInputTokens,
		MaxOutputTokens:    agentCfg.MaxOutputTokens,
		MaxContextTokens:   contextTokens,
		MaxToolOutputBytes: toolOutputBytes,
		Timeout:            timeout,
	}
}

// outputTokenLimit returns how many tokens one completion may produce.
//
// It is the model's own completion limit, because that is the number the
// provider accepts; the agent's max_output_tokens is a budget over the whole
// task and is enforced separately by the loop.
func outputTokenLimit(capabilities llm.ModelCapabilities) int {
	if capabilities.MaxOutputTokens > 0 {
		return capabilities.MaxOutputTokens
	}
	return 0
}

// timeoutOrDefault returns a duration or the documented default.
func timeoutOrDefault(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}
