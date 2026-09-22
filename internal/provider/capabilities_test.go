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
	"testing"

	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/llm"
)

func boolPtr(value bool) *bool { return &value }
func intPtr(value int) *int    { return &value }

func TestStaticCapabilities(t *testing.T) {
	ollama := StaticCapabilities(config.ProviderTypeOllama)
	if ollama.ToolCalling {
		t.Fatal("ollama tool calling must not be assumed: models differ")
	}
	if !ollama.Streaming || !ollama.SystemMessage {
		t.Fatalf("ollama capabilities = %+v", ollama)
	}
	if ollama.MaxContextTokens != ConservativeContextTokens {
		t.Fatalf("ollama context = %d", ollama.MaxContextTokens)
	}

	anthropic := StaticCapabilities(config.ProviderTypeAnthropic)
	if !anthropic.ToolCalling || !anthropic.ParallelTools || !anthropic.StructuredOutput {
		t.Fatalf("anthropic capabilities = %+v", anthropic)
	}
	if anthropic.MaxContextTokens != CloudContextTokens {
		t.Fatalf("anthropic context = %d", anthropic.MaxContextTokens)
	}

	compat := StaticCapabilities(config.ProviderTypeOpenAICompat)
	if !compat.ToolCalling || compat.ParallelTools {
		t.Fatalf("compatible endpoints are conservative about concurrency: %+v", compat)
	}
	if compat.MaxContextTokens != ConservativeContextTokens {
		t.Fatalf("compatible context = %d", compat.MaxContextTokens)
	}

	planned := StaticCapabilities(config.ProviderTypeGemini)
	if planned.ToolCalling {
		t.Fatal("a planned provider must not claim capabilities it cannot verify")
	}

	unknown := StaticCapabilities("made-up")
	if !unknown.Streaming || unknown.MaxContextTokens != ConservativeContextTokens {
		t.Fatalf("unknown provider capabilities = %+v", unknown)
	}
}

func TestApplyOverrides(t *testing.T) {
	base := llm.ModelCapabilities{Streaming: true, MaxContextTokens: 8192}

	if got := ApplyOverrides(base, nil); got != base {
		t.Fatalf("ApplyOverrides(nil) = %+v, want the base unchanged", got)
	}

	overrides := &config.CapabilityOverrides{
		ToolCalling:      boolPtr(true),
		ParallelTools:    boolPtr(true),
		Streaming:        boolPtr(false),
		StructuredOutput: boolPtr(true),
		Vision:           boolPtr(true),
		Reasoning:        boolPtr(true),
		SystemMessage:    boolPtr(false),
		MaxContextTokens: intPtr(65536),
	}

	got := ApplyOverrides(base, overrides)
	want := llm.ModelCapabilities{
		ToolCalling:      true,
		ParallelTools:    true,
		Streaming:        false,
		StructuredOutput: true,
		Vision:           true,
		Reasoning:        true,
		SystemMessage:    false,
		MaxContextTokens: 65536,
	}
	if got != want {
		t.Fatalf("ApplyOverrides() = %+v, want %+v", got, want)
	}

	// A non-positive context override is ignored instead of producing a model
	// with no usable window.
	ignored := ApplyOverrides(base, &config.CapabilityOverrides{MaxContextTokens: intPtr(0)})
	if ignored.MaxContextTokens != base.MaxContextTokens {
		t.Fatalf("context = %d, want %d", ignored.MaxContextTokens, base.MaxContextTokens)
	}

	// The override values must not be mutated by ApplyOverrides.
	if overrides.ToolCalling == nil || !*overrides.ToolCalling {
		t.Fatal("ApplyOverrides must not modify the override values")
	}
	if overrides.MaxContextTokens == nil || *overrides.MaxContextTokens != 65536 {
		t.Fatal("ApplyOverrides must not modify the override context window")
	}
}

func TestResolvePrecedence(t *testing.T) {
	model := &config.Model{
		Primary:          &config.ModelRef{Provider: "local-ollama", Model: "qwen3-coder"},
		MaxContextTokens: 32768,
		MaxOutputTokens:  1024,
	}

	// 1. Without a probe the static table applies, then the declared window.
	fromStatic := Resolve(config.ProviderTypeOllama, model, nil)
	if fromStatic.MaxContextTokens != 32768 {
		t.Fatalf("context = %d, want the declared window", fromStatic.MaxContextTokens)
	}
	if fromStatic.MaxOutputTokens != 1024 {
		t.Fatalf("output tokens = %d, want the declared budget", fromStatic.MaxOutputTokens)
	}
	if fromStatic.ToolCalling {
		t.Fatal("tool calling must still be unknown")
	}

	// 2. A probe result replaces the static table.
	probe := llm.ModelCapabilities{
		ToolCalling:      true,
		Streaming:        true,
		SystemMessage:    true,
		MaxContextTokens: 262144,
		MaxOutputTokens:  8192,
	}
	fromProbe := Resolve(config.ProviderTypeOllama, model, &probe)
	if !fromProbe.ToolCalling {
		t.Fatal("the probe result must enable tool calling")
	}
	if fromProbe.MaxContextTokens != 32768 {
		t.Fatalf("context = %d, want the declared window to win over the probe", fromProbe.MaxContextTokens)
	}

	// 3. Explicit overrides win over the probe.
	modelWithOverrides := *model
	modelWithOverrides.Capabilities = &config.CapabilityOverrides{StructuredOutput: boolPtr(true)}
	fromOverrides := Resolve(config.ProviderTypeOllama, &modelWithOverrides, &probe)
	if !fromOverrides.StructuredOutput {
		t.Fatal("the explicit override must win over the probe result")
	}
}

func TestResolveNormalizesBudgets(t *testing.T) {
	// An unknown provider type with no declared window still gets a usable,
	// positive budget.
	capabilities := Resolve("made-up", nil, nil)
	if capabilities.MaxContextTokens != ConservativeContextTokens {
		t.Fatalf("context = %d", capabilities.MaxContextTokens)
	}
	if capabilities.MaxOutputTokens != ConservativeMaxOutputTokens {
		t.Fatalf("output tokens = %d", capabilities.MaxOutputTokens)
	}

	// An output budget larger than the window is clamped, otherwise every
	// request would ask for more tokens than the model can hold.
	clamped := Resolve(config.ProviderTypeOllama, &config.Model{
		MaxContextTokens: 2048,
		MaxOutputTokens:  99999,
	}, nil)
	if clamped.MaxOutputTokens != 2048 {
		t.Fatalf("output tokens = %d, want them clamped to the window", clamped.MaxOutputTokens)
	}
}
