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

// Package openairesponses implements the OpenAI Responses API.
//
// It is the successor of Chat Completions for the OpenAI platform, and it is
// modelled as its own protocol rather than as a chat variant: the system prompt
// is an `instructions` field, the conversation is a flat list of typed input
// items, a tool call is its own item with a call identifier, and a completion
// can be constrained by a JSON schema through `text.format`. The reasoning
// channel, the cached token accounting and the schema constrained output are
// all reported as capabilities instead of being guessed from a model name.
package openairesponses

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
	providerType = config.ProviderTypeOpenAIResponses

	// responsesPath is the generation endpoint relative to base_url, which
	// already carries the version segment.
	responsesPath = "responses"

	// modelsPath lists the models the credential can reach.
	modelsPath = "models"

	// maxResponseBytes bounds a non streaming response body.
	maxResponseBytes = provider.MaxErrorBodyBytes * 16

	// modelListTimeout bounds the model discovery call.
	modelListTimeout = 10 * time.Second
)

// Provider implements provider.Provider for the Responses API.
type Provider struct {
	name         string
	kind         string
	organization string
	transport    *provider.Transport
	logger       *slog.Logger

	probeMu sync.Mutex
	probes  map[string]probeResult
}

type probeResult struct {
	models  []string
	err     error
	expires time.Time
}

// New builds a provider from its registry options.
func New(_ context.Context, options provider.Options) (provider.Provider, error) {
	if options.Transport == nil {
		return nil, apperrors.New(apperrors.KindInternal, "provider.openairesponses",
			"the transport is missing")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}

	organization := ""
	if options.Config != nil {
		organization = strings.TrimSpace(options.Config.Organization)
	}

	return &Provider{
		name:         options.Name,
		kind:         providerType,
		organization: organization,
		transport:    options.Transport,
		logger:       logger,
		probes:       map[string]probeResult{},
	}, nil
}

func init() {
	provider.Register(providerType, New)
}

// Name implements provider.Provider.
func (p *Provider) Name() string { return p.name }

// Type implements provider.Provider.
func (p *Provider) Type() string { return p.kind }

// Info implements provider.Describer.
func (p *Provider) Info() provider.Info {
	return provider.Info{
		Name:       p.name,
		Type:       p.kind,
		BaseURL:    p.transport.Endpoint(),
		Credential: p.transport.DescribeCredential(),
	}
}

// Close implements provider.Closer.
func (p *Provider) Close() error { return p.transport.Close() }

// Capabilities implements provider.Provider.
//
// The Responses API carries no per model capability metadata, so the answer
// comes from the static table for this provider type plus whatever the model
// listing reveals. A model the listing does not mention is a warning rather
// than a failure: an account may have access to a model that the listing does
// not enumerate, and refusing to run on that basis would be wrong.
func (p *Provider) Capabilities(ctx context.Context, model string) (llm.ModelCapabilities, error) {
	capabilities := provider.StaticCapabilities(providerType)

	models, err := p.ListModels(ctx)
	if err != nil {
		if apperrors.IsKind(err, apperrors.KindAuthentication) {
			return llm.ModelCapabilities{}, err
		}
		p.logger.Debug("cannot list models, falling back to static capabilities",
			slog.String("provider", p.name), slog.String("error", err.Error()))
		return capabilities, nil
	}

	if !provider.MatchModel(models, model) {
		p.logger.Warn("the endpoint does not list the configured model; "+
			"the request will still be attempted",
			slog.String("provider", p.name),
			slog.String("model", model),
			slog.Int("listed_models", len(models)))
	}

	return capabilities, nil
}

// ListModels reports the model identifiers the credential can reach.
func (p *Provider) ListModels(ctx context.Context) ([]string, error) {
	p.probeMu.Lock()
	if cached, ok := p.probes[modelsPath]; ok && time.Now().Before(cached.expires) {
		p.probeMu.Unlock()
		return cached.models, cached.err
	}
	p.probeMu.Unlock()

	models, err := p.fetchModels(ctx)

	p.probeMu.Lock()
	p.probes[modelsPath] = probeResult{models: models, err: err, expires: time.Now().Add(time.Minute)}
	p.probeMu.Unlock()

	return models, err
}

func (p *Provider) fetchModels(ctx context.Context) ([]string, error) {
	requestCtx, cancel := context.WithTimeout(ctx, modelListTimeout)
	defer cancel()

	request, requestCancel, err := p.newRequest(requestCtx, http.MethodGet, modelsPath, nil)
	if err != nil {
		return nil, err
	}
	defer requestCancel()

	response, err := p.transport.Client.Do(request)
	if err != nil {
		return nil, provider.NetworkError("provider.models", err, "")
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode >= 400 {
		return nil, provider.HTTPStatusError("provider.models", response, provider.ReadErrorBody(response))
	}

	var listing modelListing
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&listing); err != nil {
		return nil, apperrors.Wrap(apperrors.KindProvider, "provider.models",
			"the model listing is not valid JSON", err)
	}

	models := make([]string, 0, len(listing.Data))
	for _, model := range listing.Data {
		if strings.TrimSpace(model.ID) != "" {
			models = append(models, model.ID)
		}
	}
	return models, nil
}

// Health implements provider.HealthReporter.
//
// The platform has no version endpoint, so the cheapest probe that proves both
// the endpoint and the credential is the model listing.
func (p *Provider) Health(ctx context.Context) (provider.Health, error) {
	models, err := p.ListModels(ctx)
	if err != nil {
		return provider.Health{Reachable: false, Detail: apperrors.Summary(err)}, err
	}

	noun := "models"
	if len(models) == 1 {
		noun = "model"
	}
	return provider.Health{
		Reachable: true,
		Detail:    fmt.Sprintf("the API answered, %d %s available", len(models), noun),
	}, nil
}

// Generate implements provider.Provider.
func (p *Provider) Generate(ctx context.Context, request *llm.GenerateRequest) (llm.Stream, error) {
	payload, err := p.buildRequest(request, true)
	if err != nil {
		return nil, err
	}

	encoded, err := provider.MarshalRequest(payload, payload.Extra)
	if err != nil {
		return nil, err
	}
	if p.logger.Enabled(ctx, slog.LevelDebug) {
		p.logger.Debug("provider request",
			slog.String("provider", p.name),
			slog.String("model", payload.Model),
			slog.Int("input_items", len(payload.Input)),
			slog.Int("tools", len(payload.Tools)),
			slog.Int("bytes", len(encoded)))
	}

	httpRequest, cancel, err := p.newRequest(ctx, http.MethodPost, responsesPath, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Accept", "text/event-stream")

	response, err := p.transport.Client.Do(httpRequest)
	if err != nil {
		cancel()
		return nil, provider.NetworkError("provider.generate", err, "")
	}

	if response.StatusCode >= 400 {
		body := provider.ReadErrorBody(response)
		_ = response.Body.Close()
		cancel()
		return nil, provider.HTTPStatusError("provider.generate", response, body)
	}

	if strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		return newSSEStream(response.Body, request.Model, p.logger, cancel), nil
	}

	defer func() {
		_ = response.Body.Close()
		cancel()
	}()

	var completion responsesResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&completion); err != nil {
		return nil, apperrors.Wrap(apperrors.KindProvider, "provider.generate",
			"the response is neither an event stream nor a JSON response", err)
	}

	events, err := completionEvents(&completion, request.Model)
	if err != nil {
		return nil, err
	}
	return provider.NewBufferedStream(events, cancel, nil), nil
}

// buildRequest maps the protocol request onto the Responses API.
func (p *Provider) buildRequest(request *llm.GenerateRequest, stream bool) (*responsesRequest, error) {
	instructions, conversation := splitInstructions(request.Messages)
	items, err := toInputItems(conversation)
	if err != nil {
		return nil, err
	}

	payload := &responsesRequest{
		Model:           request.Model,
		Instructions:    instructions,
		Input:           items,
		MaxOutputTokens: request.MaxTokens,
		Temperature:     request.Temperature,
		TopP:            request.TopP,
		Stream:          stream,
		Extra:           provider.MergeExtras(p.transport.ExtraBody, request.Extra),
	}

	if len(request.Tools) > 0 {
		payload.Tools = toWireTools(request.Tools)
		payload.ToolChoice = toToolChoice(request.ToolChoice)
	}

	if request.ResponseSchema != nil && request.ResponseSchemaName != "" {
		payload.Text = textFormat(request.ResponseSchemaName, *request.ResponseSchema)
	}

	return payload, nil
}

// newRequest builds a request with the headers the API expects.
func (p *Provider) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, context.CancelFunc, error) {
	contentType := ""
	if body != nil {
		contentType = "application/json"
	}

	request, cancel, err := p.transport.NewRequest(ctx, method, path, contentType, body)
	if err != nil {
		return nil, nil, err
	}
	if p.organization != "" {
		request.Header.Set("OpenAI-Organization", p.organization)
	}
	return request, cancel, nil
}

var _ provider.Provider = (*Provider)(nil)
var _ provider.ModelLister = (*Provider)(nil)
var _ provider.HealthReporter = (*Provider)(nil)
var _ provider.Describer = (*Provider)(nil)
