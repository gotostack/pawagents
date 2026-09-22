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
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
)

// DefaultBaseURL returns the endpoint used when the configuration omits
// base_url. An empty result means the provider type cannot guess one and the
// configuration validator requires the field.
func DefaultBaseURL(providerType string) string {
	switch providerType {
	case config.ProviderTypeOpenAIResponses, config.ProviderTypeOpenAIChat:
		return "https://api.openai.com/v1"
	case config.ProviderTypeAnthropic:
		return "https://api.anthropic.com"
	case config.ProviderTypeOllama:
		return "http://127.0.0.1:11434"
	default:
		return ""
	}
}

// Auth style identifiers returned by AuthStyleFor.
const (
	// AuthStyleBearer sends "Authorization: Bearer <credential>".
	AuthStyleBearer = "bearer"
	// AuthStyleAPIKey sends the credential in x-api-key, which is what
	// Anthropic uses.
	AuthStyleAPIKey = "api-key"
	// AuthStyleNone sends no credential.
	AuthStyleNone = "none"
)

// AuthStyleFor returns how a provider type attaches its credential.
func AuthStyleFor(providerType string) string {
	switch providerType {
	case config.ProviderTypeAnthropic:
		return AuthStyleAPIKey
	case config.ProviderTypeOllama:
		// A plain local Ollama server needs no credential, but a reverse
		// proxy in front of it usually does, so send one when configured.
		return AuthStyleBearer
	default:
		return AuthStyleBearer
	}
}

// Transport is the HTTP plumbing shared by every HTTP based provider: the
// endpoint, the credential, the headers, the proxy, the TLS settings and the
// per-request timeout.
//
// Providers build their own request bodies on top of it. The timeout is
// enforced with a context deadline per request rather than with
// http.Client.Timeout, because a streamed response must stay open while it
// produces tokens.
type Transport struct {
	// BaseURL is the endpoint root without a trailing slash.
	BaseURL string
	// Timeout bounds a single request.
	Timeout time.Duration
	// Headers are extra headers sent with every request.
	Headers http.Header
	// ExtraBody is merged into every JSON request body.
	ExtraBody map[string]any
	// Client is the HTTP client to use.
	Client *http.Client
	// AuthHeader and AuthPrefix control how the credential is attached.
	AuthHeader string
	AuthPrefix string

	apiKey             string
	apiKeyEnv          string
	requiresCredential bool

	transport *http.Transport
}

// NewTransport builds the transport described by a provider configuration
// block. It never performs a network call, so a misconfigured credential is
// reported when the first request is made, not when the provider is created.
func NewTransport(cfg *config.Provider) (*Transport, error) {
	if cfg == nil {
		return nil, apperrors.New(apperrors.KindConfig, "provider.transport",
			"the provider configuration is nil")
	}

	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		baseURL = DefaultBaseURL(cfg.Type)
	}
	if baseURL == "" {
		return nil, apperrors.New(apperrors.KindConfig, "provider.transport",
			"provider type %q needs an explicit base_url", cfg.Type)
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, apperrors.Wrap(apperrors.KindConfig, "provider.transport",
			"base_url %q is not a valid URL", err, baseURL)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, apperrors.New(apperrors.KindConfig, "provider.transport",
			"base_url %q must use the http or https scheme", baseURL)
	}

	tlsConfig, err := buildTLSConfig(cfg)
	if err != nil {
		return nil, err
	}

	proxy, err := buildProxyFunc(cfg.Proxy)
	if err != nil {
		return nil, err
	}

	httpTransport := &http.Transport{
		Proxy:                 proxy,
		TLSClientConfig:       tlsConfig,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	if timeout := cfg.Timeout.Duration(); timeout > 0 {
		// Fail fast when the server accepts the connection and then stalls
		// before sending response headers. The body itself may stream for as
		// long as the request deadline allows.
		httpTransport.ResponseHeaderTimeout = timeout
	}

	headers := make(http.Header, len(cfg.Headers))
	for name, value := range cfg.Headers {
		headers.Set(name, value)
	}

	style := AuthStyleFor(cfg.Type)
	authHeader, authPrefix := "", ""
	switch style {
	case AuthStyleBearer:
		authHeader, authPrefix = "Authorization", "Bearer"
	case AuthStyleAPIKey:
		authHeader = "x-api-key"
	case AuthStyleNone:
	}

	return &Transport{
		BaseURL:            strings.TrimRight(baseURL, "/"),
		Timeout:            cfg.Timeout.Duration(),
		Headers:            headers,
		ExtraBody:          cfg.ExtraBody,
		Client:             &http.Client{Transport: httpTransport},
		AuthHeader:         authHeader,
		AuthPrefix:         authPrefix,
		apiKey:             cfg.APIKey,
		apiKeyEnv:          cfg.APIKeyEnv,
		requiresCredential: config.ProviderNeedsCredential(cfg.Type),
		transport:          httpTransport,
	}, nil
}

// Credential resolves the API key. The environment variable is read on every
// call so that a rotated secret is picked up without restarting a long running
// MCP server.
func (t *Transport) Credential() (string, error) {
	if t == nil {
		return "", apperrors.New(apperrors.KindInternal, "provider.credentials",
			"the transport is nil")
	}
	if t.apiKey != "" {
		return t.apiKey, nil
	}
	if t.apiKeyEnv != "" {
		value := strings.TrimSpace(os.Getenv(t.apiKeyEnv))
		if value == "" {
			return "", apperrors.New(apperrors.KindAuthentication, "provider.credentials",
				"environment variable %s is not set", t.apiKeyEnv)
		}
		return value, nil
	}
	if t.requiresCredential {
		return "", apperrors.New(apperrors.KindAuthentication, "provider.credentials",
			"no credential configured; set api_key_env or api_key")
	}
	return "", nil
}

// DescribeCredential explains how the credential is configured, without
// revealing it. It is used by `pagent provider list` and by diagnostics.
func (t *Transport) DescribeCredential() string {
	if t == nil {
		return "none"
	}
	switch {
	case t.apiKey != "":
		return "inline"
	case t.apiKeyEnv != "":
		return "env:" + t.apiKeyEnv
	case t.requiresCredential:
		return "missing"
	default:
		return "none"
	}
}

// NewRequest builds a request against the provider endpoint.
//
// The caller receives a context that carries the per-request deadline, so it
// must call CancelableRequest and defer the returned cancel function. The
// timeout is applied here instead of inside Generate so that every provider
// gets the same behaviour.
func (t *Transport) NewRequest(ctx context.Context, method, path, contentType string, body io.Reader) (*http.Request, context.CancelFunc, error) {
	if t == nil || t.Client == nil {
		return nil, nil, apperrors.New(apperrors.KindInternal, "provider.request",
			"the transport is not initialised")
	}

	requestCtx := ctx
	cancel := func() {}
	if t.Timeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, t.Timeout)
	}

	endpoint, err := t.endpoint(path)
	if err != nil {
		cancel()
		return nil, nil, err
	}

	request, err := http.NewRequestWithContext(requestCtx, method, endpoint, body)
	if err != nil {
		cancel()
		return nil, nil, apperrors.Wrap(apperrors.KindInternal, "provider.request",
			"cannot build the request for %s", err, endpoint)
	}

	for name, values := range t.Headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	request.Header.Set("Accept", "application/json")
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	request.Header.Set("User-Agent", "pawagents")

	if t.AuthHeader != "" {
		credential, err := t.Credential()
		if err != nil {
			cancel()
			return nil, nil, err
		}
		if credential != "" {
			value := credential
			if t.AuthPrefix != "" {
				value = t.AuthPrefix + " " + credential
			}
			request.Header.Set(t.AuthHeader, value)
		}
	}

	return request, cancel, nil
}

// endpoint joins the base URL with a relative API path.
//
// Plain concatenation is used rather than URL resolution because a base URL
// such as "https://api.deepseek.com/v1" has no trailing slash, and resolving a
// relative path against it would drop the "/v1" prefix.
func (t *Transport) endpoint(path string) (string, error) {
	base := strings.TrimRight(t.BaseURL, "/")
	cleaned := strings.Trim(path, "/")
	if cleaned == "" {
		return base, nil
	}

	full := base + "/" + cleaned
	if _, err := url.Parse(full); err != nil {
		return "", apperrors.Wrap(apperrors.KindConfig, "provider.request",
			"cannot build the endpoint URL from %q", err, t.BaseURL)
	}
	return full, nil
}

// Close releases idle connections.
func (t *Transport) Close() error {
	if t == nil || t.transport == nil {
		return nil
	}
	t.transport.CloseIdleConnections()
	return nil
}

// buildTLSConfig turns the configuration into a TLS configuration.
func buildTLSConfig(cfg *config.Provider) (*tls.Config, error) {
	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: cfg.TLS.InsecureSkipVerify,
		ServerName:         cfg.TLS.ServerName,
	}

	if cfg.TLS.CACertFile != "" {
		pem, err := os.ReadFile(cfg.TLS.CACertFile)
		if err != nil {
			return nil, apperrors.Wrap(apperrors.KindConfig, "provider.tls",
				"cannot read the CA bundle %s", err, cfg.TLS.CACertFile)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, apperrors.New(apperrors.KindConfig, "provider.tls",
				"the CA bundle %s contains no usable certificate", cfg.TLS.CACertFile)
		}
		tlsConfig.RootCAs = pool
	}

	if cfg.TLS.ClientCertFile != "" || cfg.TLS.ClientKeyFile != "" {
		if cfg.TLS.ClientCertFile == "" || cfg.TLS.ClientKeyFile == "" {
			return nil, apperrors.New(apperrors.KindConfig, "provider.tls",
				"tls.client_cert_file and tls.client_key_file must be set together")
		}
		certificate, err := tls.LoadX509KeyPair(cfg.TLS.ClientCertFile, cfg.TLS.ClientKeyFile)
		if err != nil {
			return nil, apperrors.Wrap(apperrors.KindConfig, "provider.tls",
				"cannot load the client certificate", err)
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}

	return tlsConfig, nil
}

// buildProxyFunc returns the proxy selector for the transport. An empty proxy
// keeps the default behaviour of honouring HTTP_PROXY and friends.
func buildProxyFunc(proxy string) (func(*http.Request) (*url.URL, error), error) {
	if strings.TrimSpace(proxy) == "" {
		return http.ProxyFromEnvironment, nil
	}
	parsed, err := url.Parse(proxy)
	if err != nil {
		return nil, apperrors.Wrap(apperrors.KindConfig, "provider.proxy",
			"proxy %q is not a valid URL", err, proxy)
	}
	if parsed.Host == "" {
		return nil, apperrors.New(apperrors.KindConfig, "provider.proxy",
			"proxy %q is missing a host", proxy)
	}
	return http.ProxyURL(parsed), nil
}

// Endpoint renders the endpoint for logs and diagnostics. A query string is
// dropped because some gateways carry a token there.
func (t *Transport) Endpoint() string {
	if t == nil {
		return ""
	}
	parsed, err := url.Parse(t.BaseURL)
	if err != nil {
		return t.BaseURL
	}
	// Keep the path, drop any query string that might carry a token.
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return fmt.Sprintf("%s://%s%s", parsed.Scheme, parsed.Host, parsed.Path)
}
