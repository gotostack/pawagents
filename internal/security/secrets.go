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

// Package security owns the workspace guard, the permission checks and the
// secret redaction rules used by PawAgents.
//
// This file implements secret handling: a single definition of what counts as
// a credential-looking key, shared by the logging layer, the configuration
// renderer and (later) the session store, so that no subsystem invents its own
// idea of what must stay hidden.
package security

import (
	"path"
	"strings"
)

// RedactedPlaceholder replaces secret values in any output.
const RedactedPlaceholder = "[REDACTED]"

// DefaultSecretPatterns matches environment-style credential names. The
// patterns are matched case-insensitively against keys with path.Match
// semantics, which is why they are stored uppercase.
func DefaultSecretPatterns() []string {
	return []string{
		"*_TOKEN",
		"*_KEY",
		"*_SECRET",
		"*_PASSWORD",
		"*_PASSWD",
		"*_CREDENTIAL",
		"*_APIKEY",
		"*_API_KEY",
	}
}

// exactSensitiveKeys are always treated as secrets regardless of the configured
// patterns, because they routinely appear as attribute or header names.
var exactSensitiveKeys = map[string]struct{}{
	"api_key":             {},
	"apikey":              {},
	"api-key":             {},
	"authorization":       {},
	"proxy-authorization": {},
	"auth":                {},
	"password":            {},
	"passwd":              {},
	"secret":              {},
	"token":               {},
	"access_token":        {},
	"refresh_token":       {},
	"id_token":            {},
	"client_secret":       {},
	"private_key":         {},
	"session_key":         {},
	"cookie":              {},
	"set-cookie":          {},
	"bearer":              {},
}

// IsSensitiveKey reports whether a key looks like a credential and must be
// redacted. A nil patterns argument selects DefaultSecretPatterns.
func IsSensitiveKey(key string, patterns []string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	if normalized == "" {
		return false
	}
	if _, ok := exactSensitiveKeys[normalized]; ok {
		return true
	}
	if patterns == nil {
		patterns = DefaultSecretPatterns()
	}
	upper := strings.ToUpper(normalized)
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if ok, err := path.Match(strings.ToUpper(pattern), upper); err == nil && ok {
			return true
		}
	}
	return false
}

// RedactValue returns the placeholder when the key is sensitive, otherwise the
// original value.
func RedactValue(key, value string, patterns []string) string {
	if IsSensitiveKey(key, patterns) {
		return RedactedPlaceholder
	}
	return value
}

// RedactStringMap copies a map, redacting the values of sensitive keys. A nil
// input returns nil so that omitempty keeps working.
func RedactStringMap(in map[string]string, patterns []string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = RedactValue(key, value, patterns)
	}
	return out
}

// RedactAnyMap copies a map whose values are arbitrary JSON values, redacting
// the values of sensitive keys recursively.
func RedactAnyMap(in map[string]any, patterns []string) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		if IsSensitiveKey(key, patterns) {
			out[key] = RedactedPlaceholder
			continue
		}
		if nested, ok := value.(map[string]any); ok {
			out[key] = RedactAnyMap(nested, patterns)
			continue
		}
		out[key] = value
	}
	return out
}

// IsEnvSecret reports whether an environment variable name must never be handed
// to a model. It combines the operator configured patterns with the built-in
// credential heuristics.
func IsEnvSecret(name string, patterns []string) bool {
	if IsSensitiveKey(name, patterns) {
		return true
	}
	upper := strings.ToUpper(strings.TrimSpace(name))
	switch upper {
	case "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"GOOGLE_APPLICATION_CREDENTIALS", "AZURE_CLIENT_SECRET",
		"SSH_AUTH_SOCK", "GPG_AGENT_INFO", "ANTHROPIC_API_KEY",
		"OPENAI_API_KEY":
		return true
	}
	return strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "SECRET") ||
		strings.HasSuffix(upper, "_KEY")
}
