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
	"strings"
)

// ModelLister is implemented by providers that can enumerate the models their
// endpoint serves. It is optional: endpoints without a listing simply do not
// implement it, and callers fall back to the configured model names.
type ModelLister interface {
	// ListModels returns the model identifiers the endpoint advertises.
	ListModels(ctx context.Context) ([]string, error)
}

// Health describes the result of a connectivity probe.
type Health struct {
	// Reachable reports whether the endpoint answered.
	Reachable bool `json:"reachable"`
	// Version is the server version when the endpoint reports one.
	Version string `json:"version,omitempty"`
	// Detail is a short human readable status line.
	Detail string `json:"detail,omitempty"`
}

// HealthReporter is implemented by providers that can probe their endpoint
// without running a generation. `pagent provider test` uses it.
type HealthReporter interface {
	// Health probes the endpoint. It must be cheap and must not send a
	// generation request.
	Health(ctx context.Context) (Health, error)
}

// MatchModel reports whether a requested model is present in a listing.
//
// Matching is deliberately forgiving. Endpoints list different identifiers for
// the same model: Ollama appends a tag ("qwen3-coder:latest" for
// "qwen3-coder"), hosted providers list dated snapshots
// ("gpt-4o-2024-08-06" for "gpt-4o"), and gateways expose namespaced aliases.
// An exact match wins, then a tag or dash suffix, then a path-like prefix.
func MatchModel(models []string, model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return true
	}

	for _, candidate := range models {
		if strings.ToLower(strings.TrimSpace(candidate)) == normalized {
			return true
		}
	}
	for _, candidate := range models {
		listed := strings.ToLower(strings.TrimSpace(candidate))
		if strings.HasPrefix(listed, normalized+":") ||
			strings.HasPrefix(listed, normalized+"-") ||
			strings.HasPrefix(listed, normalized+"/") {
			return true
		}
	}
	for _, candidate := range models {
		listed := strings.ToLower(strings.TrimSpace(candidate))
		if strings.HasSuffix(listed, "/"+normalized) {
			return true
		}
	}
	return false
}
