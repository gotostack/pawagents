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

package mcpserver

import (
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pawagents/pawagents/internal/apperrors"
)

// failure renders a classified error as the message of a failed tool call.
//
// MCP reports a tool failure as a flag plus text, so the classification has to
// travel inside the text to stay machine readable. Every failure message is
// therefore "Kind: explanation", which lets a host branch on the classification
// without parsing English, and lets a human read the same line unchanged. A
// delegation that failed after it started reports the classification twice:
// here and in the error_kind field of its result envelope.
func failure(err error) error {
	if err == nil {
		return nil
	}

	kind := string(apperrors.KindOf(err))
	message := strings.TrimSpace(apperrors.Summary(err))
	if message == "" {
		message = err.Error()
	}
	// Summary drops the kind prefix already, but an error that was wrapped
	// several times can still start with one; repeating it would be noise.
	message = strings.TrimPrefix(message, kind+": ")
	return fmt.Errorf("%s: %s", kind, message)
}

// readOnlyAnnotations marks a tool that cannot change anything.
//
// Every PawAgents tool is read-only, and saying so in the protocol lets a host
// call one without asking its user for permission.
func readOnlyAnnotations(title string) *mcp.ToolAnnotations {
	closed := false
	return &mcp.ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    true,
		DestructiveHint: &closed,
		IdempotentHint:  true,
		OpenWorldHint:   &closed,
	}
}
