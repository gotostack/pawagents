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

import (
	"fmt"
	"strings"

	"github.com/pawagents/pawagents/internal/llm"
)

// Task is one delegated unit of work.
//
// The shape is deliberately explicit rather than a single prompt string. A host
// that says *what* it wants, *why*, *where to look* and *what rules apply*
// gives the subagent a far better chance than dumping a repository into a
// prompt, and it keeps the token cost bounded.
type Task struct {
	// Instruction is what to do.
	Instruction string
	// Background is context the host already knows and the agent cannot
	// discover, for example which proposal a patch implements.
	Background string
	// Files are paths worth starting from. They are hints: the agent is
	// expected to search further when the evidence leads elsewhere.
	Files []string
	// Constraints are rules the host imposes on the answer.
	Constraints []string
}

// IsEmpty reports whether the task carries no instruction.
func (t Task) IsEmpty() bool { return strings.TrimSpace(t.Instruction) == "" }

// buildSystemPrompt assembles the system message.
//
// The order matters: the agent profile comes first because it is the identity
// the operator configured, then the rules that make the answer usable, then the
// output contract. Everything appended here is a property of the runtime, not
// of a particular agent, so it stays identical across profiles.
func buildSystemPrompt(profile Profile, options promptOptions) string {
	var b strings.Builder

	if strings.TrimSpace(profile.SystemPrompt) != "" {
		b.WriteString(strings.TrimSpace(profile.SystemPrompt))
		b.WriteString("\n\n")
	}

	b.WriteString("## How you work\n\n")
	b.WriteString("- You are read-only. You cannot modify files, and you must not ask for it; ")
	b.WriteString("you report what you found so the calling agent can act.\n")
	b.WriteString("- Gather evidence before you conclude. Read the file, or search for the ")
	b.WriteString("symbol, instead of describing what code like this usually does.\n")
	b.WriteString("- Never invent a file name, a line number or a behaviour. If you could not ")
	b.WriteString("verify something, say so and lower your confidence.\n")
	b.WriteString("- Cite evidence as path/to/file.go:123 so the finding can be checked.\n")
	if options.hasTools {
		b.WriteString("- Use the tools that are offered: search to locate code, read to confirm it, ")
		b.WriteString("and the git tools to see what changed.\n")
	}

	if options.structured {
		b.WriteString("\n## Output contract\n\n")
		b.WriteString("Answer with a single JSON object and nothing else:\n\n")
		b.WriteString("{\n")
		b.WriteString("  \"summary\": \"one paragraph on what you reviewed and concluded\",\n")
		b.WriteString("  \"findings\": [\n")
		b.WriteString("    {\n")
		b.WriteString("      \"severity\": \"high|medium|low\",\n")
		b.WriteString("      \"category\": \"correctness|concurrency|security|performance|maintainability\",\n")
		b.WriteString("      \"file\": \"path/to/file.go\",\n")
		b.WriteString("      \"line\": 123,\n")
		b.WriteString("      \"title\": \"short statement of the problem\",\n")
		b.WriteString("      \"description\": \"why it is a problem\",\n")
		b.WriteString("      \"evidence\": \"the code you actually read\",\n")
		b.WriteString("      \"suggestion\": \"what to change\",\n")
		b.WriteString("      \"confidence\": 0.9\n")
		b.WriteString("    }\n")
		b.WriteString("  ]\n")
		b.WriteString("}\n\n")
		b.WriteString("An empty findings list is a valid answer when you found nothing. ")
		b.WriteString("Do not pad it with style preferences.\n")
	} else {
		b.WriteString("\n## Output contract\n\n")
		b.WriteString("Answer in plain markdown. Lead with your conclusion, then the evidence.\n")
	}

	return b.String()
}

// buildUserMessage assembles the delegated task.
func buildUserMessage(task Task) string {
	var b strings.Builder

	b.WriteString("## Task\n\n")
	b.WriteString(strings.TrimSpace(task.Instruction))
	b.WriteString("\n")

	if background := strings.TrimSpace(task.Background); background != "" {
		b.WriteString("\n## Background\n\n")
		b.WriteString(background)
		b.WriteString("\n")
	}

	if len(task.Files) > 0 {
		b.WriteString("\n## Files to start from\n\n")
		for _, file := range task.Files {
			fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(file))
		}
		b.WriteString("\nThese are starting points, not the full scope. Search further when the ")
		b.WriteString("evidence leads elsewhere.\n")
	}

	if len(task.Constraints) > 0 {
		b.WriteString("\n## Constraints\n\n")
		for _, constraint := range task.Constraints {
			fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(constraint))
		}
	}

	return b.String()
}

// promptOptions carries the runtime facts the system prompt has to state.
type promptOptions struct {
	// hasTools reports whether any tool was granted.
	hasTools bool
	// structured reports whether the answer must be the finding envelope.
	structured bool
}

// estimateTokens approximates the token count of a conversation.
//
// It is a character based estimate rather than a tokeniser, because shipping a
// tokeniser per model would be a large dependency for a budgeting decision that
// only needs to be roughly right. Four characters per token matches English and
// code closely enough to decide when to compact.
func estimateTokens(messages []llm.Message) int {
	total := 0
	for _, message := range messages {
		total += 4 // the role and framing overhead
		for _, part := range message.Content {
			total += estimateTextTokens(part.Text)
		}
		for _, call := range message.ToolCalls {
			total += estimateTextTokens(call.Name) + estimateTextTokens(call.Arguments) + 4
		}
		if message.ToolResult != nil {
			total += estimateTextTokens(message.ToolResult.Content) + 4
		}
	}
	return total
}

// estimateTextTokens approximates the tokens of a text.
func estimateTextTokens(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + 3) / 4
}

// fitsContext reports whether the conversation fits the usable part of the
// context window.
//
// The usable part is deliberately below the full window: the model still has to
// produce an answer, and tool definitions take space that the estimate does not
// count.
func fitsContext(messages []llm.Message, contextTokens int) bool {
	if contextTokens <= 0 {
		return true
	}
	usable := contextTokens - contextTokens/4
	return estimateTokens(messages) <= usable
}
