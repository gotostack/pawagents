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
	"strings"
	"testing"
	"time"

	"github.com/pawagents/pawagents/internal/apperrors"
)

func TestEventValidate(t *testing.T) {
	call := ToolCall{ID: "c1", Name: "repo.read", Arguments: "{}"}

	valid := []Event{
		NewStartEvent("qwen3-coder"),
		NewTextDeltaEvent("hi"),
		NewThinkingDeltaEvent("hmm"),
		NewToolCallDeltaEvent(0, "c1", "repo.read", `{"path"`),
		NewToolCallEvent(0, call),
		NewUsageEvent(Usage{InputTokens: 1}),
		NewFinishEvent(FinishReasonStop, "qwen3-coder"),
		NewErrorEvent(errors.New("boom")),
	}
	for _, event := range valid {
		if err := event.Validate(); err != nil {
			t.Fatalf("event %q should be valid: %v", event.Type, err)
		}
	}

	invalid := []struct {
		name    string
		event   Event
		wantErr string
	}{
		{name: "unknown type", event: Event{Type: "delta"}, wantErr: "unsupported event type"},
		{name: "error without error", event: Event{Type: EventError}, wantErr: "must carry an error"},
		{name: "tool call without call", event: Event{Type: EventToolCall}, wantErr: "must carry the call"},
		{
			name:    "tool call without a name",
			event:   Event{Type: EventToolCall, ToolCall: &ToolCall{ID: "c1"}},
			wantErr: "missing the tool name",
		},
		{name: "empty tool call delta", event: Event{Type: EventToolCallDelta}, wantErr: "at least an identifier"},
		{
			name:    "invalid finish reason",
			event:   Event{Type: EventFinish, FinishReason: "exploded"},
			wantErr: "unsupported finish reason",
		},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			err := test.event.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want %q", test.wantErr)
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Validate() = %q, want it to contain %q", err.Error(), test.wantErr)
			}
		})
	}

	if !NewFinishEvent(FinishReasonStop, "m").IsTerminal() {
		t.Fatal("a finish event is terminal")
	}
	if NewTextDeltaEvent("x").IsTerminal() {
		t.Fatal("a text delta is not terminal")
	}
}

func TestSliceStreamAndStreamFunc(t *testing.T) {
	stream := SliceStream(NewTextDeltaEvent("a"), NewTextDeltaEvent("b"))

	first, err := stream.Recv()
	if err != nil || first.TextDelta != "a" {
		t.Fatalf("first event = %+v, %v", first, err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("second Recv() returned %v", err)
	}
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("third Recv() = %v, want io.EOF", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	empty := StreamFunc{}
	if _, err := empty.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("a stream without RecvFunc must end immediately, got %v", err)
	}
	if err := empty.Close(); err != nil {
		t.Fatalf("a stream without CloseFunc must close cleanly, got %v", err)
	}

	closed := false
	custom := StreamFunc{
		RecvFunc:  func() (Event, error) { return NewFinishEvent(FinishReasonStop, "m"), nil },
		CloseFunc: func() error { closed = true; return nil },
	}
	if _, err := custom.Recv(); err != nil {
		t.Fatalf("Recv() = %v", err)
	}
	if err := custom.Close(); err != nil || !closed {
		t.Fatalf("Close() = %v, closed = %v", err, closed)
	}
}

func TestCollectAccumulatesTextAndUsage(t *testing.T) {
	closed := false
	stream := StreamFunc{
		RecvFunc: func() func() (Event, error) {
			events := []Event{
				NewStartEvent("qwen3-coder"),
				NewThinkingDeltaEvent("let me look"),
				NewTextDeltaEvent("Found "),
				NewTextDeltaEvent("two issues."),
				NewUsageEvent(Usage{InputTokens: 100, OutputTokens: 20}),
				NewUsageEvent(Usage{InputTokens: 120, OutputTokens: 25, CacheReadTokens: 100}),
				NewFinishEvent(FinishReasonStop, "qwen3-coder"),
			}
			index := 0
			return func() (Event, error) {
				if index >= len(events) {
					return Event{}, io.EOF
				}
				event := events[index]
				index++
				return event, nil
			}
		}(),
		CloseFunc: func() error { closed = true; return nil },
	}

	response, err := Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect() returned %v", err)
	}
	if response.Text() != "Found two issues." {
		t.Fatalf("text = %q", response.Text())
	}
	if response.Message.ReasoningText() != "let me look" {
		t.Fatalf("reasoning = %q", response.Message.ReasoningText())
	}
	if len(response.Message.Content) != 2 {
		t.Fatalf("content = %+v, want the reasoning and the answer as ordered parts",
			response.Message.Content)
	}
	if response.Message.Content[0].Kind != ContentThinking || response.Message.Content[1].Kind != ContentText {
		t.Fatalf("content order = %+v, want reasoning first", response.Message.Content)
	}
	if response.FinishReason != FinishReasonStop {
		t.Fatalf("finish reason = %q", response.FinishReason)
	}
	if response.Model != "qwen3-coder" {
		t.Fatalf("model = %q", response.Model)
	}
	if response.Usage.InputTokens != 120 || response.Usage.OutputTokens != 25 {
		t.Fatalf("usage = %+v, want the last usage event to win", response.Usage)
	}
	if response.Usage.TotalTokens != 145 {
		t.Fatalf("total = %d, want 145", response.Usage.TotalTokens)
	}
	if !closed {
		t.Fatal("Collect must close the stream")
	}
	if response.Message.Role != RoleAssistant {
		t.Fatalf("role = %q", response.Message.Role)
	}
}

func TestCollectMergesToolCallFragments(t *testing.T) {
	stream := SliceStream(
		NewToolCallDeltaEvent(1, "c2", "git.diff", `{"paths`),
		NewToolCallDeltaEvent(0, "c1", "repo.read", `{"path":`),
		NewToolCallDeltaEvent(0, "", "", `"main.go"}`),
		NewToolCallDeltaEvent(1, "", "", `":["a.go"]}`),
		NewFinishEvent(FinishReasonToolCalls, "qwen3-coder"),
	)

	response, err := Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect() returned %v", err)
	}
	calls := response.Message.ToolCalls
	if len(calls) != 2 {
		t.Fatalf("calls = %+v", calls)
	}
	if calls[0].ID != "c1" || calls[0].Name != "repo.read" {
		t.Fatalf("calls[0] = %+v", calls[0])
	}
	if calls[0].Arguments != `{"path":"main.go"}` {
		t.Fatalf("calls[0] arguments = %q", calls[0].Arguments)
	}
	if calls[1].ID != "c2" || calls[1].Name != "git.diff" {
		t.Fatalf("calls[1] = %+v, want fragments sorted by index", calls[1])
	}
	if !response.HasToolCalls() {
		t.Fatal("HasToolCalls() must be true")
	}
	if response.FinishReason != FinishReasonToolCalls {
		t.Fatalf("finish reason = %q", response.FinishReason)
	}
}

func TestCollectCompleteToolCallReplacesFragments(t *testing.T) {
	stream := SliceStream(
		NewToolCallDeltaEvent(0, "c1", "repo.read", `{"path":"half`),
		NewToolCallEvent(0, ToolCall{ID: "c1", Name: "repo.read", Arguments: `{"path":"main.go"}`}),
		NewFinishEvent(FinishReasonToolCalls, "m"),
	)

	response, err := Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect() returned %v", err)
	}
	if len(response.Message.ToolCalls) != 1 {
		t.Fatalf("calls = %+v", response.Message.ToolCalls)
	}
	if got := response.Message.ToolCalls[0].Arguments; got != `{"path":"main.go"}` {
		t.Fatalf("arguments = %q, want the complete call to win", got)
	}
}

func TestCollectDropsUnnamedFragments(t *testing.T) {
	stream := SliceStream(
		NewToolCallDeltaEvent(0, "c1", "", `{"path":"main.go"}`),
		NewFinishEvent(FinishReasonToolCalls, "m"),
	)

	response, err := Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect() returned %v", err)
	}
	if response.Message.HasToolCalls() {
		t.Fatalf("an unnamed fragment must be dropped, got %+v", response.Message.ToolCalls)
	}
}

func TestCollectInfersFinishReasonWhenTheStreamJustEnds(t *testing.T) {
	response, err := Collect(context.Background(), SliceStream(NewTextDeltaEvent("partial answer")))
	if err != nil {
		t.Fatalf("Collect() returned %v", err)
	}
	if response.FinishReason != FinishReasonStop {
		t.Fatalf("finish reason = %q, want stop", response.FinishReason)
	}
	if response.Text() != "partial answer" {
		t.Fatalf("text = %q", response.Text())
	}

	response, err = Collect(context.Background(), SliceStream(
		NewToolCallEvent(0, ToolCall{ID: "c1", Name: "repo.read", Arguments: "{}"}),
	))
	if err != nil {
		t.Fatalf("Collect() returned %v", err)
	}
	if response.FinishReason != FinishReasonToolCalls {
		t.Fatalf("finish reason = %q, want tool_calls", response.FinishReason)
	}
}

func TestCollectRejectsAnEmptyStream(t *testing.T) {
	_, err := Collect(context.Background(), SliceStream())
	if err == nil {
		t.Fatal("an empty stream must be reported as a provider failure")
	}
	if !apperrors.IsKind(err, apperrors.KindProvider) {
		t.Fatalf("kind = %q, want %q", apperrors.KindOf(err), apperrors.KindProvider)
	}
}

func TestCollectRejectsNilStream(t *testing.T) {
	_, err := Collect(context.Background(), nil)
	if !apperrors.IsKind(err, apperrors.KindInternal) {
		t.Fatalf("kind = %q, want %q", apperrors.KindOf(err), apperrors.KindInternal)
	}
}

func TestCollectPropagatesErrors(t *testing.T) {
	cause := errors.New("connection reset")

	_, err := Collect(context.Background(), StreamFunc{
		RecvFunc: func() (Event, error) { return Event{}, cause },
	})
	if !apperrors.IsKind(err, apperrors.KindProvider) {
		t.Fatalf("kind = %q, want a provider error", apperrors.KindOf(err))
	}
	if !errors.Is(err, cause) {
		t.Fatal("the provider cause must stay reachable")
	}

	streamFailure := errors.New("rate limited")
	_, err = Collect(context.Background(), SliceStream(NewErrorEvent(streamFailure)))
	if !errors.Is(err, streamFailure) {
		t.Fatalf("error = %v, want the stream failure", err)
	}

	_, err = Collect(context.Background(), SliceStream(Event{Type: EventError}))
	if err == nil || !strings.Contains(err.Error(), "without an error") {
		t.Fatalf("error = %v", err)
	}
}

func TestCollectKeepsClassifiedErrors(t *testing.T) {
	classified := apperrors.New(apperrors.KindAuthentication, "provider.auth", "invalid api key")

	_, err := Collect(context.Background(), StreamFunc{
		RecvFunc: func() (Event, error) { return Event{}, classified },
	})
	if err != classified {
		t.Fatalf("error = %v, want the classified error to pass through unchanged", err)
	}
}

func TestCollectHonoursContext(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Collect(cancelled, SliceStream(NewFinishEvent(FinishReasonStop, "m")))
	if !apperrors.IsKind(err, apperrors.KindCancelled) {
		t.Fatalf("kind = %q, want a cancelled error", apperrors.KindOf(err))
	}

	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()

	_, err = Collect(expired, SliceStream(NewFinishEvent(FinishReasonStop, "m")))
	if !apperrors.IsKind(err, apperrors.KindTimeout) {
		t.Fatalf("kind = %q, want a timeout error", apperrors.KindOf(err))
	}
}

func TestCollectRejectsUnsupportedEvents(t *testing.T) {
	_, err := Collect(context.Background(), SliceStream(Event{Type: "mystery"}))
	if !apperrors.IsKind(err, apperrors.KindProvider) {
		t.Fatalf("kind = %q", apperrors.KindOf(err))
	}
}

func TestCollectRejectsInvalidFinishReason(t *testing.T) {
	event := NewFinishEvent(FinishReasonStop, "m")
	event.FinishReason = "exploded"

	_, err := Collect(context.Background(), SliceStream(event))
	if err == nil || !strings.Contains(err.Error(), "unsupported finish reason") {
		t.Fatalf("error = %v", err)
	}
}

func TestResponseHelpers(t *testing.T) {
	var response *Response
	if response.HasToolCalls() || response.Text() != "" || response.IsTruncated() {
		t.Fatal("nil responses must be safe to inspect")
	}

	truncated := &Response{FinishReason: FinishReasonLength}
	if !truncated.IsTruncated() {
		t.Fatal("IsTruncated() must report FinishReasonLength")
	}
}
