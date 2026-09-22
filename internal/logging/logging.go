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

// Package logging configures the structured logger used by the CLI and the
// agent runtime.
//
// Two rules drive the design:
//
//  1. Logs go to stderr so that stdout stays reserved for command output and
//     for the MCP stdio protocol.
//  2. Values whose key looks like a credential are redacted before they reach
//     the handler, so API keys cannot leak into terminal output or log files.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/pawagents/pawagents/internal/security"
)

// RedactedPlaceholder replaces sensitive values. It is re-exported from the
// security package so that log consumers do not need a second import.
const RedactedPlaceholder = security.RedactedPlaceholder

// Formats supported by New.
const (
	FormatText = "text"
	FormatJSON = "json"
)

// DefaultLevel is the log level used when none is configured.
const DefaultLevel = "info"

// Options controls logger construction.
type Options struct {
	// Level is one of debug, info, warn, error. Empty means info.
	Level string
	// Format is text or json. Empty means text.
	Format string
	// Writer receives the log records. Empty means os.Stderr.
	Writer io.Writer
	// Redact lists glob patterns matched against attribute keys. Empty means
	// DefaultRedactPatterns.
	Redact []string
	// AddSource includes the source location in every record.
	AddSource bool
}

// DefaultRedactPatterns matches environment-style secret names.
func DefaultRedactPatterns() []string { return security.DefaultSecretPatterns() }

// New builds a logger from opts.
func New(opts Options) (*slog.Logger, error) {
	level, err := ParseLevel(opts.Level)
	if err != nil {
		return nil, err
	}

	writer := opts.Writer
	if writer == nil {
		writer = os.Stderr
	}

	handlerOpts := &slog.HandlerOptions{
		Level:     level,
		AddSource: opts.AddSource || level <= slog.LevelDebug,
	}

	var handler slog.Handler
	switch strings.ToLower(strings.TrimSpace(opts.Format)) {
	case "", FormatText:
		handler = slog.NewTextHandler(writer, handlerOpts)
	case FormatJSON:
		handler = slog.NewJSONHandler(writer, handlerOpts)
	default:
		return nil, fmt.Errorf("unsupported log format %q (want %s or %s)", opts.Format, FormatText, FormatJSON)
	}

	patterns := opts.Redact
	if patterns == nil {
		patterns = DefaultRedactPatterns()
	}

	return slog.New(&redactHandler{inner: handler, patterns: patterns}), nil
}

// Setup builds a logger and installs it as the slog default.
func Setup(opts Options) (*slog.Logger, error) {
	logger, err := New(opts)
	if err != nil {
		return nil, err
	}
	slog.SetDefault(logger)
	return logger, nil
}

// ParseLevel converts a level name into a slog.Level. An empty string means
// info.
func ParseLevel(level string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "":
		return slog.LevelInfo, nil
	case "debug", "trace":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error", "err":
		return slog.LevelError, nil
	case "off", "none", "silent":
		return slog.Level(127), nil
	default:
		return 0, fmt.Errorf("unsupported log level %q (want debug, info, warn or error)", level)
	}
}

// IsSensitiveKey reports whether an attribute key should be redacted.
func IsSensitiveKey(key string, patterns []string) bool {
	return security.IsSensitiveKey(key, patterns)
}

// redactHandler rewrites attribute values of sensitive keys before delegating
// to the wrapped handler.
type redactHandler struct {
	inner    slog.Handler
	patterns []string
}

func (h *redactHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *redactHandler) Handle(ctx context.Context, record slog.Record) error {
	clone := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		clone.AddAttrs(h.redact(attr))
		return true
	})
	return h.inner.Handle(ctx, clone)
}

func (h *redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		redacted = append(redacted, h.redact(attr))
	}
	return &redactHandler{inner: h.inner.WithAttrs(redacted), patterns: h.patterns}
}

func (h *redactHandler) WithGroup(name string) slog.Handler {
	return &redactHandler{inner: h.inner.WithGroup(name), patterns: h.patterns}
}

func (h *redactHandler) redact(attr slog.Attr) slog.Attr {
	attr.Value = attr.Value.Resolve()
	if attr.Value.Kind() == slog.KindGroup {
		group := attr.Value.Group()
		for i := range group {
			group[i] = h.redact(group[i])
		}
		attr.Value = slog.GroupValue(group...)
		return attr
	}
	if IsSensitiveKey(attr.Key, h.patterns) {
		attr.Value = slog.StringValue(RedactedPlaceholder)
	}
	return attr
}

// Redact rewrites the value of every sensitive key found in attrs in place and
// returns the attribute list. It is used by non-slog call sites such as
// session persistence and telemetry export.
func Redact(patterns []string, attrs ...slog.Attr) []slog.Attr {
	if patterns == nil {
		patterns = DefaultRedactPatterns()
	}
	handler := &redactHandler{patterns: patterns}
	out := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		out = append(out, handler.redact(attr))
	}
	return out
}
