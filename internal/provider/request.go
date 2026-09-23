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

package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/llm"
)

// MarshalRequest encodes a provider request payload.
//
// extra holds the provider extra_body from the configuration merged with the
// per-request extras, and is merged into the encoded object rather than being
// declared as a field, so that a user can reach any vendor specific knob
// without waiting for a code change. The merge is deliberately last-wins: an
// explicit extra field overrides the value the adapter would have sent, which
// is what a user writing one expects.
func MarshalRequest(payload any, extra map[string]any) ([]byte, error) {
	if payload == nil {
		return nil, apperrors.New(apperrors.KindInternal, "provider.request",
			"the request payload is nil")
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, apperrors.Wrap(apperrors.KindInternal, "provider.request",
			"cannot encode the request body", err)
	}
	if len(extra) == 0 {
		return encoded, nil
	}

	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		return nil, apperrors.Wrap(apperrors.KindInternal, "provider.request",
			"cannot merge the extra request fields", err)
	}
	for key, value := range extra {
		object[key] = value
	}

	merged, err := json.Marshal(object)
	if err != nil {
		return nil, apperrors.Wrap(apperrors.KindInternal, "provider.request",
			"cannot encode the merged request body", err)
	}
	return merged, nil
}

// MergeExtras combines the provider level extras with the per-request ones,
// letting the request win. A nil result means nothing to merge.
func MergeExtras(providerExtras, requestExtras map[string]any) map[string]any {
	if len(providerExtras) == 0 && len(requestExtras) == 0 {
		return nil
	}

	merged := make(map[string]any, len(providerExtras)+len(requestExtras))
	for key, value := range providerExtras {
		merged[key] = value
	}
	for key, value := range requestExtras {
		merged[key] = value
	}
	return merged
}

// BufferedStream replays a fixed event sequence.
//
// Providers that cannot stream use it to answer with the equivalent event
// sequence, so the agent loop never has to care whether a server honoured
// `stream: true`.
type BufferedStream struct {
	events []llm.Event
	cancel context.CancelFunc
	body   io.Closer
	index  int
	closed bool
}

// NewBufferedStream builds a stream over a fixed event list. cancel and body
// may both be nil.
func NewBufferedStream(events []llm.Event, cancel context.CancelFunc, body io.Closer) llm.Stream {
	if cancel == nil {
		cancel = func() {}
	}
	return &BufferedStream{events: events, cancel: cancel, body: body}
}

// Recv implements llm.Stream.
func (s *BufferedStream) Recv() (llm.Event, error) {
	if s.index >= len(s.events) {
		return llm.Event{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

// Close implements llm.Stream. It is safe to call more than once.
func (s *BufferedStream) Close() error {
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

// DataURL encodes inline bytes as a data URL, which is how the OpenAI family of
// protocols carries an image that is part of the conversation.
func DataURL(mimeType string, data []byte) string {
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
}
