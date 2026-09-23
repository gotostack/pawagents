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
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/pawagents/pawagents/internal/apperrors"
)

func TestErrorMessageReadsEveryEnvelope(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "openai envelope",
			body: `{"error":{"message":"model does not exist","type":"invalid_request_error"}}`,
			want: "model does not exist",
		},
		{
			name: "ollama envelope",
			body: `{"error":"model not found"}`,
			want: "model not found",
		},
		{
			name: "bare message",
			body: `{"message":"quota exceeded"}`,
			want: "quota exceeded",
		},
		{
			name: "detail field",
			body: `{"error":{"detail":"invalid api key"}}`,
			want: "invalid api key",
		},
		{
			name: "html from a proxy keeps the first line",
			body: "<html>\n<body>bad gateway</body>\n</html>",
			want: "<html>",
		},
		{
			name: "plain text",
			body: "boom\nsecond line",
			want: "boom",
		},
		{
			name: "empty",
			body: "",
			want: "",
		},
		{
			name: "whitespace",
			body: "   \n\t ",
			want: "",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ErrorMessage([]byte(testCase.body)); got != testCase.want {
				t.Fatalf("ErrorMessage(%q) = %q, want %q", testCase.body, got, testCase.want)
			}
		})
	}

	if got := ErrorMessage(nil); got != "" {
		t.Fatalf("ErrorMessage(nil) = %q", got)
	}
}

func TestErrorMessageCollapsesAndTruncates(t *testing.T) {
	collapsed := ErrorMessage([]byte(`{"error":{"message":"line one\nline two"}}`))
	if collapsed != "line one line two" {
		t.Fatalf("collapsed = %q", collapsed)
	}

	long := ErrorMessage([]byte(strings.Repeat("x", 1000)))
	if len(long) > 410 {
		t.Fatalf("len = %d, want the message to be truncated", len(long))
	}
	if !strings.HasSuffix(long, "...") {
		t.Fatalf("message = %q, want a truncation marker", long)
	}
}

func TestHTTPStatusErrorClassifiesEveryStatus(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   apperrors.Kind
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"error":{"message":"bad key"}}`, want: apperrors.KindAuthentication},
		{name: "forbidden", status: http.StatusForbidden, body: `{"error":{"message":"no access"}}`, want: apperrors.KindAuthentication},
		{name: "missing model", status: http.StatusNotFound, body: `{"error":{"message":"unknown model"}}`, want: apperrors.KindNotFound},
		{name: "rate limited", status: http.StatusTooManyRequests, body: `{"error":{"message":"slow down"}}`, want: apperrors.KindProvider},
		{name: "request timeout", status: http.StatusRequestTimeout, body: "", want: apperrors.KindTimeout},
		{name: "gateway timeout", status: http.StatusGatewayTimeout, body: "", want: apperrors.KindTimeout},
		{name: "bad request", status: http.StatusBadRequest, body: `{"error":{"message":"bad field"}}`, want: apperrors.KindProvider},
		{name: "server error", status: http.StatusInternalServerError, body: "", want: apperrors.KindProvider},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := &http.Response{
				StatusCode: testCase.status,
				Header:     http.Header{},
			}
			err := HTTPStatusError("provider.generate", response, []byte(testCase.body))
			if kind := apperrors.KindOf(err); kind != testCase.want {
				t.Fatalf("kind = %s, want %s (%v)", kind, testCase.want, err)
			}

			var classified *apperrors.Error
			if !errors.As(err, &classified) {
				t.Fatalf("the error is not classified: %v", err)
			}
			if status, ok := classified.Details["status"].(int); !ok || status != testCase.status {
				t.Fatalf("details = %+v", classified.Details)
			}
		})
	}
}

func TestHTTPStatusErrorRecordsRequestAndRetryHeaders(t *testing.T) {
	response := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{},
	}
	response.Header.Set("X-Request-Id", "req_123")
	response.Header.Set("Retry-After", "30")

	err := HTTPStatusError("provider.generate", response, []byte(`{"error":{"message":"slow"}}`))

	var classified *apperrors.Error
	if !errors.As(err, &classified) {
		t.Fatalf("the error is not classified: %v", err)
	}
	if classified.Details["request_id"] != "req_123" {
		t.Fatalf("details = %+v", classified.Details)
	}
	if classified.Details["retry_after"] != "30" {
		t.Fatalf("details = %+v", classified.Details)
	}
	if !strings.Contains(classified.Message, "rate limited") {
		t.Fatalf("message = %q", classified.Message)
	}
}

// timeoutError is a network error that reports itself as a timeout.
type timeoutError struct{}

func (timeoutError) Error() string { return "i/o timeout" }
func (timeoutError) Timeout() bool { return true }

func TestNetworkErrorClassifiesTransportFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want apperrors.Kind
	}{
		{name: "deadline", err: context.DeadlineExceeded, want: apperrors.KindTimeout},
		{name: "cancelled", err: context.Canceled, want: apperrors.KindCancelled},
		{name: "network timeout", err: timeoutError{}, want: apperrors.KindTimeout},
		{name: "refused", err: errors.New("connection refused"), want: apperrors.KindProvider},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := NetworkError("provider.generate", testCase.err, "is the server running?")
			if kind := apperrors.KindOf(err); kind != testCase.want {
				t.Fatalf("kind = %s, want %s", kind, testCase.want)
			}
			if !errors.Is(err, testCase.err) {
				t.Fatalf("the cause must be wrapped: %v", err)
			}
		})
	}

	withHint := NetworkError("provider.generate", errors.New("connection refused"), "start the server")
	if !strings.Contains(withHint.Error(), "start the server") {
		t.Fatalf("the hint is missing: %v", withHint)
	}
}
