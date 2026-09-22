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

// Package all imports every provider implementation so that their init
// functions run and register the factories.
//
// The CLI and the runtime import this package instead of each provider
// package. Adding a provider therefore means adding one import here, and no
// other composition point changes.
package all

import (
	// OpenAI-compatible chat completions: DeepSeek, DashScope, OpenRouter,
	// LiteLLM, vLLM, company gateways and Ollama's compatibility endpoint.
	_ "github.com/pawagents/pawagents/internal/provider/openaicompat"
)
