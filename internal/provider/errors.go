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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/pawagents/pawagents/internal/apperrors"
)

// MaxErrorBodyBytes bounds how much of an error body is read, so a misbehaving
// gateway cannot exhaust memory with a huge HTML error page.
const MaxErrorBodyBytes = 64 * 1024

// ReadErrorBody reads a bounded error body. The body is closed by the caller.
func ReadErrorBody(response *http.Response) []byte {
	if response == nil || response.Body == nil {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxErrorBodyBytes))
	if err != nil {
		return nil
	}
	return body
}

// ErrorMessage extracts the human readable part of a provider error body.
//
// Four shapes are seen in practice: the OpenAI envelope
// {"error":{"message":...,"type":...}}, the flat Ollama envelope
// {"error":"..."}, a bare {"message":"..."} and, when a proxy answered instead
// of the provider, an HTML page. The result is always a single sanitized line,
// because it ends up in a log line and in an error message.
func ErrorMessage(body []byte) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return ""
	}

	var envelope struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if err := json.Unmarshal(trimmed, &envelope); err == nil {
		if message := decodeErrorMessage(envelope.Error); message != "" {
			return SanitizeMessage(message)
		}
		if strings.TrimSpace(envelope.Message) != "" {
			return SanitizeMessage(envelope.Message)
		}
	}

	text := string(trimmed)
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		text = text[:index]
	}
	if len(text) > 400 {
		text = text[:400] + "..."
	}
	return SanitizeMessage(text)
}

// decodeErrorMessage reads the "error" field, which is either an object with a
// message or a plain string.
func decodeErrorMessage(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return strings.TrimSpace(asString)
	}

	var asObject struct {
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	if err := json.Unmarshal(raw, &asObject); err == nil {
		if strings.TrimSpace(asObject.Message) != "" {
			return strings.TrimSpace(asObject.Message)
		}
		return strings.TrimSpace(asObject.Detail)
	}

	return ""
}

// SanitizeMessage collapses whitespace so that a multi-line provider message
// stays on one line.
func SanitizeMessage(message string) string {
	return strings.Join(strings.Fields(message), " ")
}

// HTTPStatusError maps an HTTP failure onto the error model.
//
// Providers share this mapping so that a host sees the same classification
// whatever endpoint it talks to, and so that the CLI exit status is meaningful:
// a rejected credential is an authentication error, a missing model is a
// not-found error, and a slow endpoint is a timeout.
func HTTPStatusError(op string, response *http.Response, body []byte) error {
	status := response.StatusCode
	message := ErrorMessage(body)
	if message == "" {
		message = http.StatusText(status)
	}

	details := map[string]any{"status": status}
	if requestID := requestID(response); requestID != "" {
		details["request_id"] = requestID
	}

	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return apperrors.New(apperrors.KindAuthentication, op,
			"the provider rejected the credential (HTTP %d): %s", status, message).
			WithDetails(details)
	case status == http.StatusNotFound:
		return apperrors.New(apperrors.KindNotFound, op,
			"the endpoint or model was not found (HTTP %d): %s", status, message).
			WithDetails(details)
	case status == http.StatusTooManyRequests:
		if retryAfter := response.Header.Get("Retry-After"); retryAfter != "" {
			details["retry_after"] = retryAfter
		}
		return apperrors.New(apperrors.KindProvider, op,
			"the provider rate limited the request (HTTP %d): %s", status, message).
			WithDetails(details)
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return apperrors.New(apperrors.KindTimeout, op,
			"the provider timed out (HTTP %d): %s", status, message).WithDetails(details)
	default:
		return apperrors.New(apperrors.KindProvider, op,
			"the provider returned HTTP %d: %s", status, message).WithDetails(details)
	}
}

// requestID returns the request identifier a provider echoed back, whichever
// header it used. It is recorded in the error details so that a user can quote
// it to the vendor.
func requestID(response *http.Response) string {
	if response == nil {
		return ""
	}
	for _, header := range []string{"X-Request-Id", "Request-Id", "X-Amzn-Requestid"} {
		if value := strings.TrimSpace(response.Header.Get(header)); value != "" {
			return value
		}
	}
	return ""
}

// NetworkError maps a transport failure onto the error model.
//
// hint is appended to the connection failure message so that a provider can say
// what the user should check; it is ignored for timeouts and cancellations,
// where the cause is already precise.
func NetworkError(op string, err error, hint string) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return apperrors.Wrap(apperrors.KindTimeout, op, "the provider request timed out", err)
	case errors.Is(err, context.Canceled):
		return apperrors.Wrap(apperrors.KindCancelled, op, "the provider request was cancelled", err)
	}

	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) && netErr.Timeout() {
		return apperrors.Wrap(apperrors.KindTimeout, op, "the provider request timed out", err)
	}

	message := "cannot reach the provider endpoint"
	if trimmed := strings.TrimSpace(hint); trimmed != "" {
		message += ": " + trimmed
	}
	return apperrors.Wrap(apperrors.KindProvider, op, "%s", err, message)
}
