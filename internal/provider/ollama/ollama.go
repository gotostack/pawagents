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

// Package ollama implements the native Ollama provider.
//
// Ollama is a first class provider rather than a "point an
// openai-compatible provider at port 11434" recipe, for three reasons:
//
//  1. its native API reports which capabilities a model actually has, so tool
//     calling can be detected instead of assumed;
//  2. the native stream carries reasoning output and token statistics that the
//     compatibility endpoint drops;
//  3. a local runtime must work with no cloud API key and no network, and the
//     failures it produces (server down, model not pulled, model without tool
//     support) deserve precise diagnostics.
package ollama

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/llm"
	"github.com/pawagents/pawagents/internal/provider"
)

const (
	// providerType is the configuration type this package implements.
	providerType = config.ProviderTypeOllama

	// metadataTTL bounds how long model metadata is cached. A `ollama pull`
	// during a session must become visible without a restart.
	metadataTTL = 5 * time.Minute
)

// Capability names reported by /api/show.
const (
	capabilityCompletion = "completion"
	capabilityTools      = "tools"
	capabilityVision     = "vision"
	capabilityThinking   = "thinking"
	capabilityEmbedding  = "embedding"
)

// Provider implements provider.Provider for a native Ollama server.
type Provider struct {
	name      string
	transport *provider.Transport
	logger    *slog.Logger

	mu       sync.Mutex
	metadata map[string]metadataEntry
}

type metadataEntry struct {
	model   modelMetadata
	err     error
	expires time.Time
}

// modelMetadata is what /api/show reveals about a model.
type modelMetadata struct {
	capabilities  []string
	contextLength int
	parameterSize string
	family        string
}

// has reports whether the model advertises a capability.
func (m modelMetadata) has(capability string) bool {
	for _, candidate := range m.capabilities {
		if candidate == capability {
			return true
		}
	}
	return false
}

// New builds the provider from its registry options.
func New(_ context.Context, options provider.Options) (provider.Provider, error) {
	if options.Transport == nil {
		return nil, apperrors.New(apperrors.KindInternal, "provider.ollama",
			"the transport is missing")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Provider{
		name:      options.Name,
		transport: options.Transport,
		logger:    logger,
		metadata:  map[string]metadataEntry{},
	}, nil
}

func init() {
	provider.Register(providerType, New)
}

// Name implements provider.Provider.
func (p *Provider) Name() string { return p.name }

// Type implements provider.Provider.
func (p *Provider) Type() string { return providerType }

// Info implements provider.Describer.
func (p *Provider) Info() provider.Info {
	return provider.Info{
		Name:       p.name,
		Type:       providerType,
		BaseURL:    p.transport.Endpoint(),
		Credential: p.transport.DescribeCredential(),
	}
}

// Close implements provider.Closer.
func (p *Provider) Close() error { return p.transport.Close() }

// Capabilities implements provider.Provider.
//
// The answer comes from /api/show, which is the only reliable source: the same
// server can host a model with tool support and one without. A model that is
// not installed is an error, because the request would fail anyway and a
// capability error is far more actionable than a 404 from the server.
func (p *Provider) Capabilities(ctx context.Context, model string) (llm.ModelCapabilities, error) {
	if strings.TrimSpace(model) == "" {
		return llm.ModelCapabilities{}, apperrors.New(apperrors.KindInvalidArgument,
			"provider.ollama", "a model name is required to report capabilities")
	}

	metadata, err := p.modelMetadata(ctx, model)
	if err != nil {
		if apperrors.IsKind(err, apperrors.KindNotFound) {
			return llm.ModelCapabilities{}, err
		}
		// The server may be older than the capability reporting, or briefly
		// unreachable. Fall back to the conservative table instead of
		// blocking a run on an unrelated probe.
		p.logger.Debug("cannot read the model metadata, using conservative capabilities",
			slog.String("provider", p.name),
			slog.String("model", model),
			slog.String("error", err.Error()))
		return provider.StaticCapabilities(providerType), nil
	}

	capabilities := llm.ModelCapabilities{
		// Ollama always streams and always accepts a system role.
		Streaming:     true,
		SystemMessage: true,
		// /api/chat accepts a JSON schema in the format field.
		StructuredOutput: true,
		ToolCalling:      metadata.has(capabilityTools),
		Vision:           metadata.has(capabilityVision),
		Reasoning:        metadata.has(capabilityThinking),
		MaxContextTokens: metadata.contextLength,
	}

	if !capabilities.ToolCalling {
		p.logger.Debug("model does not advertise tool support",
			slog.String("provider", p.name),
			slog.String("model", model),
			slog.String("capabilities", strings.Join(metadata.capabilities, ",")))
	}

	return capabilities, nil
}

// modelMetadata returns the cached metadata of a model, reading it from
// /api/show when needed.
func (p *Provider) modelMetadata(ctx context.Context, model string) (modelMetadata, error) {
	if strings.TrimSpace(model) == "" {
		return modelMetadata{}, apperrors.New(apperrors.KindInvalidArgument, "provider.ollama",
			"a model name is required")
	}

	p.mu.Lock()
	if entry, ok := p.metadata[model]; ok && time.Now().Before(entry.expires) {
		p.mu.Unlock()
		return entry.model, entry.err
	}
	p.mu.Unlock()

	metadata, err := p.fetchMetadata(ctx, model)

	p.mu.Lock()
	p.metadata[model] = metadataEntry{
		model:   metadata,
		err:     err,
		expires: time.Now().Add(metadataTTL),
	}
	p.mu.Unlock()

	return metadata, err
}

// ListModels implements provider.ModelLister.
func (p *Provider) ListModels(ctx context.Context) ([]string, error) {
	entries, err := p.fetchTags(ctx)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool, len(entries))
	models := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name
		if name == "" {
			name = entry.Model
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		models = append(models, name)
	}
	sort.Strings(models)
	return models, nil
}

// Health implements provider.HealthReporter.
func (p *Provider) Health(ctx context.Context) (provider.Health, error) {
	version, err := p.fetchVersion(ctx)
	if err != nil {
		return provider.Health{Reachable: false, Detail: err.Error()}, err
	}
	health := provider.Health{Reachable: true, Version: version}
	if version == "" {
		health.Detail = "the server answered but did not report a version"
		return health, nil
	}
	health.Detail = "Ollama " + version
	return health, nil
}

// SuggestModelName returns the name of the first installed model, used to help
// a user who configured a model that is not pulled yet.
func (p *Provider) SuggestModelName(ctx context.Context) string {
	models, err := p.ListModels(ctx)
	if err != nil || len(models) == 0 {
		return ""
	}
	return models[0]
}
