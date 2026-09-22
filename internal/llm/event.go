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

package llm

import (
	"context"
	"errors"
	"io"
	"sort"
	"strings"

	"github.com/pawagents/pawagents/internal/apperrors"
)

// EventType identifies what a streamed event carries.
type EventType string

// Stream event types.
const (
	// EventStart opens a stream and reports the concrete model in use.
	EventStart EventType = "start"
	// EventTextDelta appends to the visible answer.
	EventTextDelta EventType = "text_delta"
	// EventThinkingDelta appends to a reasoning trace.
	EventThinkingDelta EventType = "thinking_delta"
	// EventToolCallDelta appends a fragment of a tool call. Providers that
	// stream JSON arguments emit one event per fragment.
	EventToolCallDelta EventType = "tool_call_delta"
	// EventToolCall delivers a complete tool call, replacing any fragment
	// collected for the same index.
	EventToolCall EventType = "tool_call"
	// EventUsage reports token accounting. It may arrive more than once; the
	// last value wins.
	EventUsage EventType = "usage"
	// EventFinish closes a stream and reports why generation stopped.
	EventFinish EventType = "finish"
	// EventError reports a mid-stream failure.
	EventError EventType = "error"
)

// EventTypes returns every known event type.
func EventTypes() []EventType {
	return []EventType{
		EventStart, EventTextDelta, EventThinkingDelta, EventToolCallDelta,
		EventToolCall, EventUsage, EventFinish, EventError,
	}
}

// Valid reports whether the event type is known.
func (t EventType) Valid() bool {
	for _, known := range EventTypes() {
		if known == t {
			return true
		}
	}
	return false
}

// FinishReason explains why a generation stopped.
type FinishReason string

// Finish reasons.
const (
	// FinishReasonStop means the model finished its answer.
	FinishReasonStop FinishReason = "stop"
	// FinishReasonLength means the output token budget was exhausted.
	FinishReasonLength FinishReason = "length"
	// FinishReasonToolCalls means the model is waiting for tool results.
	FinishReasonToolCalls FinishReason = "tool_calls"
	// FinishReasonContentFilter means the provider blocked the output.
	FinishReasonContentFilter FinishReason = "content_filter"
	// FinishReasonError means generation failed after it had started.
	FinishReasonError FinishReason = "error"
	// FinishReasonUnspecified means the provider did not say.
	FinishReasonUnspecified FinishReason = ""
)

// Valid reports whether the reason is known.
func (r FinishReason) Valid() bool {
	switch r {
	case FinishReasonStop, FinishReasonLength, FinishReasonToolCalls,
		FinishReasonContentFilter, FinishReasonError, FinishReasonUnspecified:
		return true
	default:
		return false
	}
}

// Event is one item of a streamed generation.
//
// A single struct is used instead of an interface so that consumers can switch
// on Type without type assertions, and so that a custom provider can emit
// events without importing an extra package per event kind.
type Event struct {
	// Type selects which of the fields below is meaningful.
	Type EventType
	// Index identifies the tool call a delta belongs to.
	Index int
	// TextDelta is the appended text for EventTextDelta.
	TextDelta string
	// ThinkingDelta is the appended reasoning text for EventThinkingDelta.
	ThinkingDelta string
	// ToolCallID and ToolName are set on the first fragment of a tool call.
	ToolCallID string
	ToolName   string
	// ArgumentsDelta is the appended JSON fragment for EventToolCallDelta.
	ArgumentsDelta string
	// ToolCall carries a complete call for EventToolCall.
	ToolCall *ToolCall
	// Usage carries token accounting for EventUsage.
	Usage *Usage
	// FinishReason carries the stop reason for EventFinish.
	FinishReason FinishReason
	// Model names the concrete model in use, when the provider reports it.
	Model string
	// Err carries the failure for EventError.
	Err error
}

// NewStartEvent builds the first event of a stream.
func NewStartEvent(model string) Event {
	return Event{Type: EventStart, Model: model}
}

// NewTextDeltaEvent builds a text delta event.
func NewTextDeltaEvent(text string) Event {
	return Event{Type: EventTextDelta, TextDelta: text}
}

// NewThinkingDeltaEvent builds a reasoning delta event.
func NewThinkingDeltaEvent(text string) Event {
	return Event{Type: EventThinkingDelta, ThinkingDelta: text}
}

// NewToolCallDeltaEvent builds a tool call fragment event. The identifier and
// name are only set on the first fragment of a call.
func NewToolCallDeltaEvent(index int, id, name, argumentsDelta string) Event {
	return Event{
		Type:           EventToolCallDelta,
		Index:          index,
		ToolCallID:     id,
		ToolName:       name,
		ArgumentsDelta: argumentsDelta,
	}
}

// NewToolCallEvent builds a complete tool call event.
func NewToolCallEvent(index int, call ToolCall) Event {
	clone := call
	return Event{Type: EventToolCall, Index: index, ToolCall: &clone}
}

// NewUsageEvent builds a usage event. The usage is copied, so the caller keeps
// ownership of its value.
func NewUsageEvent(usage Usage) Event {
	clone := usage
	return Event{Type: EventUsage, Usage: &clone}
}

// NewFinishEvent builds the terminal event of a stream.
func NewFinishEvent(reason FinishReason, model string) Event {
	return Event{Type: EventFinish, FinishReason: reason, Model: model}
}

// NewErrorEvent builds a failure event.
func NewErrorEvent(err error) Event {
	return Event{Type: EventError, Err: err}
}

// IsTerminal reports whether the event ends the stream.
func (e Event) IsTerminal() bool { return e.Type == EventFinish || e.Type == EventError }

// Validate checks the event carries the field its type requires. Providers are
// expected to call it before emitting so that a translation bug is caught at
// the boundary instead of inside the agent loop.
func (e Event) Validate() error {
	if !e.Type.Valid() {
		return apperrors.New(apperrors.KindProvider, "llm.event",
			"unsupported event type %q", e.Type)
	}
	switch e.Type {
	case EventError:
		if e.Err == nil {
			return apperrors.New(apperrors.KindProvider, "llm.event",
				"an error event must carry an error")
		}
	case EventToolCall:
		if e.ToolCall == nil {
			return apperrors.New(apperrors.KindProvider, "llm.event",
				"a tool call event must carry the call")
		}
		return e.ToolCall.Validate()
	case EventToolCallDelta:
		if e.ArgumentsDelta == "" && e.ToolCallID == "" && e.ToolName == "" {
			return apperrors.New(apperrors.KindProvider, "llm.event",
				"a tool call delta must carry at least an identifier, a name or arguments")
		}
	case EventFinish:
		if !e.FinishReason.Valid() {
			return apperrors.New(apperrors.KindProvider, "llm.event",
				"unsupported finish reason %q", e.FinishReason)
		}
	}
	return nil
}

// Response is the aggregated result of one Generate call.
type Response struct {
	// Message is the assistant turn, including any tool calls.
	Message Message
	// Usage is the token accounting of the request.
	Usage Usage
	// FinishReason explains why generation stopped.
	FinishReason FinishReason
	// Model is the concrete model the provider used.
	Model string
	// Provider is the provider name that served the request.
	Provider string
	// Raw holds the provider's own final payload for session persistence and
	// debugging. It is never interpreted by the runtime.
	Raw []byte
}

// HasToolCalls reports whether the model asked for tools.
func (r *Response) HasToolCalls() bool {
	return r != nil && r.Message.HasToolCalls()
}

// Text returns the assistant text.
func (r *Response) Text() string {
	if r == nil {
		return ""
	}
	return r.Message.Text()
}

// IsTruncated reports whether the completion hit the output token budget.
func (r *Response) IsTruncated() bool {
	return r != nil && r.FinishReason == FinishReasonLength
}

// Stream is a provider generation in progress.
//
// Recv returns io.EOF once the stream is finished. Implementations must not
// return io.EOF together with a usable event: the sentinel is reserved for the
// end of the stream. Close must be safe to call more than once.
type Stream interface {
	Recv() (Event, error)
	Close() error
}

// StreamFunc adapts a pair of functions to the Stream interface. It exists so
// that tests and simple providers can implement a stream in three lines.
type StreamFunc struct {
	// RecvFunc returns the next event.
	RecvFunc func() (Event, error)
	// CloseFunc releases resources. It may be nil.
	CloseFunc func() error
}

// Recv implements Stream.
func (s StreamFunc) Recv() (Event, error) {
	if s.RecvFunc == nil {
		return Event{}, io.EOF
	}
	return s.RecvFunc()
}

// Close implements Stream.
func (s StreamFunc) Close() error {
	if s.CloseFunc == nil {
		return nil
	}
	return s.CloseFunc()
}

// SliceStream builds a stream from a fixed event list followed by io.EOF. It
// is primarily a test helper, but it also lets a non-streaming provider return
// a complete answer through the streaming interface.
func SliceStream(events ...Event) Stream {
	index := 0
	return StreamFunc{RecvFunc: func() (Event, error) {
		if index >= len(events) {
			return Event{}, io.EOF
		}
		event := events[index]
		index++
		return event, nil
	}}
}

// Collect drains a stream into a finished Response.
//
// Collect owns the stream: it always closes it before returning. Tool call
// fragments are merged per index, text and reasoning deltas are concatenated,
// and the last usage event wins.
//
// Some providers end a stream with io.EOF instead of a finish event. When
// output was produced Collect accepts it and infers the finish reason; when
// nothing was produced the stream is treated as failed, because silently
// returning an empty answer would hide a broken provider.
func Collect(ctx context.Context, stream Stream) (*Response, error) {
	if stream == nil {
		return nil, apperrors.New(apperrors.KindInternal, "llm.collect",
			"the stream is nil")
	}
	defer func() { _ = stream.Close() }()

	response := &Response{Message: Message{Role: RoleAssistant}}
	var text, thinking strings.Builder
	fragments := make(map[int]*toolCallBuilder)
	finished := false

	for {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, contextError("llm.collect", err)
			}
		}

		event, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, streamError(err)
		}

		switch event.Type {
		case EventStart:
			if event.Model != "" {
				response.Model = event.Model
			}
		case EventTextDelta:
			text.WriteString(event.TextDelta)
		case EventThinkingDelta:
			thinking.WriteString(event.ThinkingDelta)
		case EventToolCallDelta:
			builder := fragments[event.Index]
			if builder == nil {
				builder = &toolCallBuilder{}
				fragments[event.Index] = builder
			}
			if event.ToolCallID != "" {
				builder.id = event.ToolCallID
			}
			if event.ToolName != "" {
				builder.name = event.ToolName
			}
			builder.arguments.WriteString(event.ArgumentsDelta)
		case EventToolCall:
			if event.ToolCall == nil {
				return nil, apperrors.New(apperrors.KindProvider, "llm.collect",
					"the provider emitted a tool call event without a call")
			}
			builder := &toolCallBuilder{
				id:   event.ToolCall.ID,
				name: event.ToolCall.Name,
			}
			builder.arguments.WriteString(event.ToolCall.Arguments)
			fragments[event.Index] = builder
			if event.Model != "" {
				response.Model = event.Model
			}
		case EventUsage:
			if event.Usage != nil {
				usage := *event.Usage
				usage.Normalize()
				response.Usage = usage
			}
		case EventFinish:
			response.FinishReason = event.FinishReason
			if event.Model != "" {
				response.Model = event.Model
			}
			if !event.FinishReason.Valid() {
				return nil, apperrors.New(apperrors.KindProvider, "llm.collect",
					"the provider reported the unsupported finish reason %q", event.FinishReason)
			}
			finished = true
		case EventError:
			if event.Err == nil {
				return nil, apperrors.New(apperrors.KindProvider, "llm.collect",
					"the provider emitted an error event without an error")
			}
			return nil, streamError(event.Err)
		default:
			return nil, apperrors.New(apperrors.KindProvider, "llm.collect",
				"the provider emitted the unsupported event type %q", event.Type)
		}
	}

	response.Usage.Normalize()

	if thinking.Len() > 0 {
		response.Message.Content = append(response.Message.Content, ThinkingPart(thinking.String()))
	}
	if text.Len() > 0 {
		response.Message.Content = append(response.Message.Content, TextPart(text.String()))
	}
	response.Message.ToolCalls = mergeToolCalls(fragments)

	if !finished {
		if response.Message.IsEmpty() {
			return nil, apperrors.New(apperrors.KindProvider, "llm.collect",
				"the stream ended without producing any output or a finish event")
		}
		response.FinishReason = FinishReasonStop
		if response.Message.HasToolCalls() {
			response.FinishReason = FinishReasonToolCalls
		}
	}

	return response, nil
}

type toolCallBuilder struct {
	id        string
	name      string
	arguments strings.Builder
}

func mergeToolCalls(fragments map[int]*toolCallBuilder) []ToolCall {
	if len(fragments) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(fragments))
	for index := range fragments {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)

	calls := make([]ToolCall, 0, len(indexes))
	for _, index := range indexes {
		builder := fragments[index]
		if builder.name == "" {
			// A fragment without a name cannot be dispatched, so it is
			// dropped rather than turned into a call that fails later.
			continue
		}
		calls = append(calls, ToolCall{
			ID:        builder.id,
			Name:      builder.name,
			Arguments: builder.arguments.String(),
		})
	}
	if len(calls) == 0 {
		return nil
	}
	return calls
}

// streamError classifies a provider failure, preserving errors that are
// already classified.
func streamError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return contextError("llm.collect", err)
	}
	var classified *apperrors.Error
	if errors.As(err, &classified) {
		return classified
	}
	return apperrors.Wrap(apperrors.KindProvider, "llm.collect",
		"the provider failed while streaming", err)
}

// contextError converts a context failure into the matching classified error.
func contextError(op string, err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return apperrors.Wrap(apperrors.KindTimeout, op, "the request deadline was exceeded", err)
	case errors.Is(err, context.Canceled):
		return apperrors.Wrap(apperrors.KindCancelled, op, "the request was cancelled", err)
	default:
		return apperrors.Wrap(apperrors.KindInternal, op, "unexpected context error", err)
	}
}
