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

package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	providerType = config.ProviderTypeOpenAICompat

	// chatPath is the Chat Completions endpoint relative to base_url.
	chatPath = "chat/completions"

	// modelsPath lists the models the endpoint serves.
	modelsPath = "models"

	// maxErrorBodyBytes bounds how much of an error body is read, so a
	// misbehaving gateway cannot exhaust memory with a huge HTML error page.
	maxErrorBodyBytes = 64 * 1024

	// maxEventBytes bounds a single SSE line.
	maxEventBytes = 4 * 1024 * 1024

	// modelListTimeout bounds the model discovery call.
	modelListTimeout = 10 * time.Second
)

// Provider implements the OpenAI Chat Completions protocol.
//
// It is used for `type: openai-compatible` endpoints and, through embedding,
// for OpenAI itself once the dedicated adapter lands. Anything vendor specific
// stays out of this file: the same code must work for DeepSeek, DashScope,
// OpenRouter, LiteLLM, vLLM and a company gateway.
type Provider struct {
	name         string
	kind         string
	transport    *provider.Transport
	logger       *slog.Logger
	includeUsage bool

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
		return nil, apperrors.New(apperrors.KindInternal, "provider.openaicompat",
			"the transport is missing")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Provider{
		name:         options.Name,
		kind:         providerType,
		transport:    options.Transport,
		logger:       logger,
		includeUsage: true,
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
// The Chat Completions protocol carries no capability metadata, so the answer
// comes from the static table for this provider type plus whatever the model
// listing reveals. A model that the endpoint does not list produces a warning
// rather than a failure: gateways frequently expose aliases that the listing
// does not enumerate, and refusing to run on that basis would be wrong. Use
// `pagent provider test` for a strict existence check.
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

// ListModels reports the model identifiers the endpoint advertises.
//
// Endpoints that do not implement the listing return a
// KindCapability error, which callers treat as "unknown" rather than as a
// failure.
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

	request, requestCancel, err := p.transport.NewRequest(requestCtx, http.MethodGet, modelsPath, "", nil)
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

	var listing chatModelList
	if err := json.NewDecoder(io.LimitReader(response.Body, maxErrorBodyBytes*16)).Decode(&listing); err != nil {
		return nil, apperrors.Wrap(apperrors.KindProvider, "provider.models",
			"the model listing is not valid JSON", err)
	}

	models := make([]string, 0, len(listing.Data))
	for _, model := range listing.Data {
		if model.ID != "" {
			models = append(models, model.ID)
		}
	}
	return models, nil
}

// Generate implements provider.Provider.
func (p *Provider) Generate(ctx context.Context, request *llm.GenerateRequest) (llm.Stream, error) {
	if request == nil {
		return nil, apperrors.New(apperrors.KindInvalidArgument, "provider.generate",
			"the generation request is nil")
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}

	payload, err := p.buildRequest(request, true)
	if err != nil {
		return nil, err
	}

	response, cancel, err := p.post(ctx, payload)
	if err != nil && payload.StreamOptions != nil && isUnsupportedStreamOptions(err) {
		// Some compatible servers reject every unknown field. Retrying once
		// without the option keeps usage reporting for the servers that
		// support it without breaking the ones that do not.
		p.logger.Debug("retrying without stream_options",
			slog.String("provider", p.name), slog.String("error", err.Error()))
		payload.StreamOptions = nil
		response, cancel, err = p.post(ctx, payload)
	}
	if err != nil {
		return nil, err
	}

	return p.openStream(response, cancel, request.Model)
}

// post sends one request and returns the raw response together with the cancel
// function that owns its request deadline. The caller must call cancel when it
// stops reading the body.
func (p *Provider) post(ctx context.Context, payload *chatRequest) (*http.Response, context.CancelFunc, error) {
	encoded, err := provider.MarshalRequest(payload, payload.Extra)
	if err != nil {
		return nil, nil, err
	}
	if p.logger.Enabled(ctx, slog.LevelDebug) {
		// The body may contain repository content, so it is only logged at
		// debug level and never at the default level.
		p.logger.Debug("provider request",
			slog.String("provider", p.name),
			slog.String("model", payload.Model),
			slog.Int("messages", len(payload.Messages)),
			slog.Int("bytes", len(encoded)))
	}

	request, cancel, err := p.transport.NewRequest(ctx, http.MethodPost, chatPath,
		"application/json", bytes.NewReader(encoded))
	if err != nil {
		return nil, nil, err
	}

	response, err := p.transport.Client.Do(request)
	if err != nil {
		cancel()
		return nil, nil, provider.NetworkError("provider.generate", err, "")
	}

	if response.StatusCode >= 400 {
		body := provider.ReadErrorBody(response)
		_ = response.Body.Close()
		cancel()
		return nil, nil, provider.HTTPStatusError("provider.generate", response, body)
	}

	return response, cancel, nil
}

// openStream turns an HTTP response into a stream of protocol events. A server
// that ignores `stream: true` and answers with a single JSON document is
// handled by synthesising the equivalent event sequence, so callers do not
// have to care which behaviour they get.
func (p *Provider) openStream(response *http.Response, cancel context.CancelFunc, model string) (llm.Stream, error) {
	contentType := response.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/event-stream") {
		return newSSEStream(response, model, p.logger, cancel), nil
	}

	body := response.Body
	defer func() {
		_ = body.Close()
		cancel()
	}()

	var completion chatCompletion
	if err := json.NewDecoder(io.LimitReader(body, maxErrorBodyBytes*16)).Decode(&completion); err != nil {
		return nil, apperrors.Wrap(apperrors.KindProvider, "provider.generate",
			"the response is neither an event stream nor a JSON completion", err)
	}

	events, err := completionEvents(&completion, model)
	if err != nil {
		return nil, err
	}

	return provider.NewBufferedStream(events, cancel, nil), nil
}

// buildRequest maps the protocol request onto the wire format.
func (p *Provider) buildRequest(request *llm.GenerateRequest, stream bool) (*chatRequest, error) {
	messages, err := toChatMessages(request.Messages)
	if err != nil {
		return nil, err
	}

	payload := &chatRequest{
		Model:       request.Model,
		Messages:    messages,
		MaxTokens:   request.MaxTokens,
		Temperature: request.Temperature,
		TopP:        request.TopP,
		Stop:        request.Stop,
		Stream:      stream,
	}

	if len(request.Tools) > 0 {
		payload.Tools = toChatTools(request.Tools)
		payload.ToolChoice = toToolChoice(request.ToolChoice)
	}

	if request.ResponseSchema != nil {
		schema := map[string]any(*request.ResponseSchema)
		payload.ResponseFormat = &chatResponseFormat{
			Type: "json_schema",
			JSONSchema: &chatJSONSchema{
				Name:   request.ResponseSchemaName,
				Schema: schema,
				Strict: true,
			},
		}
	}

	if stream && p.includeUsage {
		payload.StreamOptions = &chatStreamOptions{IncludeUsage: true}
	}

	payload.Extra = mergeExtra(p.transport.ExtraBody, request.Extra)

	return payload, nil
}

// marshalRequest encodes the payload and merges the extra fields.
//
// Extras are merged after encoding so that a user can add a field the
// protocol does not model, or override one the protocol sets, without a code
// change. A conflicting extra always wins, because it is the more specific
// instruction.

// encode merges the provider extra_body and the request extra options and
// marshals the payload. ExtraBody is applied first so that a per-request value
// wins.
func mergeExtra(base map[string]any, overrides map[string]any) map[string]any {
	if len(base) == 0 && len(overrides) == 0 {
		return nil
	}
	merged := make(map[string]any, len(base)+len(overrides))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range overrides {
		merged[key] = value
	}
	return merged
}

// toChatMessages converts protocol messages into the wire format.
//
// Reasoning content is intentionally dropped: providers either reject it on
// input or re-bill it, and it is not needed to continue a conversation.
func toChatMessages(messages []llm.Message) ([]chatMessage, error) {
	out := make([]chatMessage, 0, len(messages))

	for _, message := range messages {
		converted := chatMessage{Name: message.Name}

		switch message.Role {
		case llm.RoleSystem:
			converted.Role = "system"
			converted.Content = message.Text()
		case llm.RoleUser:
			converted.Role = "user"
			content, err := toUserContent(message)
			if err != nil {
				return nil, err
			}
			converted.Content = content
		case llm.RoleAssistant:
			converted.Role = "assistant"
			text := message.Text()
			if text != "" {
				converted.Content = text
			}
			for _, call := range message.ToolCalls {
				converted.ToolCalls = append(converted.ToolCalls, chatToolCall{
					ID:   call.ID,
					Type: "function",
					Function: chatFunctionCall{
						Name:      call.Name,
						Arguments: call.Arguments,
					},
				})
			}
		case llm.RoleTool:
			if message.ToolResult == nil {
				return nil, apperrors.New(apperrors.KindInvalidArgument, "provider.generate",
					"a tool message must carry a tool result")
			}
			converted.Role = "tool"
			converted.ToolCallID = message.ToolResult.ToolCallID
			content := message.ToolResult.Content
			if message.ToolResult.IsError {
				// Tell the model the call failed, otherwise a permission error
				// or a missing file can be mistaken for a valid empty result.
				content = "[tool error] " + content
			}
			converted.Content = content
		default:
			return nil, apperrors.New(apperrors.KindInvalidArgument, "provider.generate",
				"unsupported message role %q", message.Role)
		}

		out = append(out, converted)
	}

	return out, nil
}

// toUserContent produces either a plain string, which every compatible server
// accepts, or the content part array that image input requires.
func toUserContent(message llm.Message) (any, error) {
	hasImage := false
	for _, part := range message.Content {
		if part.Kind == llm.ContentImage {
			hasImage = true
			break
		}
	}
	if !hasImage {
		return message.Text(), nil
	}

	parts := make([]chatContentPart, 0, len(message.Content))
	for _, part := range message.Content {
		switch part.Kind {
		case llm.ContentText:
			if part.Text == "" {
				continue
			}
			parts = append(parts, chatContentPart{Type: "text", Text: part.Text})
		case llm.ContentImage:
			if part.Image == nil {
				return nil, apperrors.New(apperrors.KindInvalidArgument, "provider.generate",
					"an image content part is missing its payload")
			}
			url := part.Image.URL
			if url == "" {
				url = provider.DataURL(part.Image.MIMEType, part.Image.Data)
			}
			parts = append(parts, chatContentPart{
				Type:     "image_url",
				ImageURL: &chatImageURL{URL: url, Detail: part.Image.Detail},
			})
		case llm.ContentThinking:
			// Reasoning is never sent back to the provider.
		}
	}
	return parts, nil
}

// toChatTools converts tool definitions into the wire format.
func toChatTools(definitions []llm.ToolDefinition) []chatTool {
	out := make([]chatTool, 0, len(definitions))
	for _, definition := range definitions {
		out = append(out, chatTool{
			Type: "function",
			Function: chatFunction{
				Name:        definition.Name,
				Description: definition.Description,
				Parameters:  map[string]any(definition.InputSchema),
				Strict:      definition.Strict,
			},
		})
	}
	return out
}

// toToolChoice converts the tool choice into the wire format. The zero value
// is omitted so that servers which only understand "auto" are not sent a field
// they may reject.
func toToolChoice(choice llm.ToolChoice) any {
	switch choice.Mode {
	case "", llm.ToolChoiceAuto:
		return nil
	case llm.ToolChoiceNone:
		return "none"
	case llm.ToolChoiceRequired:
		return "required"
	case llm.ToolChoiceFunction:
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": choice.Name},
		}
	default:
		return nil
	}
}

// isUnsupportedStreamOptions reports whether a failure was caused by the
// stream_options field, which a single retry can work around.
func isUnsupportedStreamOptions(err error) bool {
	var classified *apperrors.Error
	if !errors.As(err, &classified) {
		return false
	}
	status, ok := classified.Details["status"].(int)
	if !ok || status != http.StatusBadRequest {
		return false
	}
	return strings.Contains(strings.ToLower(classified.Message), "stream_options")
}
