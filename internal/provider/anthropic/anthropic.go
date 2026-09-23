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

// Package anthropic implements the Anthropic Messages API, the native protocol
// of the Claude models.
//
// It is a first class provider rather than an OpenAI-compatible endpoint,
// because the Messages API differs in ways that matter: content is a list of
// typed blocks, the system prompt is a top level field, tools are declared with
// an input schema, tool results travel back inside a user message, and token
// usage is reported in two places during a stream. Translating through a
// compatibility layer would lose all of that.
package anthropic

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
	providerType = config.ProviderTypeAnthropic

	// messagesPath is the generation endpoint relative to base_url. The
	// default base URL carries no version segment, so the version is part of
	// the path.
	messagesPath = "v1/messages"

	// modelsPath lists the models the credential can reach.
	modelsPath = "v1/models"

	// apiVersion is the protocol version sent with every request. Anthropic
	// requires the header and pins behaviour to the value, which is what makes
	// the API stable for a client.
	apiVersion = "2023-06-01"

	// defaultMaxTokens is used when the caller does not bound the completion.
	// The field is mandatory in this API, so a value must always be sent; it is
	// deliberately modest because the same field is the model's own ceiling and
	// an oversized default would be taken as a licence to ramble.
	defaultMaxTokens = 4096

	// maxResponseBytes bounds a non streaming response body.
	maxResponseBytes = provider.MaxErrorBodyBytes * 16

	// modelListTimeout bounds the model discovery call.
	modelListTimeout = 10 * time.Second
)

// Provider implements provider.Provider for the Messages API.
type Provider struct {
	name      string
	kind      string
	transport *provider.Transport
	logger    *slog.Logger

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
		return nil, apperrors.New(apperrors.KindInternal, "provider.anthropic",
			"the transport is missing")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Provider{
		name:      options.Name,
		kind:      providerType,
		transport: options.Transport,
		logger:    logger,
		probes:    map[string]probeResult{},
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
// The Messages API carries no per model capability metadata, so the answer
// comes from the static table for this provider type plus whatever the model
// listing reveals. A model the listing does not mention produces a warning
// rather than a failure: a proxy in front of the API may expose aliases that
// the listing does not enumerate, and refusing to run on that basis would be
// wrong.
//
// Note what the static table does not claim: the Messages API has no
// response_format, so StructuredOutput is false. The runtime then asks the
// model for the finding envelope in the prompt instead of constraining the
// completion, and a schema is never sent to an endpoint that would reject it.
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
// The Messages API has no version endpoint, so the cheapest probe that proves
// both the endpoint and the credential is the model listing.
func (p *Provider) Health(ctx context.Context) (provider.Health, error) {
	models, err := p.ListModels(ctx)
	if err != nil {
		return provider.Health{Reachable: false, Detail: apperrors.Summary(err)}, err
	}

	health := provider.Health{
		Reachable: true,
		Detail:    "the API answered, " + plural(len(models), "model") + " available",
	}
	return health, nil
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
		// The body contains repository content, so it is logged at debug level
		// only, and never as the default.
		p.logger.Debug("provider request",
			slog.String("provider", p.name),
			slog.String("model", payload.Model),
			slog.Int("messages", len(payload.Messages)),
			slog.Int("tools", len(payload.Tools)),
			slog.Int("bytes", len(encoded)))
	}

	httpRequest, cancel, err := p.newRequest(ctx, http.MethodPost, messagesPath, bytes.NewReader(encoded))
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

	// A server that ignores `stream: true` is answered with one JSON document,
	// which is turned into the equivalent event sequence.
	defer func() {
		_ = response.Body.Close()
		cancel()
	}()

	var message messagesResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&message); err != nil {
		return nil, apperrors.Wrap(apperrors.KindProvider, "provider.generate",
			"the response is neither an event stream nor a JSON message", err)
	}

	events, err := messageEvents(&message, request.Model)
	if err != nil {
		return nil, err
	}
	return provider.NewBufferedStream(events, cancel, nil), nil
}

// buildRequest maps the protocol request onto the Messages API.
func (p *Provider) buildRequest(request *llm.GenerateRequest, stream bool) (*messagesRequest, error) {
	if request.ResponseSchema != nil {
		// Failing loudly is the contract: silently answering with prose while
		// the caller expects a schema would be far worse.
		return nil, apperrors.New(apperrors.KindCapability, "provider.anthropic",
			"the Messages API cannot constrain a completion to a JSON schema; "+
				"set output_mode: text or use a provider with structured output")
	}

	system, conversation := splitSystem(request.Messages)
	messages, err := toWireMessages(conversation)
	if err != nil {
		return nil, err
	}

	maxTokens := request.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}

	payload := &messagesRequest{
		Model:         request.Model,
		System:        system,
		Messages:      messages,
		MaxTokens:     maxTokens,
		Temperature:   request.Temperature,
		TopP:          request.TopP,
		StopSequences: request.Stop,
		Stream:        stream,
		Extra:         provider.MergeExtras(p.transport.ExtraBody, request.Extra),
	}

	// Tool use is all or nothing: the API expresses "do not use tools" by the
	// absence of a tool list, so a none choice drops the definitions too.
	if len(request.Tools) > 0 && request.ToolChoice.Mode != llm.ToolChoiceNone {
		payload.Tools = toWireTools(request.Tools)
		payload.ToolChoice = toToolChoice(request.ToolChoice)
	}

	return payload, nil
}

// newRequest builds a request with the headers the API requires.
func (p *Provider) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, context.CancelFunc, error) {
	contentType := ""
	if body != nil {
		contentType = "application/json"
	}

	request, cancel, err := p.transport.NewRequest(ctx, method, path, contentType, body)
	if err != nil {
		return nil, nil, err
	}
	request.Header.Set("anthropic-version", apiVersion)
	return request, cancel, nil
}

// plural renders a count with its noun.
func plural(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", count, noun)
}

var _ provider.Provider = (*Provider)(nil)
var _ provider.ModelLister = (*Provider)(nil)
var _ provider.HealthReporter = (*Provider)(nil)
var _ provider.Describer = (*Provider)(nil)
