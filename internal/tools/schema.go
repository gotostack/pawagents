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

package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/pawagents/pawagents/internal/apperrors"
)

// Arguments decodes the JSON object a model produced for a tool call.
//
// Two behaviours matter for a local model: an empty payload is treated as an
// empty object, because a model that calls a tool with no arguments emits
// nothing or "{}"; and an unknown key is rejected rather than ignored, because
// a model that invents an argument must be told so instead of silently
// receiving a result for a different operation.
type Arguments struct {
	values map[string]any
	used   map[string]bool
}

// DecodeArguments parses a tool call payload.
func DecodeArguments(raw json.RawMessage) (*Arguments, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return &Arguments{values: map[string]any{}, used: map[string]bool{}}, nil
	}

	var values map[string]any
	if err := json.Unmarshal([]byte(trimmed), &values); err != nil {
		return nil, apperrors.Wrap(apperrors.KindTool, "tool.arguments",
			"the arguments are not a JSON object", err)
	}
	if values == nil {
		values = map[string]any{}
	}
	return &Arguments{values: values, used: map[string]bool{}}, nil
}

// Has reports whether a key was supplied.
func (a *Arguments) Has(name string) bool {
	_, ok := a.values[name]
	return ok
}

// String returns a string argument.
func (a *Arguments) String(name string, required bool) (string, error) {
	raw, ok := a.values[name]
	if !ok {
		if required {
			return "", missingArgument(name)
		}
		return "", nil
	}
	a.used[name] = true

	value, ok := raw.(string)
	if !ok {
		return "", wrongType(name, "a string", raw)
	}
	return value, nil
}

// Int returns an integer argument, clamped to the given bounds when they are
// positive. A model that asks for 100000 results gets the bound instead of an
// error, which keeps the loop moving.
func (a *Arguments) Int(name string, fallback, min, max int) (int, error) {
	raw, ok := a.values[name]
	if !ok {
		return fallback, nil
	}
	a.used[name] = true

	value := 0
	switch typed := raw.(type) {
	case float64:
		value = int(typed)
	case int:
		value = typed
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, wrongType(name, "an integer", raw)
		}
		value = int(parsed)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, wrongType(name, "an integer", raw)
		}
		value = parsed
	default:
		return 0, wrongType(name, "an integer", raw)
	}

	if min > 0 && value < min {
		value = min
	}
	if max > 0 && value > max {
		value = max
	}
	return value, nil
}

// Bool returns a boolean argument.
func (a *Arguments) Bool(name string, fallback bool) (bool, error) {
	raw, ok := a.values[name]
	if !ok {
		return fallback, nil
	}
	a.used[name] = true

	switch typed := raw.(type) {
	case bool:
		return typed, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "yes", "1":
			return true, nil
		case "false", "no", "0":
			return false, nil
		}
	}
	return false, wrongType(name, "a boolean", raw)
}

// StringSlice returns a list of strings, accepting a single string as a
// one-element list because models often collapse a list with one entry.
func (a *Arguments) StringSlice(name string) ([]string, error) {
	raw, ok := a.values[name]
	if !ok {
		return nil, nil
	}
	a.used[name] = true

	switch typed := raw.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil, nil
		}
		return []string{typed}, nil
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, wrongType(name, "a list of strings", raw)
			}
			out = append(out, text)
		}
		return out, nil
	default:
		return nil, wrongType(name, "a list of strings", raw)
	}
}

// Unused returns the supplied keys the tool never read, sorted. Callers use it
// to reject a payload that names arguments the tool does not have.
func (a *Arguments) Unused() []string {
	out := make([]string, 0, len(a.values))
	for key := range a.values {
		if !a.used[key] {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// RejectUnknown returns an error naming the unused keys. It is called at the
// end of every tool execution.
func (a *Arguments) RejectUnknown() error {
	unused := a.Unused()
	if len(unused) == 0 {
		return nil
	}
	known := make([]string, 0, len(a.used))
	for key := range a.used {
		known = append(known, key)
	}
	sort.Strings(known)

	return apperrors.New(apperrors.KindTool, "tool.arguments",
		"unknown argument(s): %s (accepted: %s)",
		strings.Join(unused, ", "), strings.Join(known, ", ")).
		WithDetails(map[string]any{"unknown": unused, "accepted": known})
}

func missingArgument(name string) error {
	return apperrors.New(apperrors.KindTool, "tool.arguments",
		"argument %q is required", name).WithDetails(map[string]any{"argument": name})
}

func wrongType(name, want string, got any) error {
	return apperrors.New(apperrors.KindTool, "tool.arguments",
		"argument %q must be %s, got %s", name, want, describeValue(got)).
		WithDetails(map[string]any{"argument": name, "value": fmt.Sprintf("%v", got)})
}

func describeValue(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case float64, int, json.Number:
		return "a number"
	case []any:
		return "a list"
	case map[string]any:
		return "an object"
	default:
		return "an unexpected value"
	}
}
