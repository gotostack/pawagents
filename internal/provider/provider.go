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

// Package provider defines the boundary between the PawAgents runtime and a
// model endpoint.
//
// Everything above this package works with model aliases and the vendor
// neutral protocol in internal/llm. Everything below it speaks a vendor wire
// format. A provider implementation therefore has exactly three jobs:
//
//  1. report what a model can do (Capabilities),
//  2. translate a GenerateRequest into a vendor request,
//  3. translate the vendor response stream back into llm.Events.
//
// A provider never decides policy. Budgets, permissions, tool execution and
// retries belong to the agent loop and the orchestrator; a provider that
// silently retries or truncates would break the accounting those layers
// depend on.
package provider

import (
	"context"

	"github.com/pawagents/pawagents/internal/llm"
)

// Provider is implemented by every model endpoint adapter.
type Provider interface {
	// Name returns the configured provider name, for example "local-ollama".
	// It is what the user wrote in config.yaml and what appears in a session.
	Name() string

	// Type returns the provider type identifier, for example "ollama". It
	// selects the adapter and is reported by `pagent provider list`.
	Type() string

	// Capabilities reports what the given model supports.
	//
	// Implementations must not guess: a model whose tool support is unknown
	// reports ToolCalling false, and the agent loop turns that into a
	// CapabilityError instead of attempting a text based tool protocol.
	Capabilities(ctx context.Context, model string) (llm.ModelCapabilities, error)

	// Generate starts a streaming generation.
	//
	// The returned stream is owned by the caller, which must close it. An
	// error is returned only when generation could not start; failures after
	// that point are reported as an error event on the stream so that partial
	// output is not lost.
	Generate(ctx context.Context, request *llm.GenerateRequest) (llm.Stream, error)
}

// Closer is implemented by providers that own resources such as HTTP
// transports. It is optional: a provider without resources simply does not
// implement it. The registry closes what it can when it is shut down.
type Closer interface {
	// Close releases the provider resources. It must be safe to call twice.
	Close() error
}

// Info describes a provider for `pagent provider list` and for diagnostics.
type Info struct {
	// Name is the configured provider name.
	Name string `json:"name"`
	// Type is the provider type identifier.
	Type string `json:"type"`
	// BaseURL is the endpoint the provider talks to. Secrets are never
	// included.
	BaseURL string `json:"base_url,omitempty"`
	// Disabled reports whether the configuration excluded the provider.
	Disabled bool `json:"disabled,omitempty"`
	// Credential reports how authentication is configured, for example
	// "env:OPENAI_API_KEY", "inline" or "none". The credential itself is
	// never included.
	Credential string `json:"credential,omitempty"`
}

// Describer is implemented by providers that can describe themselves without
// opening a connection.
type Describer interface {
	// Info returns the provider description.
	Info() Info
}
