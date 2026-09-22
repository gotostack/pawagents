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

// Package llm defines the vendor neutral protocol that every PawAgents
// subsystem speaks: messages, tool definitions, generation requests, streaming
// events, usage accounting and model capabilities.
//
// # Why this package exists
//
// Vendor SDK types must never reach the agent runtime. `openai.ChatCompletion`
// `Message`, `anthropic.Message` and `ollama.ChatResponse` all describe the
// same three ideas with different shapes, so a runtime that consumes them
// directly ends up with a branch per vendor in its core loop. Providers
// therefore translate in both directions at their own boundary:
//
//	agent  ──llm.GenerateRequest──▶  provider  ──vendor request──▶  API
//	agent  ◀──llm.Event────────────  provider  ◀──vendor stream───  API
//
// The import direction is fixed: providers and the agent loop may import this
// package, and this package imports nothing but the standard library plus
// apperrors. That keeps the protocol free of cycles and free of vendor
// dependencies.
//
// # Streaming model
//
// Generate returns a Stream of Events rather than a finished response. A
// provider that does not stream natively still implements the same interface
// by emitting a single text delta followed by a finish event, which keeps the
// agent loop identical for every provider.
package llm
