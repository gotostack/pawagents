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
	"context"
	"errors"
	"testing"

	"github.com/pawagents/pawagents/internal/llm"
)

// fakeProvider is a minimal implementation of the Provider contract. It exists
// so that the interface is exercised by a test: if the contract becomes
// impossible to satisfy without vendor types, this file stops compiling.
type fakeProvider struct {
	name  string
	kind  string
	caps  llm.ModelCapabilities
	calls int
	err   error
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Type() string { return f.kind }

func (f *fakeProvider) Capabilities(context.Context, string) (llm.ModelCapabilities, error) {
	if f.err != nil {
		return llm.ModelCapabilities{}, f.err
	}
	return f.caps, nil
}

func (f *fakeProvider) Generate(_ context.Context, request *llm.GenerateRequest) (llm.Stream, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	return llm.SliceStream(
		llm.NewStartEvent(request.Model),
		llm.NewTextDeltaEvent("hello "),
		llm.NewTextDeltaEvent("world"),
		llm.NewUsageEvent(llm.Usage{InputTokens: 10, OutputTokens: 2}),
		llm.NewFinishEvent(llm.FinishReasonStop, request.Model),
	), nil
}

func (f *fakeProvider) Info() Info {
	return Info{Name: f.name, Type: f.kind, BaseURL: "http://127.0.0.1:11434", Credential: "none"}
}

func (f *fakeProvider) Close() error { return nil }

func TestProviderContract(t *testing.T) {
	var provider Provider = &fakeProvider{
		name: "local-ollama",
		kind: "ollama",
		caps: llm.ModelCapabilities{
			ToolCalling:      true,
			Streaming:        true,
			SystemMessage:    true,
			MaxContextTokens: ConservativeContextTokens,
		},
	}

	var closer Closer = provider.(Closer)
	if err := closer.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	var describer Describer = provider.(Describer)
	info := describer.Info()
	if info.Name != "local-ollama" || info.Type != "ollama" {
		t.Fatalf("Info() = %+v", info)
	}
	if info.Credential != "none" {
		t.Fatalf("credential = %q, want a description, never a secret", info.Credential)
	}

	capabilities, err := provider.Capabilities(context.Background(), "qwen3-coder")
	if err != nil {
		t.Fatalf("Capabilities() = %v", err)
	}
	if err := capabilities.Validate(llm.Requirements{Tools: true}, "qwen3-coder"); err != nil {
		t.Fatalf("the fake provider should satisfy the tool requirement: %v", err)
	}

	request := &llm.GenerateRequest{
		Model:    "qwen3-coder",
		Messages: []llm.Message{llm.NewUserMessage("say hello")},
	}
	stream, err := provider.Generate(context.Background(), request)
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}

	response, err := llm.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect() = %v", err)
	}
	if response.Text() != "hello world" {
		t.Fatalf("text = %q", response.Text())
	}
	if response.Model != "qwen3-coder" {
		t.Fatalf("model = %q", response.Model)
	}
	if response.Usage.InputTokens != 10 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}

func TestProviderRejectsInvalidRequest(t *testing.T) {
	provider := &fakeProvider{name: "fake", kind: "ollama"}

	if _, err := provider.Generate(context.Background(), &llm.GenerateRequest{}); err == nil {
		t.Fatal("a request without a model and messages must be rejected")
	}
	if provider.calls != 1 {
		t.Fatalf("calls = %d", provider.calls)
	}
}

func TestProviderReportsStartupFailure(t *testing.T) {
	cause := errors.New("connection refused")
	provider := &fakeProvider{name: "fake", kind: "ollama", err: cause}

	if _, err := provider.Generate(context.Background(), &llm.GenerateRequest{}); !errors.Is(err, cause) {
		t.Fatalf("error = %v, want the provider cause", err)
	}
	if _, err := provider.Capabilities(context.Background(), "m"); !errors.Is(err, cause) {
		t.Fatalf("error = %v, want the provider cause", err)
	}
}
