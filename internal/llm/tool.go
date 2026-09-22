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
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/pawagents/pawagents/internal/apperrors"
)

// toolNamePattern is the single definition of a valid tool name. The tool
// runtime, the configuration validator and the provider adapters all rely on
// it so that a tool that passes validation is also a tool the runtime can
// address.
var toolNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)

// ToolNamePattern returns the source of the tool name pattern, for messages
// that need to show it to a user.
func ToolNamePattern() string { return toolNamePattern.String() }

// IsValidToolName reports whether name is a "namespace.tool" identifier.
func IsValidToolName(name string) bool { return toolNamePattern.MatchString(name) }

// JSONSchema is a JSON Schema document describing tool input or structured
// output.
//
// It is a raw map rather than a modelled struct on purpose: providers differ
// in which keywords they accept, and modelling the specification would only
// create a translation layer that loses information. The helpers below build
// the common shapes, and Validate catches the mistakes that would otherwise
// surface as an opaque provider error.
type JSONSchema map[string]any

// ObjectSchema builds an object schema from its properties.
func ObjectSchema(properties map[string]JSONSchema, required ...string) JSONSchema {
	schema := JSONSchema{"type": "object"}
	if len(properties) > 0 {
		asAny := make(map[string]any, len(properties))
		for name, property := range properties {
			asAny[name] = map[string]any(property)
		}
		schema["properties"] = asAny
	}
	if len(required) > 0 {
		schema["required"] = append([]string(nil), required...)
	}
	return schema
}

// WithDescription returns a copy of the schema carrying a description.
func (s JSONSchema) WithDescription(description string) JSONSchema {
	out := s.Clone()
	out["description"] = description
	return out
}

// StringSchema builds a string schema.
func StringSchema() JSONSchema { return JSONSchema{"type": "string"} }

// EnumSchema builds a string schema restricted to a set of values.
func EnumSchema(values ...string) JSONSchema {
	enum := make([]any, 0, len(values))
	for _, value := range values {
		enum = append(enum, value)
	}
	return JSONSchema{"type": "string", "enum": enum}
}

// NumberSchema builds a number schema.
func NumberSchema() JSONSchema { return JSONSchema{"type": "number"} }

// IntegerSchema builds an integer schema.
func IntegerSchema() JSONSchema { return JSONSchema{"type": "integer"} }

// BooleanSchema builds a boolean schema.
func BooleanSchema() JSONSchema { return JSONSchema{"type": "boolean"} }

// ArraySchema builds an array schema for the given item schema.
func ArraySchema(items JSONSchema) JSONSchema {
	return JSONSchema{"type": "array", "items": map[string]any(items)}
}

// Clone returns a deep copy of the schema.
func (s JSONSchema) Clone() JSONSchema {
	if s == nil {
		return nil
	}
	out := make(JSONSchema, len(s))
	for key, value := range s {
		out[key] = cloneJSONValue(value)
	}
	return out
}

// IsZero reports whether the schema is empty.
func (s JSONSchema) IsZero() bool { return len(s) == 0 }

// Field reads a nested field from the schema, for example "properties.query".
func (s JSONSchema) Field(path string) (any, bool) {
	current := any(map[string]any(s))
	for _, segment := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[segment]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// Validate checks the schema is a usable JSON Schema for a tool definition.
//
// The check is intentionally shallow: an unknown type is rejected, an object
// must declare its properties as an object, and every required property must
// exist in properties. Anything deeper is left to the provider.
func (s JSONSchema) Validate() error {
	if s.IsZero() {
		return apperrors.New(apperrors.KindInvalidArgument, "llm.schema",
			"the schema is empty")
	}

	if kind, ok := s["type"]; ok {
		text, ok := kind.(string)
		if !ok {
			return apperrors.New(apperrors.KindInvalidArgument, "llm.schema",
				"the schema type must be a string")
		}
		switch text {
		case "object", "array", "string", "number", "integer", "boolean", "null":
		default:
			return apperrors.New(apperrors.KindInvalidArgument, "llm.schema",
				"unsupported schema type %q", text)
		}
	}

	properties, _ := s["properties"].(map[string]any)
	if raw, ok := s["properties"]; ok && raw != nil && properties == nil {
		return apperrors.New(apperrors.KindInvalidArgument, "llm.schema",
			"schema properties must be an object")
	}

	if required, ok := s["required"]; ok {
		names, ok := toStringSlice(required)
		if !ok {
			return apperrors.New(apperrors.KindInvalidArgument, "llm.schema",
				"schema required must be a list of property names")
		}
		for _, name := range names {
			if properties == nil {
				return apperrors.New(apperrors.KindInvalidArgument, "llm.schema",
					"schema requires %q but declares no properties", name)
			}
			if _, ok := properties[name]; !ok {
				return apperrors.New(apperrors.KindInvalidArgument, "llm.schema",
					"schema requires %q but does not define it in properties", name)
			}
		}
	}

	if err := validateNested(s, "properties", properties); err != nil {
		return err
	}
	if err := validateNested(s, "items", nil); err != nil {
		return err
	}
	return nil
}

func validateNested(schema JSONSchema, key string, direct map[string]any) error {
	if direct != nil {
		for name, raw := range direct {
			nested, ok := raw.(map[string]any)
			if !ok {
				return apperrors.New(apperrors.KindInvalidArgument, "llm.schema",
					"schema %q entry %q is not an object", key, name)
			}
			if err := JSONSchema(nested).Validate(); err != nil {
				return fmt.Errorf("schema %s.%s: %w", key, name, err)
			}
		}
		return nil
	}

	raw, ok := schema[key]
	if !ok || raw == nil {
		return nil
	}
	nested, ok := raw.(map[string]any)
	if !ok {
		return apperrors.New(apperrors.KindInvalidArgument, "llm.schema",
			"schema %q must be an object", key)
	}
	return JSONSchema(nested).Validate()
}

// ToolDefinition describes one tool offered to a model.
type ToolDefinition struct {
	// Name is the "namespace.tool" identifier the model must use.
	Name string
	// Description tells the model when to call the tool. It is the strongest
	// lever on tool selection quality, so it must say what the tool does
	// *and* when not to use it.
	Description string
	// InputSchema describes the arguments object.
	InputSchema JSONSchema
	// Strict asks the provider to enforce the schema when it can. Providers
	// that cannot must ignore the flag rather than reject the request.
	Strict bool
}

// IsZero reports whether the definition is unset.
func (d ToolDefinition) IsZero() bool {
	return d.Name == "" && d.Description == ""
}

// Validate checks the definition is dispatchable.
func (d ToolDefinition) Validate() error {
	var problems []error

	if strings.TrimSpace(d.Name) == "" {
		problems = append(problems, apperrors.New(apperrors.KindInvalidArgument, "llm.tool",
			"tool name is required"))
	} else if !IsValidToolName(d.Name) {
		problems = append(problems, apperrors.New(apperrors.KindInvalidArgument, "llm.tool",
			"tool name %q must match %s", d.Name, toolNamePattern.String()))
	}
	if strings.TrimSpace(d.Description) == "" {
		problems = append(problems, apperrors.New(apperrors.KindInvalidArgument, "llm.tool",
			"tool %q is missing a description", d.Name))
	}
	if d.InputSchema.IsZero() {
		problems = append(problems, apperrors.New(apperrors.KindInvalidArgument, "llm.tool",
			"tool %q is missing an input schema", d.Name))
	} else if err := d.InputSchema.Validate(); err != nil {
		problems = append(problems, fmt.Errorf("tool %q: %w", d.Name, err))
	}

	return apperrors.Multi(apperrors.KindInvalidArgument, "llm.tool",
		"invalid tool definition", problems...)
}

// ValidateToolDefinitions checks a tool list and rejects duplicate names,
// which would make a tool call ambiguous.
func ValidateToolDefinitions(definitions []ToolDefinition) error {
	var problems []error
	seen := make(map[string]bool, len(definitions))

	for _, definition := range definitions {
		if err := definition.Validate(); err != nil {
			problems = append(problems, err)
			continue
		}
		if seen[definition.Name] {
			problems = append(problems, apperrors.New(apperrors.KindInvalidArgument, "llm.tool",
				"tool %q is defined more than once", definition.Name))
		}
		seen[definition.Name] = true
	}

	return apperrors.Multi(apperrors.KindInvalidArgument, "llm.tools",
		"invalid tool definitions", problems...)
}

// ToolChoiceMode constrains whether the model may call tools.
type ToolChoiceMode string

// Tool choice modes.
const (
	// ToolChoiceAuto lets the model decide.
	ToolChoiceAuto ToolChoiceMode = "auto"
	// ToolChoiceNone forbids tool calls for this request.
	ToolChoiceNone ToolChoiceMode = "none"
	// ToolChoiceRequired forces at least one tool call.
	ToolChoiceRequired ToolChoiceMode = "required"
	// ToolChoiceFunction forces one named tool.
	ToolChoiceFunction ToolChoiceMode = "function"
)

// ToolChoice tells the provider whether tools may be used.
type ToolChoice struct {
	Mode ToolChoiceMode
	// Name is required when Mode is ToolChoiceFunction.
	Name string
}

// Validate checks the choice against the offered tools.
//
// The zero value means auto, and auto is valid even when no tool is offered:
// an agent without tools is a normal configuration, and the request simply
// carries an empty tool list. Only the modes that actively demand a call
// require tools to exist.
func (c ToolChoice) Validate(offered []ToolDefinition) error {
	mode := c.Mode
	if mode == "" {
		mode = ToolChoiceAuto
	}

	switch mode {
	case ToolChoiceAuto, ToolChoiceNone:
		return nil
	case ToolChoiceRequired:
		if len(offered) == 0 {
			return apperrors.New(apperrors.KindInvalidArgument, "llm.toolchoice",
				"tool choice %q requires at least one tool", mode)
		}
		return nil
	case ToolChoiceFunction:
		if strings.TrimSpace(c.Name) == "" {
			return apperrors.New(apperrors.KindInvalidArgument, "llm.toolchoice",
				"tool choice %q requires a tool name", mode)
		}
		for _, definition := range offered {
			if definition.Name == c.Name {
				return nil
			}
		}
		return apperrors.New(apperrors.KindInvalidArgument, "llm.toolchoice",
			"tool choice names %q which is not offered to the model", c.Name)
	default:
		return apperrors.New(apperrors.KindInvalidArgument, "llm.toolchoice",
			"unsupported tool choice mode %q", c.Mode)
	}
}

func toStringSlice(value any) ([]string, bool) {
	switch typed := value.(type) {
	case []string:
		return typed, true
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, text)
		}
		return out, true
	default:
		return nil, false
	}
}

func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = cloneJSONValue(item)
		}
		return out
	case JSONSchema:
		return typed.Clone()
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, cloneJSONValue(item))
		}
		return out
	default:
		return value
	}
}

// MarshalJSON keeps the schema shape identical to a plain JSON object.
func (s JSONSchema) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any(s))
}
