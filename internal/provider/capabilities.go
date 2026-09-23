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

package provider

import (
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/llm"
)

// ConservativeContextTokens is the context window assumed for a model whose
// real window is unknown. Budgeting needs a positive number, and pretending a
// local model has a 200k window would produce a request the server rejects.
const ConservativeContextTokens = 8192

// ConservativeMaxOutputTokens is the completion budget assumed for a model
// that does not declare one.
const ConservativeMaxOutputTokens = 4096

// CloudContextTokens is the assumed context window of the hosted models whose
// providers document a large window.
const CloudContextTokens = 200000

// StaticCapabilities returns what a provider type guarantees by contract.
//
// These values are a starting point, not the truth. The authoritative source
// is the provider's Capabilities call, which probes the endpoint; the static
// table only exists so that a request can be validated before the first probe
// completes, and so that an unreachable endpoint still produces a useful
// diagnostic.
//
// Note the asymmetry on ToolCalling: hosted providers document function
// calling for their chat models, so it is assumed true, while an Ollama server
// can host a model without it, so it is assumed false until probed.
func StaticCapabilities(providerType string) llm.ModelCapabilities {
	switch providerType {
	case config.ProviderTypeOllama:
		return llm.ModelCapabilities{
			Streaming:        true,
			SystemMessage:    true,
			MaxContextTokens: ConservativeContextTokens,
		}
	case config.ProviderTypeOpenAIResponses, config.ProviderTypeOpenAIChat:
		return llm.ModelCapabilities{
			ToolCalling:      true,
			ParallelTools:    true,
			Streaming:        true,
			StructuredOutput: true,
			Vision:           true,
			Reasoning:        true,
			SystemMessage:    true,
			MaxContextTokens: CloudContextTokens,
		}
	case config.ProviderTypeAnthropic:
		// The Messages API has no response_format: a completion cannot be
		// constrained to a JSON schema, so the runtime asks the model for the
		// finding envelope in the prompt instead. Claiming otherwise would make
		// the agent loop send a schema the endpoint rejects.
		return llm.ModelCapabilities{
			ToolCalling:      true,
			ParallelTools:    true,
			Streaming:        true,
			StructuredOutput: false,
			Vision:           true,
			Reasoning:        true,
			SystemMessage:    true,
			MaxContextTokens: CloudContextTokens,
		}
	case config.ProviderTypeOpenAICompat:
		// Compatibility endpoints vary wildly: some are full OpenAI clones,
		// some are narrower. Only the universally safe subset is assumed.
		return llm.ModelCapabilities{
			ToolCalling:      true,
			Streaming:        true,
			SystemMessage:    true,
			MaxContextTokens: ConservativeContextTokens,
		}
	case config.ProviderTypeGemini, config.ProviderTypeBedrock:
		return llm.ModelCapabilities{
			Streaming:        true,
			SystemMessage:    true,
			MaxContextTokens: CloudContextTokens,
		}
	default:
		return llm.ModelCapabilities{
			Streaming:        true,
			SystemMessage:    true,
			MaxContextTokens: ConservativeContextTokens,
		}
	}
}

// ApplyOverrides merges operator supplied capability overrides onto base.
//
// Overrides win over everything else, including a probe result: an operator
// who writes `tool_calling: true` for a local model knows something the probe
// could not detect, and silently ignoring that would be worse than trusting
// it. A wrong override surfaces as a clear provider error instead of a
// mysterious capability refusal.
func ApplyOverrides(base llm.ModelCapabilities, overrides *config.CapabilityOverrides) llm.ModelCapabilities {
	if overrides == nil {
		return base
	}
	out := base

	if overrides.ToolCalling != nil {
		out.ToolCalling = *overrides.ToolCalling
	}
	if overrides.ParallelTools != nil {
		out.ParallelTools = *overrides.ParallelTools
	}
	if overrides.Streaming != nil {
		out.Streaming = *overrides.Streaming
	}
	if overrides.StructuredOutput != nil {
		out.StructuredOutput = *overrides.StructuredOutput
	}
	if overrides.Vision != nil {
		out.Vision = *overrides.Vision
	}
	if overrides.Reasoning != nil {
		out.Reasoning = *overrides.Reasoning
	}
	if overrides.SystemMessage != nil {
		out.SystemMessage = *overrides.SystemMessage
	}
	if overrides.MaxContextTokens != nil && *overrides.MaxContextTokens > 0 {
		out.MaxContextTokens = *overrides.MaxContextTokens
	}

	return out
}

// Resolve produces the effective capabilities of a model alias.
//
// Precedence, highest first:
//
//  1. the capability overrides in the model configuration,
//  2. the probe result, when the provider already reported one,
//  3. the static table for the provider type.
//
// The model configuration also contributes the declared context window and
// output budget, which take precedence over the static table because the
// operator wrote them deliberately for this alias.
func Resolve(providerType string, model *config.Model, probe *llm.ModelCapabilities) llm.ModelCapabilities {
	capabilities := StaticCapabilities(providerType)
	if probe != nil {
		capabilities = *probe
	}

	if model != nil {
		if model.MaxContextTokens > 0 {
			capabilities.MaxContextTokens = model.MaxContextTokens
		}
		if model.MaxOutputTokens > 0 {
			capabilities.MaxOutputTokens = model.MaxOutputTokens
		}
		capabilities = ApplyOverrides(capabilities, model.Capabilities)
	}

	return normalize(capabilities)
}

// normalize guarantees the invariants the budget manager relies on: a
// positive context window and a positive output budget.
func normalize(capabilities llm.ModelCapabilities) llm.ModelCapabilities {
	if capabilities.MaxContextTokens <= 0 {
		capabilities.MaxContextTokens = ConservativeContextTokens
	}
	if capabilities.MaxOutputTokens <= 0 {
		capabilities.MaxOutputTokens = ConservativeMaxOutputTokens
	}
	if capabilities.MaxOutputTokens > capabilities.MaxContextTokens {
		capabilities.MaxOutputTokens = capabilities.MaxContextTokens
	}
	return capabilities
}
