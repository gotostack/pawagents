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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/llm"
)

// ndjsonStream converts the newline delimited JSON stream of /api/chat into
// protocol events.
//
// Two behaviours are worth documenting because they differ from the
// OpenAI-compatible family:
//
//   - tool calls arrive complete, in the final message, rather than as
//     argument fragments, and they carry no identifier, so a stable one is
//     synthesised for the agent loop to match results against;
//   - the token statistics arrive on the final object together with the stop
//     reason, so the usage event is emitted just before the finish event.
type ndjsonStream struct {
	body    io.ReadCloser
	decoder *json.Decoder
	cancel  context.CancelFunc
	logger  *slog.Logger
	model   string

	pending      []llm.Event
	finishReason llm.FinishReason
	sawToolCall  bool
	sawDelta     bool
	terminated   bool
	closed       bool
}

// Recv implements llm.Stream.
func (s *ndjsonStream) Recv() (llm.Event, error) {
	for {
		if len(s.pending) > 0 {
			event := s.pending[0]
			s.pending = s.pending[1:]
			return event, nil
		}
		if s.terminated {
			return llm.Event{}, io.EOF
		}

		var line chatResponse
		if err := s.decoder.Decode(&line); err != nil {
			if errors.Is(err, io.EOF) {
				s.terminated = true
				return s.finalEvent(), nil
			}
			s.terminated = true
			return llm.Event{}, apperrors.Wrap(apperrors.KindProvider, "provider.stream",
				"cannot decode the Ollama event stream", err)
		}

		events, err := s.lineEvents(&line)
		if err != nil {
			s.terminated = true
			return llm.Event{}, err
		}
		s.pending = events
	}
}

// Close implements llm.Stream.
func (s *ndjsonStream) Close() error {
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

// lineEvents converts one streamed object into protocol events.
func (s *ndjsonStream) lineEvents(line *chatResponse) ([]llm.Event, error) {
	if line == nil {
		return nil, nil
	}
	if strings.TrimSpace(line.Error) != "" {
		return nil, apperrors.New(apperrors.KindProvider, "provider.stream",
			"the Ollama server reported an error mid-stream: %s", sanitize(line.Error))
	}
	if line.Model != "" {
		s.model = line.Model
	}

	var events []llm.Event

	if message := line.Message; message != nil {
		if message.Thinking != "" {
			events = append(events, llm.NewThinkingDeltaEvent(message.Thinking))
		}
		// The final object repeats the message on some server versions. When
		// deltas were already streamed the content is not emitted twice.
		if message.Content != "" && !(line.Done && s.sawDelta) {
			events = append(events, llm.NewTextDeltaEvent(message.Content))
			s.sawDelta = true
		}
		for index, call := range message.ToolCalls {
			s.sawToolCall = true
			arguments, err := json.Marshal(call.Function.Arguments)
			if err != nil {
				return nil, apperrors.Wrap(apperrors.KindProvider, "provider.stream",
					"cannot encode the arguments of tool call %q", err, call.Function.Name)
			}
			events = append(events, llm.NewToolCallEvent(index, llm.ToolCall{
				// Ollama does not assign identifiers, so one is synthesised so
				// that the tool result can be matched to this call.
				ID:        fmt.Sprintf("ollama_call_%d", index+1),
				Name:      call.Function.Name,
				Arguments: string(arguments),
			}))
		}
	}

	if line.Done {
		if usage := usageFromCounts(line); !usage.IsZero() {
			events = append(events, llm.NewUsageEvent(usage))
		}
		s.finishReason = mapDoneReason(line.DoneReason)
	}

	return events, nil
}

// finalEvent builds the terminal event of the stream.
//
// A response that carried tool calls always finishes with the tool_calls
// reason, even when the server reported "stop": a model that requested tools
// is waiting for their results, and the agent loop must keep going. This is
// what the OpenAI family reports natively and Ollama does not.
func (s *ndjsonStream) finalEvent() llm.Event {
	reason := s.finishReason
	if s.sawToolCall {
		reason = llm.FinishReasonToolCalls
	} else if reason == "" || reason == llm.FinishReasonUnspecified {
		reason = llm.FinishReasonStop
	}
	return llm.NewFinishEvent(reason, s.model)
}

// usageFromCounts builds the token accounting of a finished response.
func usageFromCounts(line *chatResponse) llm.Usage {
	usage := llm.Usage{
		InputTokens:  line.PromptEvalCount,
		OutputTokens: line.EvalCount,
	}
	usage.Normalize()
	return usage
}

// mapDoneReason translates an Ollama stop reason.
//
// Ollama reports "stop" for a normal end, "length" when the prediction budget
// ran out and "load"/"unload" when the model was swapped rather than the
// answer finishing. An unknown reason maps to "stop" because treating it as a
// failure would discard a completed answer.
func mapDoneReason(reason string) llm.FinishReason {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "":
		return llm.FinishReasonUnspecified
	case "stop", "load", "unload":
		return llm.FinishReasonStop
	case "length":
		return llm.FinishReasonLength
	case "tool_calls":
		return llm.FinishReasonToolCalls
	default:
		return llm.FinishReasonStop
	}
}
