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

package openairesponses

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

// streamEvent is one Server-Sent Event of a streamed response.
//
// Every event carries a type and the fields that apply to it, so one struct
// covers the whole stream. The final events repeat the complete response,
// which is why the event carries a full responsesResponse rather than a delta.
type streamEvent struct {
	Type           string             `json:"type"`
	SequenceNumber int                `json:"sequence_number"`
	OutputIndex    int                `json:"output_index"`
	ContentIndex   int                `json:"content_index"`
	ItemID         string             `json:"item_id"`
	Delta          string             `json:"delta"`
	Text           string             `json:"text"`
	Arguments      string             `json:"arguments"`
	Item           *outputItem        `json:"item"`
	Response       *responsesResponse `json:"response"`
	Error          *wireError         `json:"error"`
	Message        string             `json:"message"`
	Code           string             `json:"code"`
}

// callState tracks one function call as it streams.
type callState struct {
	callID       string
	name         string
	sawArguments bool
}

// sseStream converts the Responses event stream into protocol events.
type sseStream struct {
	reader *provider.SSEReader
	cancel context.CancelFunc
	logger *slog.Logger
	model  string

	pending []llm.Event
	calls   map[int]*callState

	finishReason llm.FinishReason
	sawToolCall  bool
	started      bool
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
		reader: provider.NewSSEReader(response),
		cancel: cancel,
		logger: logger,
		model:  model,
		calls:  map[int]*callState{},
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
	case "response.created", "response.in_progress":
		if event.Response != nil && event.Response.Model != "" {
			s.model = event.Response.Model
		}
		if !s.started {
			s.started = true
			s.pending = append(s.pending, llm.NewStartEvent(s.model))
		}

	case "response.output_item.added":
		if item := event.Item; item != nil && item.Type == "function_call" {
			s.calls[event.OutputIndex] = &callState{callID: item.CallID, name: item.Name}
			s.sawToolCall = true
		}

	case "response.function_call_arguments.delta":
		state := s.calls[event.OutputIndex]
		if state == nil {
			state = &callState{}
			s.calls[event.OutputIndex] = state
		}
		s.sawToolCall = true

		// The identifier and the name are attached to the first fragment only,
		// which is what llm.Collect expects.
		callID, name := "", ""
		if !state.sawArguments {
			callID, name = state.callID, state.name
			state.sawArguments = true
		}
		s.pending = append(s.pending, llm.NewToolCallDeltaEvent(
			event.OutputIndex, callID, name, event.Delta))

	case "response.function_call_arguments.done":
		state := s.calls[event.OutputIndex]
		if state == nil {
			state = &callState{}
			s.calls[event.OutputIndex] = state
		}
		s.sawToolCall = true

		// A server that sends only the completed arguments never emitted
		// deltas, so the call itself has to be reported here.
		if !state.sawArguments {
			state.sawArguments = true
			s.pending = append(s.pending, llm.NewToolCallEvent(event.OutputIndex, llm.ToolCall{
				ID:        state.callID,
				Name:      state.name,
				Arguments: normaliseArguments(event.Arguments),
			}))
		}

	case "response.output_text.delta", "response.refusal.delta":
		if event.Delta != "" {
			s.pending = append(s.pending, llm.NewTextDeltaEvent(event.Delta))
		}

	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if event.Delta != "" {
			s.pending = append(s.pending, llm.NewThinkingDeltaEvent(event.Delta))
		}

	case "response.completed":
		s.completed(event, llm.FinishReasonStop)

	case "response.incomplete":
		reason := llm.FinishReasonLength
		if event.Response != nil && event.Response.IncompleteDetails != nil {
			reason = mapIncompleteReason(event.Response.IncompleteDetails.Reason)
		}
		s.completed(event, reason)

	case "response.failed":
		return failedError(event, "the response failed")

	case "error":
		return failedError(event, "the stream failed")

	default:
		// Content part deltas, output item completions and the reasoning
		// summary lifecycle carry nothing the runtime does not already have.
		s.logger.Debug("ignoring an event", slog.String("event", event.Type))
	}

	return nil
}

// completed finishes the stream from a terminal event.
//
// The terminal event repeats the whole response, including every output item,
// so only the usage and the stop reason are read: re-emitting the content would
// duplicate the answer the deltas already delivered.
func (s *sseStream) completed(event *streamEvent, reason llm.FinishReason) {
	if event.Response != nil {
		if usage := toUsage(event.Response.Usage); !usage.IsZero() {
			s.pending = append(s.pending, llm.NewUsageEvent(usage))
		}
		if event.Response.Model != "" {
			s.model = event.Response.Model
		}
		if reason == llm.FinishReasonStop && responseHasToolCalls(event.Response) {
			reason = llm.FinishReasonToolCalls
		}
	}
	if reason == llm.FinishReasonStop && s.sawToolCall {
		reason = llm.FinishReasonToolCalls
	}

	s.finishReason = reason
	s.terminated = true
	s.pending = append(s.pending, s.finalEvent())
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

// responseHasToolCalls reports whether a completed response asked for a tool.
func responseHasToolCalls(response *responsesResponse) bool {
	if response == nil {
		return false
	}
	for _, item := range response.Output {
		if item.Type == "function_call" {
			return true
		}
	}
	return false
}

// failedError converts a failure event into a classified error.
func failedError(event *streamEvent, fallback string) error {
	message := fallback
	kind := apperrors.KindProvider

	source := event.Error
	if source == nil && event.Response != nil {
		source = event.Response.Error
	}

	if source != nil {
		if text := provider.SanitizeMessage(source.Message); text != "" {
			message = text
		}
		switch source.Code {
		case "invalid_api_key", "authentication_error":
			kind = apperrors.KindAuthentication
		case "not_found", "model_not_found":
			kind = apperrors.KindNotFound
		}
	} else if text := provider.SanitizeMessage(event.Message); text != "" {
		message = text
		switch event.Code {
		case "invalid_api_key":
			kind = apperrors.KindAuthentication
		}
	}

	return apperrors.New(kind, "provider.stream", "%s", message)
}

// completionEvents converts a complete response into the equivalent event
// sequence, so that a server which ignores `stream: true` behaves identically
// from the caller's point of view.
func completionEvents(response *responsesResponse, model string) ([]llm.Event, error) {
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
	index := 0

	for _, item := range response.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				switch {
				case part.Type == "output_text" && part.Text != "":
					events = append(events, llm.NewTextDeltaEvent(part.Text))
				case part.Type == "refusal" && part.Refusal != "":
					events = append(events, llm.NewTextDeltaEvent(part.Refusal))
				}
			}

		case "reasoning":
			for _, summary := range item.Summary {
				if summary.Text != "" {
					events = append(events, llm.NewThinkingDeltaEvent(summary.Text))
				}
			}

		case "function_call":
			sawToolCall = true
			events = append(events, llm.NewToolCallEvent(index, llm.ToolCall{
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: normaliseArguments(item.Arguments),
			}))
		}
		index++
	}

	reason := llm.FinishReasonStop
	switch response.Status {
	case "incomplete":
		reason = llm.FinishReasonLength
		if response.IncompleteDetails != nil {
			reason = mapIncompleteReason(response.IncompleteDetails.Reason)
		}
	case "failed":
		return nil, apperrors.New(apperrors.KindProvider, "provider.generate",
			"the provider reported a failed response")
	}
	if reason == llm.FinishReasonStop && sawToolCall {
		reason = llm.FinishReasonToolCalls
	}

	events = append(events, llm.NewFinishEvent(reason, model))
	return events, nil
}
