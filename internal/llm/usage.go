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

package llm

// Usage records token accounting for one provider request.
//
// The JSON field names match the structured result envelope documented in the
// README, so a session file and an MCP response describe usage identically.
type Usage struct {
	// InputTokens is the number of prompt tokens billed by the provider.
	InputTokens int `json:"input_tokens"`
	// OutputTokens is the number of completion tokens.
	OutputTokens int `json:"output_tokens"`
	// TotalTokens is reported directly by some providers and derived when it
	// is missing.
	TotalTokens int `json:"total_tokens,omitempty"`
	// CacheReadTokens counts prompt tokens served from a provider cache.
	CacheReadTokens int `json:"cache_read_tokens,omitempty"`
	// CacheWriteTokens counts prompt tokens written to a provider cache.
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
	// ReasoningTokens counts hidden reasoning tokens when a provider exposes
	// them separately from the completion.
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// IsZero reports whether nothing was recorded.
func (u Usage) IsZero() bool {
	return u == Usage{}
}

// Add returns the sum of two usage records.
func (u Usage) Add(other Usage) Usage {
	return Usage{
		InputTokens:      u.InputTokens + other.InputTokens,
		OutputTokens:     u.OutputTokens + other.OutputTokens,
		TotalTokens:      u.TotalTokens + other.TotalTokens,
		CacheReadTokens:  u.CacheReadTokens + other.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens + other.CacheWriteTokens,
		ReasoningTokens:  u.ReasoningTokens + other.ReasoningTokens,
	}
}

// Normalize fills TotalTokens when a provider reported only the two halves.
func (u *Usage) Normalize() {
	if u == nil {
		return
	}
	if u.TotalTokens == 0 {
		u.TotalTokens = u.InputTokens + u.OutputTokens
	}
	if u.TotalTokens != 0 && u.TotalTokens < u.InputTokens+u.OutputTokens {
		u.TotalTokens = u.InputTokens + u.OutputTokens
	}
}

// ContextTokens returns the number of tokens that occupy the context window.
//
// Cached prompt tokens still occupy it. Providers differ in whether they count
// them inside InputTokens (OpenAI style) or beside it (Anthropic style), so
// adapters leave the irrelevant fields at zero and the sum stays correct for
// both.
func (u Usage) ContextTokens() int {
	return u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens
}
