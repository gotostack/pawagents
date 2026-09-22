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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/llm"
)

// sseStream converts a Server-Sent Events response into protocol events.
//
// Two details drive the implementation:
//
//   - the terminal chunk is `data: [DONE]`, and the usage report usually
//     arrives in the chunk before it, so the stream must keep reading after
//     finish_reason is seen;
//   - a finish event is emitted exactly once, at the end, so that usage always
//     precedes it and the agent loop sees a well formed sequence.
type sseStream struct {
	body   io.ReadCloser
	reader *bufio.Scanner
	cancel context.CancelFunc
	logger *slog.Logger
	model  string

	pending      []llm.Event
	finishReason llm.FinishReason
	sawToolCall  bool
	currentIndex int
	toolCount    int
	terminated   bool
	closed       bool
}

// newSSEStream wraps a streaming HTTP response.
func newSSEStream(
	response *http.Response,
	model string,
	logger *slog.Logger,
	cancel context.CancelFunc,
) llm.Stream {
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxEventBytes)

	if logger == nil {
		logger = slog.Default()
	}
	if cancel == nil {
		cancel = func() {}
	}

	return &sseStream{
		body:   response.Body,
		reader: scanner,
		cancel: cancel,
		logger: logger,
		model:  model,
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

		line, err := s.readDataLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				s.terminated = true
				return s.finalEvent(), nil
			}
			s.terminated = true
			return llm.Event{}, err
		}

		if line == "[DONE]" {
			s.terminated = true
			return s.finalEvent(), nil
		}

		var chunk chatCompletion
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			s.terminated = true
			return llm.Event{}, apperrors.Wrap(apperrors.KindProvider, "provider.stream",
				"the provider sent an event that is not valid JSON", err)
		}

		events, err := s.chunkEvents(&chunk)
		if err != nil {
			s.terminated = true
			return llm.Event{}, err
		}
		s.pending = events
	}
}

// Close implements llm.Stream.
func (s *sseStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	s.cancel()

	if s.body == nil {
		return nil
	}
	if err := s.body.Close(); err != nil && !errors.Is(err, http.ErrBodyReadAfterClose) {
		return err
	}
	return nil
}

// readDataLine returns the payload of the next `data:` line, skipping blank
// lines, comments (which servers use as keep-alives) and `event:` lines.
func (s *sseStream) readDataLine() (string, error) {
	for s.reader.Scan() {
		line := strings.TrimRight(s.reader.Text(), "\r")
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, ":"):
			continue
		case strings.HasPrefix(line, "event:"), strings.HasPrefix(line, "id:"),
			strings.HasPrefix(line, "retry:"):
			continue
		case strings.HasPrefix(line, "data:"):
			return strings.TrimSpace(strings.TrimPrefix(line, "data:")), nil
		default:
			continue
		}
	}

	if err := s.reader.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return "", apperrors.Wrap(apperrors.KindProvider, "provider.stream",
				"the provider sent an event larger than the accepted limit", err)
		}
		return "", apperrors.Wrap(apperrors.KindProvider, "provider.stream",
			"cannot read the provider event stream", err)
	}
	return "", io.EOF
}

// chunkEvents converts one streaming chunk into protocol events.
func (s *sseStream) chunkEvents(chunk *chatCompletion) ([]llm.Event, error) {
	if chunk.Error != nil {
		return nil, apperrors.New(apperrors.KindProvider, "provider.stream",
			"the provider reported an error mid-stream: %s", sanitizeMessage(chunk.Error.Message))
	}

	var events []llm.Event

	if chunk.Model != "" {
		s.model = chunk.Model
	}
	if chunk.Usage != nil {
		usage := toUsage(chunk.Usage)
		if !usage.IsZero() {
			events = append(events, llm.NewUsageEvent(usage))
		}
	}

	for _, choice := range chunk.Choices {
		if delta := choice.Delta; delta != nil {
			if delta.Content != "" {
				events = append(events, llm.NewTextDeltaEvent(delta.Content))
			}
			reasoning := delta.ReasoningContent
			if reasoning == "" {
				reasoning = delta.Reasoning
			}
			if reasoning != "" {
				events = append(events, llm.NewThinkingDeltaEvent(reasoning))
			}
			for _, call := range delta.ToolCalls {
				events = append(events, llm.NewToolCallDeltaEvent(
					s.toolCallIndex(call), call.ID, call.Function.Name, call.Function.Arguments))
			}
		}
		if choice.FinishReason != "" {
			s.finishReason = mapFinishReason(choice.FinishReason)
		}
	}

	return events, nil
}

// toolCallIndex returns the index of a streaming tool call fragment. Servers
// that omit the index receive one derived from the order in which new calls
// appear, so fragments still merge correctly in llm.Collect.
func (s *sseStream) toolCallIndex(call chatToolCall) int {
	if call.Index != nil {
		s.sawToolCall = true
		return *call.Index
	}
	if call.ID != "" || call.Function.Name != "" {
		s.currentIndex = s.toolCount
		s.toolCount++
	}
	s.sawToolCall = true
	return s.currentIndex
}

// finalEvent builds the terminal event of the stream.
func (s *sseStream) finalEvent() llm.Event {
	reason := s.finishReason
	if reason == "" || reason == llm.FinishReasonUnspecified {
		if s.sawToolCall {
			reason = llm.FinishReasonToolCalls
		} else {
			reason = llm.FinishReasonStop
		}
	}
	return llm.NewFinishEvent(reason, s.model)
}

// completionEvents converts a complete, non streaming response into the
// equivalent event sequence, so that a server which ignores `stream: true`
// behaves identically from the caller's point of view.
func completionEvents(completion *chatCompletion, model string) ([]llm.Event, error) {
	if completion == nil {
		return nil, apperrors.New(apperrors.KindProvider, "provider.generate",
			"the provider returned an empty response")
	}

	if completion.Error != nil {
		return nil, apperrors.New(apperrors.KindProvider, "provider.generate",
			"the provider returned an error response: %s", sanitizeMessage(completion.Error.Message))
	}
	if completion.Model != "" {
		model = completion.Model
	}

	events := []llm.Event{llm.NewStartEvent(model)}
	if completion.Usage != nil {
		events = append(events, llm.NewUsageEvent(toUsage(completion.Usage)))
	}

	reason := llm.FinishReasonUnspecified
	sawToolCall := false

	for _, choice := range completion.Choices {
		message := choice.Message
		if message != nil {
			switch content := message.Content.(type) {
			case string:
				if content != "" {
					events = append(events, llm.NewTextDeltaEvent(content))
				}
			case []any:
				// Some servers answer with the content part array even for a
				// plain text reply.
				for _, part := range content {
					object, ok := part.(map[string]any)
					if !ok {
						continue
					}
					if text, ok := object["text"].(string); ok && text != "" {
						events = append(events, llm.NewTextDeltaEvent(text))
					}
				}
			}

			for index, call := range message.ToolCalls {
				sawToolCall = true
				arguments := call.Function.Arguments
				if arguments == "" {
					arguments = "{}"
				}
				events = append(events, llm.NewToolCallEvent(index, llm.ToolCall{
					ID:        call.ID,
					Name:      call.Function.Name,
					Arguments: arguments,
				}))
			}
		}
		if choice.FinishReason != "" {
			reason = mapFinishReason(choice.FinishReason)
		}
	}

	if reason == llm.FinishReasonUnspecified {
		if sawToolCall {
			reason = llm.FinishReasonToolCalls
		} else {
			reason = llm.FinishReasonStop
		}
	}
	events = append(events, llm.NewFinishEvent(reason, model))

	return events, nil
}

// mapFinishReason translates a provider finish reason.
//
// An unknown reason maps to "stop" rather than to an error: compatible servers
// invent their own reasons, and treating one of them as a protocol failure
// would break an otherwise successful generation.
func mapFinishReason(reason string) llm.FinishReason {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "":
		return llm.FinishReasonUnspecified
	case "stop", "end_turn", "eos":
		return llm.FinishReasonStop
	case "length", "max_tokens":
		return llm.FinishReasonLength
	case "tool_calls", "function_call", "tool_use":
		return llm.FinishReasonToolCalls
	case "content_filter":
		return llm.FinishReasonContentFilter
	default:
		return llm.FinishReasonStop
	}
}

// toUsage converts provider token accounting.
func toUsage(usage *chatUsage) llm.Usage {
	if usage == nil {
		return llm.Usage{}
	}
	converted := llm.Usage{
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
		TotalTokens:  usage.TotalTokens,
	}
	if usage.PromptTokensDetails != nil {
		converted.CacheReadTokens = usage.PromptTokensDetails.CachedTokens
	}
	if usage.CompletionTokensDetails != nil {
		converted.ReasoningTokens = usage.CompletionTokensDetails.ReasoningTokens
	}
	return converted
}

// bufferedStream replays a fixed event list and releases the request context
// when it is closed. It is used for responses that arrive as a single JSON
// document instead of an event stream.
type bufferedStream struct {
	events []llm.Event
	index  int
	cancel context.CancelFunc
	body   io.ReadCloser
	closed bool
}

func newBufferedStream(events []llm.Event, cancel context.CancelFunc, body io.ReadCloser) llm.Stream {
	if cancel == nil {
		cancel = func() {}
	}
	return &bufferedStream{events: events, cancel: cancel, body: body}
}

// Recv implements llm.Stream.
func (s *bufferedStream) Recv() (llm.Event, error) {
	if s.index >= len(s.events) {
		return llm.Event{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

// Close implements llm.Stream.
func (s *bufferedStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	s.cancel()
	if s.body != nil {
		return s.body.Close()
	}
	return nil
}
