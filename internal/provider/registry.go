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
	"log/slog"
	"sort"
	"sync"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
)

// Factory builds a provider implementation from its configuration.
//
// A factory must not perform network calls: it prepares a client, nothing
// more. Connectivity problems are reported by Capabilities or Generate, which
// keeps `pagent provider list` fast and offline-safe.
type Factory func(ctx context.Context, options Options) (Provider, error)

// Options is what a factory receives.
type Options struct {
	// Name is the configured provider name, for example "local-ollama".
	Name string
	// Config is the provider configuration block.
	Config *config.Provider
	// Transport is the prepared HTTP transport. Providers that do not use
	// HTTP may ignore it.
	Transport *Transport
	// Logger receives provider diagnostics.
	Logger *slog.Logger
}

// registeredFactories holds the provider types compiled into this build. Each
// provider package registers itself from an init function, which keeps the
// registry free of imports that would point back at the provider
// implementations.
var (
	registeredMu        sync.RWMutex
	registeredFactories = map[string]Factory{}
)

// Register makes a provider type available to every registry created later. It
// is intended to be called from an init function and panics on a duplicate
// registration, because that is a programming error, not a runtime condition.
func Register(providerType string, factory Factory) {
	if providerType == "" || factory == nil {
		panic("provider: Register requires a type and a factory")
	}
	registeredMu.Lock()
	defer registeredMu.Unlock()
	if _, exists := registeredFactories[providerType]; exists {
		panic("provider: factory for " + providerType + " is already registered")
	}
	registeredFactories[providerType] = factory
}

// DefaultFactories returns a copy of the registered factories.
func DefaultFactories() map[string]Factory {
	registeredMu.RLock()
	defer registeredMu.RUnlock()

	out := make(map[string]Factory, len(registeredFactories))
	for name, factory := range registeredFactories {
		out[name] = factory
	}
	return out
}

// Registry instantiates and caches providers for one configuration.
//
// Instances are created lazily: a configuration may name ten providers while a
// single task uses one, and building a client for an unused endpoint only
// creates work and failure modes.
type Registry struct {
	mu        sync.Mutex
	cfg       *config.Config
	logger    *slog.Logger
	factories map[string]Factory
	instances map[string]Provider
}

// NewRegistry builds a registry without any provider implementation. It is
// used by tests and by callers that inject their own factories.
func NewRegistry(cfg *config.Config, logger *slog.Logger) *Registry {
	return &Registry{
		cfg:       cfg,
		logger:    logger,
		factories: map[string]Factory{},
		instances: map[string]Provider{},
	}
}

// NewDefaultRegistry builds a registry preloaded with every provider
// implementation compiled into the binary.
func NewDefaultRegistry(cfg *config.Config, logger *slog.Logger) *Registry {
	registry := NewRegistry(cfg, logger)
	registry.factories = DefaultFactories()
	return registry
}

// Register adds a factory to this registry only.
func (r *Registry) Register(providerType string, factory Factory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.factories == nil {
		r.factories = map[string]Factory{}
	}
	r.factories[providerType] = factory
}

// Config returns the configuration the registry was built from.
func (r *Registry) Config() *config.Config { return r.cfg }

// Logger returns the logger passed to provider factories.
func (r *Registry) Logger() *slog.Logger {
	if r.logger == nil {
		return slog.Default()
	}
	return r.logger
}

// Provider returns the provider with the given configured name, building it on
// first use.
func (r *Registry) Provider(ctx context.Context, name string) (Provider, error) {
	if r.cfg == nil {
		return nil, apperrors.New(apperrors.KindInternal, "provider.registry",
			"the registry has no configuration")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if instance, ok := r.instances[name]; ok {
		return instance, nil
	}

	cfgProvider, err := r.cfg.Provider(name)
	if err != nil {
		return nil, apperrors.New(apperrors.KindConfig, "provider.registry",
			"provider %q is not defined in the configuration", name)
	}
	if cfgProvider.Disabled {
		return nil, apperrors.New(apperrors.KindConfig, "provider.registry",
			"provider %q is disabled", name)
	}

	instance, err := r.build(ctx, name, cfgProvider)
	if err != nil {
		return nil, err
	}
	r.instances[name] = instance
	return instance, nil
}

// build instantiates one provider. The caller holds the registry lock.
func (r *Registry) build(ctx context.Context, name string, cfgProvider *config.Provider) (Provider, error) {
	factory, ok := r.factories[cfgProvider.Type]
	if !ok {
		if config.IsPlannedProviderType(cfgProvider.Type) {
			return nil, apperrors.New(apperrors.KindCapability, "provider.registry",
				"provider %q uses type %q, which is on the roadmap and not implemented yet",
				name, cfgProvider.Type)
		}
		return nil, apperrors.New(apperrors.KindCapability, "provider.registry",
			"provider %q uses type %q, which this build does not implement",
			name, cfgProvider.Type)
	}

	transport, err := NewTransport(cfgProvider)
	if err != nil {
		return nil, err
	}

	options := Options{
		Name:      name,
		Config:    cfgProvider,
		Transport: transport,
		Logger:    r.Logger(),
	}

	instance, err := factory(ctx, options)
	if err != nil {
		_ = transport.Close()
		if apperrors.KindOf(err) == apperrors.KindInternal {
			return nil, apperrors.Wrap(apperrors.KindProvider, "provider.registry",
				"cannot initialise provider %q", err, name)
		}
		return nil, err
	}
	if instance == nil {
		_ = transport.Close()
		return nil, apperrors.New(apperrors.KindInternal, "provider.registry",
			"the factory for provider %q returned no provider", name)
	}

	r.Logger().Debug("provider initialised",
		slog.String("provider", name),
		slog.String("type", cfgProvider.Type),
		slog.String("endpoint", transport.Endpoint()))

	return instance, nil
}

// Names returns the configured provider names, sorted, including disabled ones.
func (r *Registry) Names() []string {
	if r.cfg == nil {
		return nil
	}
	return r.cfg.ProviderNames()
}

// Infos describes every configured provider for listings and diagnostics. It
// never opens a connection and never includes a credential.
func (r *Registry) Infos() []Info {
	if r.cfg == nil {
		return nil
	}
	names := r.cfg.ProviderNames()
	infos := make([]Info, 0, len(names))

	for _, name := range names {
		cfgProvider := r.cfg.Providers[name]
		if cfgProvider == nil {
			infos = append(infos, Info{Name: name})
			continue
		}

		info := Info{
			Name:     name,
			Type:     cfgProvider.Type,
			Disabled: cfgProvider.Disabled,
		}

		if transport, err := NewTransport(cfgProvider); err == nil {
			info.BaseURL = transport.Endpoint()
			info.Credential = transport.DescribeCredential()
			_ = transport.Close()
		} else {
			info.BaseURL = cfgProvider.BaseURL
		}

		infos = append(infos, info)
	}

	return infos
}

// SupportedTypes returns the provider types this build implements, sorted.
func (r *Registry) SupportedTypes() []string {
	types := make([]string, 0, len(r.factories))
	for providerType := range r.factories {
		types = append(types, providerType)
	}
	sort.Strings(types)
	return types
}

// Close releases every instantiated provider.
func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	var problems []error
	for name, instance := range r.instances {
		closer, ok := instance.(Closer)
		if !ok {
			continue
		}
		if err := closer.Close(); err != nil {
			problems = append(problems, apperrors.Wrap(apperrors.KindProvider, "provider.registry",
				"cannot close provider %q", err, name))
		}
	}
	r.instances = map[string]Provider{}

	return apperrors.Multi(apperrors.KindProvider, "provider.registry",
		"cannot close every provider", problems...)
}
