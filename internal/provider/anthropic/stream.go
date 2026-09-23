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

package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/llm"
	"github.com/pawagents/pawagents/internal/provider"
)

// streamEvent is one Server-Sent Event of a streamed message.
type streamEvent struct {
	Type         string            `json:"type"`
	Index        int               `json:"index"`
	Message      *messagesResponse `json:"message"`
	ContentBlock *wireBlock        `json:"content_block"`
	Delta        *streamDelta      `json:"delta"`
	Usage        *wireUsage        `json:"usage"`
	Error        *wireError        `json:"error"`
}

// streamDelta is the payload of a content_block_delta or message_delta event.
type streamDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	PartialJSON string `json:"partial_json"`
	Thinking    string `json:"thinking"`
	StopReason  string `json:"stop_reason"`
}

// toolCallState accumulates the fragments of one tool_use block.
type toolCallState struct {
	id           string
	name         string
	sawArguments bool
}

// sseStream converts the Anthropic event stream into protocol events.
//
// The stream reports usage in two places: message_start carries the input
// tokens and message_delta the cumulative output tokens. Since the runtime
// keeps the last usage event it sees, every usage event emitted here carries
// the accumulated totals rather than one half of the accounting.
type sseStream struct {
	reader *provider.SSEReader
	cancel context.CancelFunc
	logger *slog.Logger
	model  string

	pending   []llm.Event
	usage     llm.Usage
	toolState map[int]*toolCallState

	finishReason llm.FinishReason
	sawToolCall  bool
	terminated   bool
	closed       bool
}

// newSSEStream wraps a streaming response.
func newSSEStream(response io.ReadCloser, model string, logger *slog.Logger, cancel context.CancelFunc) llm.Stream {
	if logger == nil {
		logger = slog.Default()
	}
	if cancel == nil {
		cancel = func() {}
	}
	return &sseStream{
		reader:    provider.NewSSEReader(response),
		cancel:    cancel,
		logger:    logger,
		model:     model,
		toolState: map[int]*toolCallState{},
	}
}

// Recv implements llm.Stream.
func (s *sseStream) Recv() (llm.Event, error) {
	for {
		if len(s.pending) > 0 {
			event := s.pending[0]
			s.pending = s.pending[1:]
			return event, nil
		}
		if s.terminated {
			return llm.Event{}, io.EOF
		}

		_, data, err := s.reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				s.terminated = true
				return s.finalEvent(), nil
			}
			s.terminated = true
			return llm.Event{}, err
		}
		if provider.IsDone(data) {
			s.terminated = true
			return s.finalEvent(), nil
		}

		var event streamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			s.terminated = true
			return llm.Event{}, apperrors.Wrap(apperrors.KindProvider, "provider.stream",
				"the provider sent an event that is not valid JSON", err)
		}

		if err := s.handleEvent(&event); err != nil {
			s.terminated = true
			return llm.Event{}, err
		}
	}
}

// Close implements llm.Stream. It is safe to call more than once.
func (s *sseStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	s.cancel()
	return s.reader.Close()
}

// handleEvent turns one provider event into zero or more protocol events.
func (s *sseStream) handleEvent(event *streamEvent) error {
	switch event.Type {
	case "message_start":
		if event.Message != nil && event.Message.Model != "" {
			s.model = event.Message.Model
		}
		s.pending = append(s.pending, llm.NewStartEvent(s.model))

		if event.Message != nil && event.Message.Usage != nil {
			s.usage = mergeUsage(s.usage, toUsage(event.Message.Usage))
			s.pending = append(s.pending, llm.NewUsageEvent(s.usage))
		}

	case "content_block_start":
		if block := event.ContentBlock; block != nil && block.Type == "tool_use" {
			state := &toolCallState{id: block.ID, name: block.Name}
			s.toolState[event.Index] = state

			// A model that answers with a complete input object instead of
			// streaming it produces no delta at all, so the block itself is
			// the call.
			if arguments := normaliseArguments(block.Input); arguments != "" && arguments != "{}" {
				state.sawArguments = true
				s.sawToolCall = true
				s.pending = append(s.pending, llm.NewToolCallEvent(event.Index, llm.ToolCall{
					ID:        block.ID,
					Name:      block.Name,
					Arguments: arguments,
				}))
			}
		}

	case "content_block_delta":
		if event.Delta == nil {
			return nil
		}
		switch event.Delta.Type {
		case "text_delta":
			if event.Delta.Text != "" {
				s.pending = append(s.pending, llm.NewTextDeltaEvent(event.Delta.Text))
			}
		case "thinking_delta":
			if event.Delta.Thinking != "" {
				s.pending = append(s.pending, llm.NewThinkingDeltaEvent(event.Delta.Thinking))
			}
		case "input_json_delta":
			state := s.toolState[event.Index]
			if state == nil {
				state = &toolCallState{}
				s.toolState[event.Index] = state
			}
			s.sawToolCall = true

			// The identifier and the name are only attached to the first
			// fragment, which is what llm.Collect expects.
			id, name := "", ""
			if !state.sawArguments {
				id, name = state.id, state.name
				state.sawArguments = true
			}
			s.pending = append(s.pending, llm.NewToolCallDeltaEvent(
				event.Index, id, name, event.Delta.PartialJSON))
		case "signature_delta":
			// The signature belongs to a thinking block the runtime does not
			// replay, so it is dropped rather than stored.
		}

	case "message_delta":
		if event.Delta != nil && event.Delta.StopReason != "" {
			s.finishReason = mapStopReason(event.Delta.StopReason)
		}
		if event.Usage != nil {
			s.usage = mergeUsage(s.usage, toUsage(event.Usage))
			s.pending = append(s.pending, llm.NewUsageEvent(s.usage))
		}

	case "message_stop":
		s.terminated = true
		s.pending = append(s.pending, s.finalEvent())

	case "ping":
		// A keep-alive, deliberately ignored.

	case "error":
		message := "the provider reported an error mid-stream"
		if event.Error != nil && event.Error.Message != "" {
			message = provider.SanitizeMessage(event.Error.Message)
		}
		kind := apperrors.KindProvider
		if event.Error != nil {
			switch event.Error.Type {
			case "authentication_error", "permission_error":
				kind = apperrors.KindAuthentication
			case "not_found_error":
				kind = apperrors.KindNotFound
			case "overloaded_error", "rate_limit_error":
				kind = apperrors.KindProvider
			}
		}
		return apperrors.New(kind, "provider.stream", "the stream failed: %s", message)

	default:
		s.logger.Debug("ignoring an unknown stream event",
			slog.String("event", event.Type))
	}

	return nil
}

// finalEvent closes the stream with a finish event.
func (s *sseStream) finalEvent() llm.Event {
	reason := s.finishReason
	if reason == llm.FinishReasonUnspecified {
		if s.sawToolCall {
			reason = llm.FinishReasonToolCalls
		} else {
			reason = llm.FinishReasonStop
		}
	}
	return llm.NewFinishEvent(reason, s.model)
}

// messageEvents converts a complete response into the equivalent event
// sequence, so that a server which ignores `stream: true` behaves identically
// from the caller's point of view.
func messageEvents(response *messagesResponse, model string) ([]llm.Event, error) {
	if response == nil {
		return nil, apperrors.New(apperrors.KindProvider, "provider.generate",
			"the provider returned an empty response")
	}
	if response.Error != nil && response.Error.Message != "" {
		return nil, apperrors.New(apperrors.KindProvider, "provider.generate",
			"the provider returned an error response: %s",
			provider.SanitizeMessage(response.Error.Message))
	}
	if response.Model != "" {
		model = response.Model
	}

	events := []llm.Event{llm.NewStartEvent(model)}
	if usage := toUsage(response.Usage); !usage.IsZero() {
		events = append(events, llm.NewUsageEvent(usage))
	}

	sawToolCall := false
	for index, block := range response.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				events = append(events, llm.NewTextDeltaEvent(block.Text))
			}
		case "thinking":
			if block.Thinking != "" {
				events = append(events, llm.NewThinkingDeltaEvent(block.Thinking))
			}
		case "tool_use":
			sawToolCall = true
			events = append(events, llm.NewToolCallEvent(index, llm.ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: normaliseArguments(block.Input),
			}))
		}
	}

	reason := mapStopReason(response.StopReason)
	if reason == llm.FinishReasonStop && sawToolCall {
		reason = llm.FinishReasonToolCalls
	}
	events = append(events, llm.NewFinishEvent(reason, model))

	return events, nil
}

// normaliseArguments returns the tool input as a JSON argument string. The API
// always sends an object, which the runtime expects as a string.
func normaliseArguments(input json.RawMessage) string {
	trimmed := ""
	if len(input) > 0 {
		trimmed = string(input)
	}
	if trimmed == "" || trimmed == "null" {
		return "{}"
	}
	return trimmed
}

// mergeUsage folds a partial usage report into the accumulated one.
//
// Anthropic reports the input tokens once, in message_start, and the output
// tokens cumulatively in every message_delta, so a field is only overwritten
// when the report actually carries it.
func mergeUsage(accumulated, update llm.Usage) llm.Usage {
	if update.InputTokens > 0 {
		accumulated.InputTokens = update.InputTokens
	}
	if update.OutputTokens > 0 {
		accumulated.OutputTokens = update.OutputTokens
	}
	if update.CacheReadTokens > 0 {
		accumulated.CacheReadTokens = update.CacheReadTokens
	}
	if update.CacheWriteTokens > 0 {
		accumulated.CacheWriteTokens = update.CacheWriteTokens
	}
	accumulated.TotalTokens = accumulated.InputTokens + accumulated.OutputTokens
	return accumulated
}
