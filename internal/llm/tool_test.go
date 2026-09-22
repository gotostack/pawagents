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
	"strings"
	"testing"
)

func TestIsValidToolName(t *testing.T) {
	valid := []string{"repo.read", "repo.search", "git.diff", "git.log", "repo.list"}
	for _, name := range valid {
		if !IsValidToolName(name) {
			t.Fatalf("%q should be a valid tool name", name)
		}
	}

	invalid := []string{"read", "repo.", ".read", "Repo.read", "repo.Read", "repo..read", "repo-read", "shell exec"}
	for _, name := range invalid {
		if IsValidToolName(name) {
			t.Fatalf("%q should not be a valid tool name", name)
		}
	}

	if !strings.Contains(ToolNamePattern(), "namespace") && ToolNamePattern() == "" {
		t.Fatal("ToolNamePattern must not be empty")
	}
}

func TestSchemaBuilders(t *testing.T) {
	schema := ObjectSchema(map[string]JSONSchema{
		"query": StringSchema().WithDescription("text to search for"),
		"limit": IntegerSchema(),
		"mode":  EnumSchema("fast", "thorough"),
		"tags":  ArraySchema(StringSchema()),
	}, "query")

	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("Marshal() returned %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal() returned %v", err)
	}
	if decoded["type"] != "object" {
		t.Fatalf("type = %v", decoded["type"])
	}
	properties, ok := decoded["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v", decoded["properties"])
	}
	for _, name := range []string{"query", "limit", "mode", "tags"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("property %q is missing", name)
		}
	}
	required, ok := decoded["required"].([]any)
	if !ok || len(required) != 1 || required[0] != "query" {
		t.Fatalf("required = %#v", decoded["required"])
	}
	if err := schema.Validate(); err != nil {
		t.Fatalf("Validate() returned %v", err)
	}

	if NumberSchema()["type"] != "number" || BooleanSchema()["type"] != "boolean" {
		t.Fatal("scalar builders must set the type")
	}
}

func TestSchemaCloneIsDeep(t *testing.T) {
	original := ObjectSchema(map[string]JSONSchema{"query": StringSchema()})
	clone := original.Clone()

	originalProperties, ok := original["properties"].(map[string]any)
	if !ok {
		t.Fatalf("original properties = %#v", original["properties"])
	}
	originalProperties["extra"] = map[string]any{"type": "string"}

	cloneProperties, ok := clone["properties"].(map[string]any)
	if !ok {
		t.Fatalf("clone properties = %#v", clone["properties"])
	}
	if _, ok := cloneProperties["extra"]; ok {
		t.Fatal("the clone shares the properties map with the original")
	}
	if _, ok := cloneProperties["query"]; !ok {
		t.Fatal("the clone lost the original properties")
	}
}

func TestSchemaFieldAccess(t *testing.T) {
	schema := ObjectSchema(map[string]JSONSchema{"query": StringSchema()})

	if value, ok := schema.Field("properties.query"); !ok {
		t.Fatal("Field should find a nested property")
	} else if nested, ok := value.(map[string]any); !ok || nested["type"] != "string" {
		t.Fatalf("nested = %#v", value)
	}
	if _, ok := schema.Field("properties.absent"); ok {
		t.Fatal("Field should report a missing property")
	}
	if _, ok := schema.Field("absent.deep.path"); ok {
		t.Fatal("Field should stop at the first missing segment")
	}
}

func TestSchemaValidate(t *testing.T) {
	tests := []struct {
		name    string
		schema  JSONSchema
		wantErr string
	}{
		{name: "empty schema", schema: JSONSchema{}, wantErr: "empty"},
		{name: "unsupported type", schema: JSONSchema{"type": "date"}, wantErr: "unsupported schema type"},
		{name: "type is not a string", schema: JSONSchema{"type": 3}, wantErr: "must be a string"},
		{name: "properties is not an object", schema: JSONSchema{"type": "object", "properties": "x"}, wantErr: "must be an object"},
		{name: "required is not a list", schema: JSONSchema{"type": "object", "required": "query"}, wantErr: "list of property names"},
		{
			name:    "required without properties",
			schema:  JSONSchema{"type": "object", "required": []any{"query"}},
			wantErr: "declares no properties",
		},
		{
			name: "required property is not defined",
			schema: JSONSchema{
				"type":       "object",
				"properties": map[string]any{"query": map[string]any{"type": "string"}},
				"required":   []any{"other"},
			},
			wantErr: "does not define it in properties",
		},
		{
			name: "nested property is not an object",
			schema: JSONSchema{
				"type":       "object",
				"properties": map[string]any{"query": "string"},
			},
			wantErr: "is not an object",
		},
		{
			name:    "items is not an object",
			schema:  JSONSchema{"type": "array", "items": "string"},
			wantErr: "must be an object",
		},
		{name: "valid array", schema: ArraySchema(StringSchema())},
		{name: "valid enum", schema: EnumSchema("a", "b")},
		{name: "type is optional", schema: JSONSchema{"description": "anything"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.schema.Validate()
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want %q", test.wantErr)
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Validate() = %q, want it to contain %q", err.Error(), test.wantErr)
			}
		})
	}
}

func TestToolDefinitionValidate(t *testing.T) {
	valid := ToolDefinition{
		Name:        "repo.read",
		Description: "Read a file from the workspace.",
		InputSchema: ObjectSchema(map[string]JSONSchema{"path": StringSchema()}, "path"),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	if valid.IsZero() {
		t.Fatal("a populated definition must not be zero")
	}

	tests := []struct {
		name    string
		mutate  func(ToolDefinition) ToolDefinition
		wantErr string
	}{
		{
			name:    "missing name",
			mutate:  func(d ToolDefinition) ToolDefinition { d.Name = ""; return d },
			wantErr: "tool name is required",
		},
		{
			name:    "invalid name",
			mutate:  func(d ToolDefinition) ToolDefinition { d.Name = "read"; return d },
			wantErr: "must match",
		},
		{
			name:    "missing description",
			mutate:  func(d ToolDefinition) ToolDefinition { d.Description = "  "; return d },
			wantErr: "missing a description",
		},
		{
			name:    "missing schema",
			mutate:  func(d ToolDefinition) ToolDefinition { d.InputSchema = nil; return d },
			wantErr: "missing an input schema",
		},
		{
			name:    "broken schema",
			mutate:  func(d ToolDefinition) ToolDefinition { d.InputSchema = JSONSchema{"type": "date"}; return d },
			wantErr: "unsupported schema type",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.mutate(valid).Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want %q", test.wantErr)
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Validate() = %q, want it to contain %q", err.Error(), test.wantErr)
			}
		})
	}
}

func TestValidateToolDefinitionsRejectsDuplicates(t *testing.T) {
	definition := ToolDefinition{
		Name:        "repo.read",
		Description: "Read a file.",
		InputSchema: ObjectSchema(nil),
	}
	if err := ValidateToolDefinitions([]ToolDefinition{definition, definition}); err == nil {
		t.Fatal("duplicate tool names must be rejected")
	} else if !strings.Contains(err.Error(), "defined more than once") {
		t.Fatalf("error = %q", err.Error())
	}
	if err := ValidateToolDefinitions([]ToolDefinition{definition}); err != nil {
		t.Fatalf("a single definition must be valid: %v", err)
	}
	if err := ValidateToolDefinitions(nil); err != nil {
		t.Fatalf("an empty list must be valid: %v", err)
	}
}

func TestToolChoiceValidate(t *testing.T) {
	offered := []ToolDefinition{{Name: "repo.read"}}

	if err := (ToolChoice{}).Validate(offered); err != nil {
		t.Fatalf("the zero value means auto: %v", err)
	}
	if err := (ToolChoice{Mode: ToolChoiceNone}).Validate(nil); err != nil {
		t.Fatalf("none must be valid without tools: %v", err)
	}
	if err := (ToolChoice{Mode: ToolChoiceAuto}).Validate(nil); err != nil {
		t.Fatalf("auto without tools is a valid tool-less request: %v", err)
	}
	if err := (ToolChoice{Mode: ToolChoiceRequired}).Validate(nil); err == nil {
		t.Fatal("required must be rejected without tools")
	}
	if err := (ToolChoice{Mode: ToolChoiceRequired}).Validate(offered); err != nil {
		t.Fatalf("required with tools must be valid: %v", err)
	}
	if err := (ToolChoice{Mode: ToolChoiceFunction, Name: "repo.read"}).Validate(offered); err != nil {
		t.Fatalf("function choice must be valid: %v", err)
	}
	if err := (ToolChoice{Mode: ToolChoiceFunction}).Validate(offered); err == nil {
		t.Fatal("function choice without a name must be rejected")
	}
	if err := (ToolChoice{Mode: ToolChoiceFunction, Name: "git.diff"}).Validate(offered); err == nil {
		t.Fatal("function choice naming an unoffered tool must be rejected")
	}
	if err := (ToolChoice{Mode: "always"}).Validate(offered); err == nil {
		t.Fatal("an unknown mode must be rejected")
	}
}
