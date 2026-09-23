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
	"bufio"
	"io"
	"strings"

	"github.com/pawagents/pawagents/internal/apperrors"
)

// MaxEventBytes bounds a single Server-Sent Events line. A provider that sends
// a larger line than this is either broken or hostile, and either way the
// stream must fail rather than grow without limit.
const MaxEventBytes = 4 * 1024 * 1024

// SSEReader reads a text/event-stream body into whole events.
//
// The reader implements the parts of the event stream specification the
// providers actually rely on: comment lines start with a colon and are ignored,
// "event:" names the event, "data:" carries the payload and several data lines
// are joined with a newline, and a blank line ends the event. Keep-alive
// newlines therefore produce no event at all.
type SSEReader struct {
	body    io.ReadCloser
	scanner *bufio.Scanner
}

// NewSSEReader wraps a streaming response body. The returned reader owns the
// body and closes it.
func NewSSEReader(body io.ReadCloser) *SSEReader {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), MaxEventBytes)
	return &SSEReader{body: body, scanner: scanner}
}

// Next returns the next event. The name is empty when the stream only sends
// data lines, which is how every provider in this project behaves. It returns
// io.EOF once the stream is exhausted, and a ProviderError when a line is too
// long to read.
func (r *SSEReader) Next() (string, string, error) {
	var name string
	var data strings.Builder

	for {
		if !r.scanner.Scan() {
			if err := r.scanner.Err(); err != nil {
				return name, data.String(), apperrors.Wrap(apperrors.KindProvider,
					"provider.stream", "cannot read the event stream", err)
			}
			if data.Len() > 0 {
				return name, data.String(), nil
			}
			return "", "", io.EOF
		}

		line := r.scanner.Text()
		switch {
		case line == "":
			if data.Len() > 0 {
				return name, data.String(), nil
			}
			name = ""
		case strings.HasPrefix(line, ":"):
			// A comment, used as a keep-alive by several gateways.
		case strings.HasPrefix(line, "event:"):
			name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		default:
			// A field the protocol does not use here, such as "id:" or
			// "retry:". Ignoring it keeps a stream readable.
		}
	}
}

// Close releases the body.
func (r *SSEReader) Close() error {
	if r == nil || r.body == nil {
		return nil
	}
	return r.body.Close()
}

// IsDone reports whether a data payload is the terminal sentinel some
// providers send instead of closing the connection.
func IsDone(data string) bool {
	trimmed := strings.TrimSpace(data)
	return trimmed == "[DONE]" || trimmed == "DONE"
}
