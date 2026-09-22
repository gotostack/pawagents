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

package agent

import "github.com/pawagents/pawagents/internal/llm"

// resultSchema describes the structured result envelope for providers that can
// constrain a completion to a schema.
//
// The schema is deliberately permissive: only the summary and the finding title
// are required, because a model that omits an optional field still produces a
// usable finding, while a schema it cannot satisfy produces no answer at all.
// The prompt asks for the same envelope, so a model without schema support is
// guided by the instruction instead.
func resultSchema() llm.JSONSchema {
	finding := llm.ObjectSchema(map[string]llm.JSONSchema{
		"severity": llm.EnumSchema("high", "medium", "low").
			WithDescription("How much the problem matters."),
		"category": llm.EnumSchema("correctness", "concurrency", "security",
			"performance", "maintainability", "tests", "other").
			WithDescription("What kind of problem it is."),
		"file": llm.StringSchema().
			WithDescription("Repository relative path of the affected file."),
		"line": llm.IntegerSchema().
			WithDescription("One based line number, when the problem sits on a line."),
		"title": llm.StringSchema().
			WithDescription("One sentence statement of the problem."),
		"description": llm.StringSchema().
			WithDescription("Why it is a problem."),
		"evidence": llm.StringSchema().
			WithDescription("The code that was actually read, quoted."),
		"suggestion": llm.StringSchema().
			WithDescription("What to change."),
		"confidence": llm.NumberSchema().
			WithDescription("How sure you are, between 0 and 1."),
	}, "title")

	return llm.ObjectSchema(map[string]llm.JSONSchema{
		"summary": llm.StringSchema().
			WithDescription("One paragraph on what was reviewed and concluded."),
		"findings": llm.ArraySchema(finding).
			WithDescription("Every problem found, most severe first. Empty when nothing was found."),
	}, "summary", "findings")
}
