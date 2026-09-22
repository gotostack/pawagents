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

// Compaction limits.
const (
	// elideThreshold is the size above which tool output is replaced by a stub
	// during compaction. Small results are kept because they are often the
	// evidence a finding cites.
	elideThreshold = 2000
	// keepRecentMessages is how many trailing messages compaction never
	// touches, so the model keeps the thread of what it just did.
	keepRecentMessages = 6
	// elisionStub replaces elided content.
	elisionStub = "[earlier tool output elided to stay inside the context window; " +
		"call the tool again with a narrower request if you need it]"
)

// compact reduces a conversation that has grown too large.
//
// The first version of PawAgents discards large tool output from the middle of
// the conversation and keeps a structured marker in its place, which is exactly
// the simple strategy the specification allows for a first release. The
// interface is a pure function over messages so that summarisation can replace
// the body later without touching the loop.
//
// The system message, the delegated task and the most recent exchanges are
// never modified: dropping the task would lose the point of the conversation,
// and dropping the last turns would leave the model unable to continue.
func compact(messages []llm.Message) ([]llm.Message, bool) {
	if len(messages) <= keepRecentMessages+2 {
		return messages, false
	}

	lastIndex := len(messages) - keepRecentMessages
	changed := false

	out := make([]llm.Message, len(messages))
	copy(out, messages)

	for index := 1; index < lastIndex; index++ {
		message := out[index]

		if message.ToolResult != nil && len(message.ToolResult.Content) > elideThreshold {
			clone := message.Clone()
			clone.ToolResult.Content = elisionStub
			clone.ToolResult.Metadata = nil
			out[index] = clone
			changed = true
			continue
		}

		// A long assistant message is usually a tool call with a large
		// argument blob, which the model no longer needs verbatim once the
		// result is in the conversation.
		if message.Role == llm.RoleAssistant && !message.HasToolCalls() &&
			len(message.Text()) > elideThreshold {
			clone := message.Clone()
			clone.Content = []llm.ContentPart{llm.TextPart(elisionStub)}
			out[index] = clone
			changed = true
		}
	}

	if !changed {
		return messages, false
	}
	return out, true
}

// compactionNotice explains what happened, for the log and for a session
// record.
func compactionNotice(before, after int) string {
	return fmt.Sprintf("context compacted from roughly %d to %d tokens", before, after)
}

// summariseHistory renders a short description of what a conversation already
// did, which a future summarising compaction can use in place of the turns it
// removes.
func summariseHistory(messages []llm.Message) string {
	var b strings.Builder
	for _, message := range messages {
		switch message.Role {
		case llm.RoleAssistant:
			for _, call := range message.ToolCalls {
				fmt.Fprintf(&b, "- called %s\n", call.Name)
			}
		case llm.RoleTool:
			if message.ToolResult != nil {
				fmt.Fprintf(&b, "- %s returned %d bytes\n", message.ToolResult.Name,
					len(message.ToolResult.Content))
			}
		}
	}
	return b.String()
}
