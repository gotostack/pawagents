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

// Package agent implements the agent loop: the part of PawAgents that turns a
// delegated task into a bounded conversation with a model and a set of tool
// calls.
//
// The loop is deliberately narrow. It does not choose a model, resolve a
// provider or build a workspace guard; the orchestrator does that and hands the
// result in. What the loop owns is the part that must not vary between hosts:
// building the prompt, calling the model, validating the model's capabilities
// before the first call, executing the tools it granted, enforcing the budget
// and assembling a structured result.
package agent

import (
	"context"
	"time"

	"github.com/pawagents/pawagents/internal/llm"
	"github.com/pawagents/pawagents/internal/security"
)

// Model is the model access the loop needs. provider.Provider satisfies it, so
// the agent package does not depend on the provider package.
type Model interface {
	// Name returns the configured provider name.
	Name() string
	// Type returns the provider type identifier.
	Type() string
	// Capabilities reports what the model supports.
	Capabilities(ctx context.Context, model string) (llm.ModelCapabilities, error)
	// Generate starts a streaming generation.
	Generate(ctx context.Context, request *llm.GenerateRequest) (llm.Stream, error)
}

// Output modes.
const (
	// OutputText returns the model answer as written.
	OutputText = "text"
	// OutputStructured asks for the finding envelope and parses it.
	OutputStructured = "structured"
)

// Budget bounds one delegated task.
//
// Every field is a hard limit: hitting one stops the loop with a
// BudgetExceededError rather than continuing on a best effort basis. A task
// that silently ran past its budget would be worse than a task that failed,
// because the host would receive a confident answer it cannot trust.
type Budget struct {
	// MaxRounds bounds the number of model calls.
	MaxRounds int
	// MaxToolCalls bounds the number of tool invocations.
	MaxToolCalls int
	// MaxInputTokens bounds the tokens sent across all rounds.
	MaxInputTokens int
	// MaxOutputTokens bounds the tokens generated across all rounds.
	MaxOutputTokens int
	// MaxContextTokens bounds the tokens of a single request.
	MaxContextTokens int
	// MaxToolOutputBytes bounds a single tool result.
	MaxToolOutputBytes int
	// Timeout bounds the whole task.
	Timeout time.Duration
}

// Profile is the runtime view of an agent configuration.
type Profile struct {
	// Name is the agent name, as a host refers to it.
	Name string
	// Description explains what the agent is for.
	Description string
	// ModelAlias is the configured model alias.
	ModelAlias string
	// SystemPrompt is the assembled instruction text.
	SystemPrompt string
	// OutputMode is OutputText or OutputStructured.
	OutputMode string
	// Tools are the tool names the agent may use.
	Tools []string
	// Grant is the enforced tool grant.
	Grant *security.Grant
}

// Usage summarises what a task consumed.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	ToolCalls    int `json:"tool_calls"`
	Rounds       int `json:"rounds"`
}

// Add merges another usage record.
func (u Usage) Add(other Usage) Usage {
	return Usage{
		InputTokens:  u.InputTokens + other.InputTokens,
		OutputTokens: u.OutputTokens + other.OutputTokens,
		ToolCalls:    u.ToolCalls + other.ToolCalls,
		Rounds:       u.Rounds + other.Rounds,
	}
}
