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
	"log/slog"
	"strings"
	"testing"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/llm"
)

// stubProvider records the options it was built with.
type stubProvider struct {
	options Options
	closed  bool
}

func (s *stubProvider) Name() string { return s.options.Name }

func (s *stubProvider) Type() string {
	if s.options.Config == nil {
		return ""
	}
	return s.options.Config.Type
}

func (s *stubProvider) Capabilities(context.Context, string) (llm.ModelCapabilities, error) {
	return llm.ModelCapabilities{Streaming: true}, nil
}

func (s *stubProvider) Generate(context.Context, *llm.GenerateRequest) (llm.Stream, error) {
	return llm.SliceStream(llm.NewFinishEvent(llm.FinishReasonStop, "stub")), nil
}

func (s *stubProvider) Close() error {
	s.closed = true
	return nil
}

func registryConfig(t *testing.T, providers map[string]*config.Provider) *config.Config {
	t.Helper()
	cfg := &config.Config{
		Version:   config.CurrentVersion,
		Providers: providers,
		Models:    map[string]*config.Model{},
		Agents:    map[string]*config.Agent{},
	}
	config.ApplyDefaults(cfg)
	cfg.Normalize()
	return cfg
}

func TestRegistryBuildsAndCachesProviders(t *testing.T) {
	cfg := registryConfig(t, map[string]*config.Provider{
		"local": {Type: config.ProviderTypeOllama, BaseURL: "http://127.0.0.1:11434"},
	})

	registry := NewRegistry(cfg, slog.Default())
	built := 0
	registry.Register(config.ProviderTypeOllama, func(_ context.Context, options Options) (Provider, error) {
		built++
		if options.Transport == nil {
			return nil, errors.New("the transport must be prepared by the registry")
		}
		return &stubProvider{options: options}, nil
	})

	first, err := registry.Provider(context.Background(), "local")
	if err != nil {
		t.Fatalf("Provider() = %v", err)
	}
	second, err := registry.Provider(context.Background(), "local")
	if err != nil {
		t.Fatalf("Provider() = %v", err)
	}
	if first != second {
		t.Fatal("the same instance must be returned for the same provider name")
	}
	if built != 1 {
		t.Fatalf("the factory ran %d times, want 1", built)
	}
	if first.Name() != "local" {
		t.Fatalf("name = %q", first.Name())
	}
	if first.Type() != config.ProviderTypeOllama {
		t.Fatalf("type = %q", first.Type())
	}

	if registry.Config() == nil {
		t.Fatal("Config() must return the configuration the registry was built from")
	}
	if registry.Config().Providers["local"] == nil {
		t.Fatal("the configured provider must be reachable through Config()")
	}
	if registry.Logger() == nil {
		t.Fatal("Logger() must never return nil")
	}

	if err := registry.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if !first.(*stubProvider).closed {
		t.Fatal("Close() must close every instantiated provider")
	}
}

func TestRegistryProviderErrors(t *testing.T) {
	tests := []struct {
		name       string
		providers  map[string]*config.Provider
		request    string
		wantKind   apperrors.Kind
		wantSubstr string
	}{
		{
			name:       "unknown provider",
			providers:  map[string]*config.Provider{"local": {Type: config.ProviderTypeOllama}},
			request:    "absent",
			wantKind:   apperrors.KindConfig,
			wantSubstr: "not defined in the configuration",
		},
		{
			name: "disabled provider",
			providers: map[string]*config.Provider{
				"local": {Type: config.ProviderTypeOllama, Disabled: true},
			},
			request:    "local",
			wantKind:   apperrors.KindConfig,
			wantSubstr: "is disabled",
		},
		{
			name: "planned provider type",
			providers: map[string]*config.Provider{
				"gem": {Type: config.ProviderTypeGemini},
			},
			request:    "gem",
			wantKind:   apperrors.KindCapability,
			wantSubstr: "not implemented yet",
		},
		{
			name: "provider type without an implementation in this build",
			providers: map[string]*config.Provider{
				"cloud": {Type: config.ProviderTypeAnthropic, BaseURL: "https://api.anthropic.com"},
			},
			request:    "cloud",
			wantKind:   apperrors.KindCapability,
			wantSubstr: "which this build does not implement",
		},
		{
			name: "invalid provider configuration",
			providers: map[string]*config.Provider{
				"local": {Type: config.ProviderTypeOllama, BaseURL: "ftp://127.0.0.1"},
			},
			request:    "local",
			wantKind:   apperrors.KindConfig,
			wantSubstr: "http or https",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry(registryConfig(t, test.providers), slog.Default())
			registry.Register(config.ProviderTypeOllama, func(_ context.Context, options Options) (Provider, error) {
				return &stubProvider{options: options}, nil
			})

			_, err := registry.Provider(context.Background(), test.request)
			if err == nil {
				t.Fatalf("Provider() = nil, want %q", test.wantSubstr)
			}
			if !apperrors.IsKind(err, test.wantKind) {
				t.Fatalf("kind = %q, want %q (%v)", apperrors.KindOf(err), test.wantKind, err)
			}
			if !strings.Contains(err.Error(), test.wantSubstr) {
				t.Fatalf("error = %q, want it to contain %q", err.Error(), test.wantSubstr)
			}
		})
	}
}

func TestRegistryWrapsFactoryFailure(t *testing.T) {
	cfg := registryConfig(t, map[string]*config.Provider{
		"local": {Type: config.ProviderTypeOllama},
	})
	registry := NewRegistry(cfg, slog.Default())
	registry.Register(config.ProviderTypeOllama, func(context.Context, Options) (Provider, error) {
		return nil, errors.New("cannot initialise")
	})

	_, err := registry.Provider(context.Background(), "local")
	if !apperrors.IsKind(err, apperrors.KindProvider) {
		t.Fatalf("kind = %q, want a provider error", apperrors.KindOf(err))
	}

	// A classified factory error is passed through unchanged.
	classified := apperrors.New(apperrors.KindAuthentication, "provider.auth", "bad key")
	registry = NewRegistry(cfg, slog.Default())
	registry.Register(config.ProviderTypeOllama, func(context.Context, Options) (Provider, error) {
		return nil, classified
	})
	if _, err := registry.Provider(context.Background(), "local"); err != classified {
		t.Fatalf("error = %v, want the classified error unchanged", err)
	}
}

func TestRegistryRejectsFactoryReturningNil(t *testing.T) {
	cfg := registryConfig(t, map[string]*config.Provider{
		"local": {Type: config.ProviderTypeOllama},
	})
	registry := NewRegistry(cfg, slog.Default())
	registry.Register(config.ProviderTypeOllama, func(context.Context, Options) (Provider, error) {
		return nil, nil
	})

	if _, err := registry.Provider(context.Background(), "local"); err == nil {
		t.Fatal("a factory that returns no provider must be reported")
	} else if !apperrors.IsKind(err, apperrors.KindInternal) {
		t.Fatalf("kind = %q", apperrors.KindOf(err))
	}
}

func TestRegistryWithoutConfiguration(t *testing.T) {
	registry := NewRegistry(nil, nil)
	if registry.Logger() == nil {
		t.Fatal("Logger() must fall back to the slog default")
	}
	if _, err := registry.Provider(context.Background(), "any"); err == nil {
		t.Fatal("a registry without a configuration must fail")
	}
	if names := registry.Names(); names != nil {
		t.Fatalf("Names() = %v, want nil", names)
	}
	if infos := registry.Infos(); infos != nil {
		t.Fatalf("Infos() = %v, want nil", infos)
	}
}

func TestRegistryInfosAndNames(t *testing.T) {
	cfg := registryConfig(t, map[string]*config.Provider{
		"local":   {Type: config.ProviderTypeOllama, BaseURL: "http://127.0.0.1:11434"},
		"gateway": {Type: config.ProviderTypeOpenAICompat, BaseURL: "https://llm.example.com/v1", APIKeyEnv: "PAWAGENTS_TEST_KEY"},
		"off":     {Type: config.ProviderTypeOllama, Disabled: true},
		"broken":  {Type: config.ProviderTypeOllama, BaseURL: "ftp://x"},
	})
	registry := NewRegistry(cfg, slog.Default())

	if names := registry.Names(); strings.Join(names, ",") != "broken,gateway,local,off" {
		t.Fatalf("Names() = %v", names)
	}

	infos := registry.Infos()
	if len(infos) != 4 {
		t.Fatalf("Infos() = %+v", infos)
	}

	byName := map[string]Info{}
	for _, info := range infos {
		byName[info.Name] = info
	}

	if byName["local"].BaseURL != "http://127.0.0.1:11434" {
		t.Fatalf("local base url = %q", byName["local"].BaseURL)
	}
	if byName["local"].Credential != "none" {
		t.Fatalf("local credential = %q", byName["local"].Credential)
	}
	if byName["gateway"].Credential != "env:PAWAGENTS_TEST_KEY" {
		t.Fatalf("gateway credential = %q, want a description, never a secret", byName["gateway"].Credential)
	}
	if !byName["off"].Disabled {
		t.Fatal("a disabled provider must be reported as disabled")
	}
	// A provider whose transport cannot be built still reports its raw value
	// instead of disappearing from the listing.
	if byName["broken"].BaseURL != "ftp://x" {
		t.Fatalf("broken base url = %q", byName["broken"].BaseURL)
	}
}

func TestRegistryRegistersDefaultFactories(t *testing.T) {
	Register("test-only-type", func(context.Context, Options) (Provider, error) {
		return &stubProvider{}, nil
	})
	t.Cleanup(func() {
		registeredMu.Lock()
		delete(registeredFactories, "test-only-type")
		registeredMu.Unlock()
	})

	factories := DefaultFactories()
	if _, ok := factories["test-only-type"]; !ok {
		t.Fatal("DefaultFactories must include a freshly registered factory")
	}

	// A returned map is a copy: mutating it must not affect the registry.
	delete(factories, "test-only-type")
	if _, ok := DefaultFactories()["test-only-type"]; !ok {
		t.Fatal("DefaultFactories must return a copy")
	}

	// Every registered factory with a duplicate type is a programming error.
	defer func() {
		if recover() == nil {
			t.Fatal("registering the same type twice must panic")
		}
	}()
	Register("test-only-type", func(context.Context, Options) (Provider, error) {
		return &stubProvider{}, nil
	})
}

func TestRegisterRejectsIncompleteInput(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Register without a factory must panic")
		}
	}()
	Register("incomplete", nil)
}

func TestRegistrySupportedTypes(t *testing.T) {
	registry := NewRegistry(nil, slog.Default())
	registry.Register("b", func(context.Context, Options) (Provider, error) { return nil, nil })
	registry.Register("a", func(context.Context, Options) (Provider, error) { return nil, nil })

	if got := strings.Join(registry.SupportedTypes(), ","); got != "a,b" {
		t.Fatalf("SupportedTypes() = %q", got)
	}
}

func TestRegistryCloseAggregatesErrors(t *testing.T) {
	cfg := registryConfig(t, map[string]*config.Provider{
		"one": {Type: config.ProviderTypeOllama},
		"two": {Type: config.ProviderTypeOllama},
	})
	registry := NewRegistry(cfg, slog.Default())
	registry.Register(config.ProviderTypeOllama, func(_ context.Context, options Options) (Provider, error) {
		return &failingCloserProvider{stubProvider: stubProvider{options: options}}, nil
	})

	for _, name := range []string{"one", "two"} {
		if _, err := registry.Provider(context.Background(), name); err != nil {
			t.Fatalf("Provider(%q) = %v", name, err)
		}
	}

	err := registry.Close()
	if err == nil {
		t.Fatal("Close() must report the failures")
	}
	if !strings.Contains(err.Error(), "one") || !strings.Contains(err.Error(), "two") {
		t.Fatalf("error = %q, want every failing provider named", err.Error())
	}
	if err := registry.Close(); err != nil {
		t.Fatalf("a second Close() must succeed: %v", err)
	}
}

type failingCloserProvider struct {
	stubProvider
}

func (f *failingCloserProvider) Close() error { return errors.New("cannot close") }
