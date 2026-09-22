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

package ollama

import "encoding/json"

// Wire types for the native Ollama API.
//
// Ollama is not an OpenAI clone: it uses newline delimited JSON instead of
// Server-Sent Events, tool call arguments are objects rather than encoded
// strings, and the model metadata needed for capability detection lives on
// /api/show. Everything is modelled explicitly for that reason.

// chatRequest is the body of POST /api/chat.
type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Tools    []chatTool    `json:"tools,omitempty"`
	// Format carries a JSON schema (or the string "json") to constrain the
	// answer. It is the native structured output mechanism.
	Format json.RawMessage `json:"format,omitempty"`
	Stream *bool           `json:"stream,omitempty"`
	// Think enables or disables the reasoning channel of a thinking model.
	Think *bool `json:"think,omitempty"`
	// Options carries the sampling parameters.
	Options map[string]any `json:"options,omitempty"`
	// KeepAlive controls how long the model stays loaded.
	KeepAlive string `json:"keep_alive,omitempty"`
	// Extra carries the provider extra_body and the per-request extras.
	Extra map[string]any `json:"-"`
}

// chatMessage is one conversation turn.
type chatMessage struct {
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	Thinking  string         `json:"thinking,omitempty"`
	ToolCalls []chatToolCall `json:"tool_calls,omitempty"`
	// ToolName identifies which tool a result message answers. Newer servers
	// require it to match a call when several results arrive together.
	ToolName string `json:"tool_name,omitempty"`
	// Images holds base64 encoded payloads without the data URL prefix.
	Images []string `json:"images,omitempty"`
}

type chatToolCall struct {
	Function chatFunctionCall `json:"function"`
}

type chatFunctionCall struct {
	Name string `json:"name"`
	// Arguments is an object, not a JSON string. Converting between the two
	// shapes is the adapter's job.
	Arguments map[string]any `json:"arguments"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// chatResponse is one newline delimited JSON object of the chat stream.
type chatResponse struct {
	Model           string       `json:"model"`
	CreatedAt       string       `json:"created_at"`
	Message         *chatMessage `json:"message"`
	Done            bool         `json:"done"`
	DoneReason      string       `json:"done_reason"`
	PromptEvalCount int          `json:"prompt_eval_count"`
	EvalCount       int          `json:"eval_count"`
	TotalDuration   int64        `json:"total_duration"`
	Error           string       `json:"error"`
}

// tagsResponse is the body of GET /api/tags.
type tagsResponse struct {
	Models []tagEntry `json:"models"`
}

type tagEntry struct {
	Name       string `json:"name"`
	Model      string `json:"model"`
	Size       int64  `json:"size"`
	ModifiedAt string `json:"modified_at"`
}

// showResponse is the body of POST /api/show.
type showResponse struct {
	Details      map[string]any `json:"details"`
	ModelInfo    map[string]any `json:"model_info"`
	Capabilities []string       `json:"capabilities"`
	Template     string         `json:"template"`
	Parameters   string         `json:"parameters"`
}

// versionResponse is the body of GET /api/version.
type versionResponse struct {
	Version string `json:"version"`
}

// errorResponse is returned with a non 200 status.
type errorResponse struct {
	Error string `json:"error"`
}
