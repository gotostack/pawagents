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

// Package apperrors defines the error model shared by every PawAgents
// subsystem.
//
// The runtime never returns bare `errors.New` strings to the CLI or the MCP
// layer. Every failure is classified with a Kind so that callers can map it to
// a stable machine-readable code and a process exit status. MCP responses rely
// on the same classification to return {code, message} pairs.
package apperrors

import (
	"errors"
	"fmt"
	"strings"
)

// Kind classifies an error. The string values are part of the public contract
// exposed through the CLI and the MCP server, so they must stay stable.
type Kind string

// Error kinds.
const (
	KindConfig          Kind = "ConfigError"
	KindProvider        Kind = "ProviderError"
	KindAuthentication  Kind = "AuthenticationError"
	KindCapability      Kind = "CapabilityError"
	KindAgent           Kind = "AgentError"
	KindTool            Kind = "ToolError"
	KindPermission      Kind = "PermissionError"
	KindWorkspace       Kind = "WorkspaceError"
	KindBudgetExceeded  Kind = "BudgetExceededError"
	KindTimeout         Kind = "TimeoutError"
	KindCancelled       Kind = "CancelledError"
	KindInvalidArgument Kind = "InvalidArgumentError"
	KindNotFound        Kind = "NotFoundError"
	KindInternal        Kind = "InternalError"
)

// AllKinds lists every known error kind. It is used by tests and by the
// documentation generator to guarantee the set stays in sync.
func AllKinds() []Kind {
	return []Kind{
		KindConfig,
		KindProvider,
		KindAuthentication,
		KindCapability,
		KindAgent,
		KindTool,
		KindPermission,
		KindWorkspace,
		KindBudgetExceeded,
		KindTimeout,
		KindCancelled,
		KindInvalidArgument,
		KindNotFound,
		KindInternal,
	}
}

// Process exit codes. They are grouped so that scripts can distinguish
// configuration problems from runtime problems without parsing messages.
const (
	ExitOK         = 0
	ExitError      = 1
	ExitUsage      = 2
	ExitConfig     = 3
	ExitAuth       = 4
	ExitCapability = 5
	ExitPermission = 6
	ExitWorkspace  = 7
	ExitProvider   = 8
	ExitAgent      = 9
	ExitTool       = 10
	ExitBudget     = 11
	ExitTimeout    = 12
	ExitCancelled  = 13
	ExitNotFound   = 14
)

// Error is the classified error type used across the project.
type Error struct {
	// Kind is the error category.
	Kind Kind
	// Op describes the failing operation, for example "config.load".
	Op string
	// Message is the human-readable description without the kind prefix.
	Message string
	// Details carries optional machine-readable context. Values must never
	// contain secrets.
	Details map[string]any
	// Err is the wrapped cause, if any.
	Err error
}

// New builds a classified error with a formatted message.
func New(kind Kind, op, format string, args ...any) *Error {
	return &Error{
		Kind:    kind,
		Op:      op,
		Message: fmt.Sprintf(format, args...),
	}
}

// Wrap builds a classified error that wraps an existing cause.
func Wrap(kind Kind, op, format string, err error, args ...any) *Error {
	return &Error{
		Kind:    kind,
		Op:      op,
		Message: fmt.Sprintf(format, args...),
		Err:     err,
	}
}

// WithDetails attaches machine-readable details and returns the receiver so it
// can be used inline.
func (e *Error) WithDetails(details map[string]any) *Error {
	if e == nil {
		return nil
	}
	if e.Details == nil {
		e.Details = make(map[string]any, len(details))
	}
	for k, v := range details {
		e.Details[k] = v
	}
	return e
}

// WithOp overrides the operation label and returns the receiver.
func (e *Error) WithOp(op string) *Error {
	if e == nil {
		return nil
	}
	e.Op = op
	return e
}

// Error implements the error interface. The rendered form is
// "<Kind>: <Op>: <Message>[: <cause>]".
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	parts := make([]string, 0, 3)
	if e.Kind != "" {
		parts = append(parts, string(e.Kind))
	}
	if e.Op != "" {
		parts = append(parts, e.Op)
	}
	message := e.Message
	if message == "" && e.Err != nil {
		message = e.Err.Error()
	}
	if message != "" {
		parts = append(parts, message)
	} else if len(parts) == 0 {
		return "<empty error>"
	}

	out := strings.Join(parts, ": ")
	if e.Err != nil && e.Message != "" {
		out += ": " + e.Err.Error()
	}
	return out
}

// Unwrap exposes the wrapped cause for errors.Is / errors.As.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Code returns the machine-readable code, which is the kind itself.
func (e *Error) Code() string {
	if e == nil {
		return ""
	}
	return string(e.Kind)
}

// MessageText returns the description without the classification prefix.
func (e *Error) MessageText() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return ""
}

// KindOf extracts the kind from an error chain, returning KindInternal when the
// error is not classified.
func KindOf(err error) Kind {
	if err == nil {
		return ""
	}
	var target *Error
	if errors.As(err, &target) {
		return target.Kind
	}
	return KindInternal
}

// IsKind reports whether err (or anything it wraps) is classified with kind.
func IsKind(err error, kind Kind) bool {
	return KindOf(err) == kind
}

// Is delegates to errors.Is so callers can compare sentinel errors.
func (e *Error) Is(target error) bool {
	if e == nil {
		return false
	}
	other, ok := target.(*Error)
	if !ok {
		return false
	}
	return e.Kind == other.Kind && e.Message == other.Message
}

// Summary renders an error without the kind prefix while keeping the operation
// and the message. It is used when an error is relocated into a new context so
// that the rendered text does not repeat the classification.
func Summary(err error) string {
	if err == nil {
		return ""
	}
	var target *Error
	if errors.As(err, &target) {
		parts := make([]string, 0, 2)
		if target.Op != "" {
			parts = append(parts, target.Op)
		}
		if message := target.MessageText(); message != "" {
			parts = append(parts, message)
		}
		if len(parts) == 0 {
			return string(target.Kind)
		}
		return strings.Join(parts, ": ")
	}
	return err.Error()
}

// ExitCode maps an error to the process exit status used by the CLI.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	switch KindOf(err) {
	case KindInvalidArgument:
		return ExitUsage
	case KindConfig:
		return ExitConfig
	case KindAuthentication:
		return ExitAuth
	case KindCapability:
		return ExitCapability
	case KindPermission:
		return ExitPermission
	case KindWorkspace:
		return ExitWorkspace
	case KindProvider:
		return ExitProvider
	case KindAgent:
		return ExitAgent
	case KindTool:
		return ExitTool
	case KindBudgetExceeded:
		return ExitBudget
	case KindTimeout:
		return ExitTimeout
	case KindCancelled:
		return ExitCancelled
	case KindNotFound:
		return ExitNotFound
	default:
		return ExitError
	}
}

// Multi aggregates several errors under a single kind and a header message.
// The rendered message lists every item on its own indented line so that CLI
// output for validation failures stays readable.
//
// Multi returns nil when errs is empty or contains only nil entries.
func Multi(kind Kind, op, header string, errs ...error) error {
	items := Collect(errs...)
	if len(items) == 0 {
		return nil
	}
	if len(items) == 1 {
		single := items[0]
		var classified *Error
		if errors.As(single, &classified) && classified.Kind == kind {
			return single
		}
	}

	var b strings.Builder
	b.WriteString(header)
	for _, item := range items {
		b.WriteString("\n  - ")
		b.WriteString(strings.ReplaceAll(item.Error(), "\n", "\n    "))
	}

	return &Error{
		Kind:    kind,
		Op:      op,
		Message: b.String(),
		Err:     errors.Join(items...),
	}
}

// Collect flattens a variadic list of errors, dropping nils and expanding
// joined errors one level deep.
func Collect(errs ...error) []error {
	out := make([]error, 0, len(errs))
	for _, err := range errs {
		if err == nil {
			continue
		}
		if joined, ok := err.(interface{ Unwrap() []error }); ok {
			for _, inner := range joined.Unwrap() {
				if inner != nil {
					out = append(out, inner)
				}
			}
			continue
		}
		out = append(out, err)
	}
	return out
}
