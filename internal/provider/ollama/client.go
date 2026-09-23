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

package ollama

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/llm"
	"github.com/pawagents/pawagents/internal/provider"
)

const (
	// chatPath is the chat endpoint relative to the base URL.
	chatPath = "api/chat"
	// tagsPath lists the installed models.
	tagsPath = "api/tags"
	// showPath reports the metadata and capabilities of a model.
	showPath = "api/show"
	// versionPath reports the server version.
	versionPath = "api/version"

	// ollamaHint explains the most common failure, which is a server that is
	// not running at all.
	ollamaHint = "is `ollama serve` running?"

	// probeTimeout bounds the metadata calls, which are not generations.
	probeTimeout = 15 * time.Second

	// maxResponseBytes bounds a metadata response body.
	maxResponseBytes = 1 << 20

	// maxLineBytes bounds one newline delimited JSON object.
	maxLineBytes = 4 << 20

	// keepAlive controls how long a model stays loaded after a request. A
	// long lived value would keep several models resident and exhaust memory
	// on a developer machine, so a short one is chosen explicitly.
	keepAlive = "5m"
)

// Generate implements provider.Provider.
func (p *Provider) Generate(ctx context.Context, request *llm.GenerateRequest) (llm.Stream, error) {
	if request == nil {
		return nil, apperrors.New(apperrors.KindInvalidArgument, "provider.generate",
			"the generation request is nil")
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}

	payload, extras, err := p.buildRequest(request)
	if err != nil {
		return nil, err
	}

	encoded, err := marshalRequest(payload, extras)
	if err != nil {
		return nil, err
	}

	if p.logger.Enabled(ctx, slog.LevelDebug) {
		p.logger.Debug("provider request",
			slog.String("provider", p.name),
			slog.String("model", payload.Model),
			slog.Int("messages", len(payload.Messages)),
			slog.Int("bytes", len(encoded)))
	}

	httpRequest, cancel, err := p.transport.NewRequest(ctx, http.MethodPost, chatPath,
		"application/json", bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}

	response, err := p.transport.Client.Do(httpRequest)
	if err != nil {
		cancel()
		return nil, provider.NetworkError("provider.generate", err, ollamaHint)
	}

	if response.StatusCode >= 400 {
		body := provider.ReadErrorBody(response)
		_ = response.Body.Close()
		cancel()
		return nil, provider.HTTPStatusError("provider.generate", response, body)
	}

	return newNDJSONStream(response, request.Model, p.logger, cancel), nil
}

// buildRequest maps the protocol request onto the native Ollama request.
func (p *Provider) buildRequest(request *llm.GenerateRequest) (*chatRequest, map[string]any, error) {
	messages, err := toChatMessages(request.Messages)
	if err != nil {
		return nil, nil, err
	}

	streaming := true
	payload := &chatRequest{
		Model:     request.Model,
		Messages:  messages,
		Stream:    &streaming,
		KeepAlive: keepAlive,
	}

	if len(request.Tools) > 0 {
		payload.Tools = toChatTools(request.Tools)
	}

	if request.ResponseSchema != nil {
		schema, err := json.Marshal(map[string]any(*request.ResponseSchema))
		if err != nil {
			return nil, nil, apperrors.Wrap(apperrors.KindInternal, "provider.generate",
				"cannot encode the response schema", err)
		}
		payload.Format = schema
	}

	options := map[string]any{}
	if request.MaxTokens > 0 {
		options["num_predict"] = request.MaxTokens
	}
	if request.Temperature != nil {
		options["temperature"] = *request.Temperature
	}
	if request.TopP != nil {
		options["top_p"] = *request.TopP
	}
	if len(request.Stop) > 0 {
		options["stop"] = request.Stop
	}
	if len(options) > 0 {
		payload.Options = options
	}

	extras := mergeExtra(p.transport.ExtraBody, request.Extra)
	return payload, extras, nil
}

// toChatMessages converts protocol messages into the native format.
func toChatMessages(messages []llm.Message) ([]chatMessage, error) {
	out := make([]chatMessage, 0, len(messages))

	for _, message := range messages {
		converted := chatMessage{Role: string(message.Role)}

		switch message.Role {
		case llm.RoleSystem, llm.RoleUser:
			converted.Content = message.Text()
			if message.Role == llm.RoleUser {
				images, err := toImages(message)
				if err != nil {
					return nil, err
				}
				converted.Images = images
			}
		case llm.RoleAssistant:
			converted.Content = message.Text()
			for _, call := range message.ToolCalls {
				arguments, err := toolArguments(call)
				if err != nil {
					return nil, err
				}
				converted.ToolCalls = append(converted.ToolCalls, chatToolCall{
					Function: chatFunctionCall{Name: call.Name, Arguments: arguments},
				})
			}
		case llm.RoleTool:
			if message.ToolResult == nil {
				return nil, apperrors.New(apperrors.KindInvalidArgument, "provider.generate",
					"a tool message must carry a tool result")
			}
			converted.Content = message.ToolResult.Content
			if message.ToolResult.IsError {
				converted.Content = "[tool error] " + converted.Content
			}
			converted.ToolName = message.ToolResult.Name
		default:
			return nil, apperrors.New(apperrors.KindInvalidArgument, "provider.generate",
				"unsupported message role %q", message.Role)
		}

		out = append(out, converted)
	}

	return out, nil
}

// toImages extracts the inline images of a message. Ollama accepts base64
// payloads without a data URL prefix and cannot fetch a remote URL.
func toImages(message llm.Message) ([]string, error) {
	var images []string
	for _, part := range message.Content {
		if part.Kind != llm.ContentImage {
			continue
		}
		if part.Image == nil {
			return nil, apperrors.New(apperrors.KindInvalidArgument, "provider.generate",
				"an image content part is missing its payload")
		}
		if len(part.Image.Data) == 0 {
			return nil, apperrors.New(apperrors.KindCapability, "provider.generate",
				"a local Ollama server cannot fetch an image URL; send the image bytes instead")
		}
		images = append(images, base64.StdEncoding.EncodeToString(part.Image.Data))
	}
	return images, nil
}

// toolArguments converts the JSON string of a tool call into the object shape
// Ollama expects.
func toolArguments(call llm.ToolCall) (map[string]any, error) {
	raw := strings.TrimSpace(call.Arguments)
	if raw == "" {
		return map[string]any{}, nil
	}
	var arguments map[string]any
	if err := json.Unmarshal([]byte(raw), &arguments); err != nil {
		return nil, apperrors.Wrap(apperrors.KindInvalidArgument, "provider.generate",
			"tool call %q arguments are not a JSON object", err, call.Name)
	}
	if arguments == nil {
		arguments = map[string]any{}
	}
	return arguments, nil
}

// toChatTools converts tool definitions into the native format.
func toChatTools(definitions []llm.ToolDefinition) []chatTool {
	out := make([]chatTool, 0, len(definitions))
	for _, definition := range definitions {
		out = append(out, chatTool{
			Type: "function",
			Function: chatFunction{
				Name:        definition.Name,
				Description: definition.Description,
				Parameters:  map[string]any(definition.InputSchema),
			},
		})
	}
	return out
}

// mergeExtra merges the provider extra_body with the per-request extras, the
// per-request value winning.
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

// marshalRequest encodes the payload and merges the extra fields, which allows
// an operator to reach a server option the protocol does not model.
func marshalRequest(payload *chatRequest, extras map[string]any) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, apperrors.Wrap(apperrors.KindInternal, "provider.request",
			"cannot encode the request body", err)
	}
	if len(extras) == 0 {
		return encoded, nil
	}

	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		return nil, apperrors.Wrap(apperrors.KindInternal, "provider.request",
			"cannot merge the extra request fields", err)
	}
	for key, value := range extras {
		object[key] = value
	}

	merged, err := json.Marshal(object)
	if err != nil {
		return nil, apperrors.Wrap(apperrors.KindInternal, "provider.request",
			"cannot encode the merged request body", err)
	}
	return merged, nil
}

// fetchMetadata reads /api/show for a model.
func (p *Provider) fetchMetadata(ctx context.Context, model string) (modelMetadata, error) {
	body, err := json.Marshal(map[string]string{"model": model})
	if err != nil {
		return modelMetadata{}, apperrors.Wrap(apperrors.KindInternal, "provider.metadata",
			"cannot encode the model request", err)
	}

	requestCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	request, requestCancel, err := p.transport.NewRequest(requestCtx, http.MethodPost, showPath,
		"application/json", bytes.NewReader(body))
	if err != nil {
		return modelMetadata{}, err
	}
	defer requestCancel()

	response, err := p.transport.Client.Do(request)
	if err != nil {
		return modelMetadata{}, provider.NetworkError("provider.metadata", err, ollamaHint)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode >= 400 {
		raw := provider.ReadErrorBody(response)
		if response.StatusCode == http.StatusNotFound {
			return modelMetadata{}, apperrors.New(apperrors.KindNotFound, "provider.metadata",
				"model %q is not installed on provider %q; run `ollama pull %s`",
				model, p.name, model).WithDetails(map[string]any{"model": model})
		}
		return modelMetadata{}, provider.HTTPStatusError("provider.metadata", response, raw)
	}

	var shown showResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&shown); err != nil {
		return modelMetadata{}, apperrors.Wrap(apperrors.KindProvider, "provider.metadata",
			"the model metadata is not valid JSON", err)
	}

	return metadataFromShow(shown), nil
}

// metadataFromShow turns an /api/show response into the metadata used for
// capability detection.
func metadataFromShow(shown showResponse) modelMetadata {
	metadata := modelMetadata{
		capabilities:  append([]string(nil), shown.Capabilities...),
		contextLength: contextLengthFromInfo(shown.ModelInfo),
	}
	if family, ok := shown.Details["family"].(string); ok {
		metadata.family = family
	}
	if size, ok := shown.Details["parameter_size"].(string); ok {
		metadata.parameterSize = size
	}
	return metadata
}

// contextLengthFromInfo finds the context window in the model metadata. The
// key is architecture prefixed ("qwen2.context_length", "llama.context_length",
// ...), so the suffix is matched rather than the exact name.
func contextLengthFromInfo(info map[string]any) int {
	longest := 0
	for key, value := range info {
		if !strings.HasSuffix(key, ".context_length") {
			continue
		}
		length := toInt(value)
		if length > longest {
			longest = length
		}
	}
	return longest
}

// toInt converts a JSON number into an int, tolerating the float form that
// encoding/json produces.
func toInt(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0
		}
		return int(parsed)
	case string:
		parsed, err := strconv.Atoi(typed)
		if err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}

// fetchTags reads the installed model list.
func (p *Provider) fetchTags(ctx context.Context) ([]tagEntry, error) {
	requestCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	request, requestCancel, err := p.transport.NewRequest(requestCtx, http.MethodGet, tagsPath, "", nil)
	if err != nil {
		return nil, err
	}
	defer requestCancel()

	response, err := p.transport.Client.Do(request)
	if err != nil {
		return nil, provider.NetworkError("provider.models", err, ollamaHint)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode >= 400 {
		return nil, provider.HTTPStatusError("provider.models", response, provider.ReadErrorBody(response))
	}

	var tags tagsResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&tags); err != nil {
		return nil, apperrors.Wrap(apperrors.KindProvider, "provider.models",
			"the model list is not valid JSON", err)
	}
	return tags.Models, nil
}

// fetchVersion reads the server version.
func (p *Provider) fetchVersion(ctx context.Context) (string, error) {
	requestCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	request, requestCancel, err := p.transport.NewRequest(requestCtx, http.MethodGet, versionPath, "", nil)
	if err != nil {
		return "", err
	}
	defer requestCancel()

	response, err := p.transport.Client.Do(request)
	if err != nil {
		return "", provider.NetworkError("provider.health", err, ollamaHint)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode >= 400 {
		return "", provider.HTTPStatusError("provider.health", response, provider.ReadErrorBody(response))
	}

	var version versionResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&version); err != nil {
		return "", apperrors.Wrap(apperrors.KindProvider, "provider.health",
			"the version response is not valid JSON", err)
	}
	return version.Version, nil
}

// newNDJSONStream wraps a streaming response.
func newNDJSONStream(
	response *http.Response,
	model string,
	logger *slog.Logger,
	cancel context.CancelFunc,
) llm.Stream {
	if logger == nil {
		logger = slog.Default()
	}
	if cancel == nil {
		cancel = func() {}
	}

	return &ndjsonStream{
		body:    response.Body,
		decoder: json.NewDecoder(response.Body),
		cancel:  cancel,
		logger:  logger,
		model:   model,
	}
}

var _ provider.HealthReporter = (*Provider)(nil)
var _ provider.ModelLister = (*Provider)(nil)
