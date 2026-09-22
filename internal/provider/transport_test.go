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
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
)

func testProvider(providerType, baseURL string) *config.Provider {
	return &config.Provider{
		Type:    providerType,
		BaseURL: baseURL,
		Timeout: config.Duration(0),
	}
}

func TestDefaultBaseURL(t *testing.T) {
	tests := []struct {
		providerType string
		want         string
	}{
		{providerType: config.ProviderTypeOllama, want: "http://127.0.0.1:11434"},
		{providerType: config.ProviderTypeOpenAIResponses, want: "https://api.openai.com/v1"},
		{providerType: config.ProviderTypeOpenAIChat, want: "https://api.openai.com/v1"},
		{providerType: config.ProviderTypeAnthropic, want: "https://api.anthropic.com"},
		{providerType: config.ProviderTypeOpenAICompat, want: ""},
		{providerType: "made-up", want: ""},
	}

	for _, test := range tests {
		if got := DefaultBaseURL(test.providerType); got != test.want {
			t.Fatalf("DefaultBaseURL(%q) = %q, want %q", test.providerType, got, test.want)
		}
	}
}

func TestAuthStyleFor(t *testing.T) {
	if got := AuthStyleFor(config.ProviderTypeAnthropic); got != AuthStyleAPIKey {
		t.Fatalf("anthropic auth style = %q", got)
	}
	if got := AuthStyleFor(config.ProviderTypeOpenAICompat); got != AuthStyleBearer {
		t.Fatalf("compatible auth style = %q", got)
	}
	if got := AuthStyleFor(config.ProviderTypeOllama); got != AuthStyleBearer {
		t.Fatalf("ollama auth style = %q", got)
	}
}

func TestNewTransportRejectsBadConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *config.Provider
		wantErr string
	}{
		{
			name:    "nil configuration",
			cfg:     nil,
			wantErr: "nil",
		},
		{
			name:    "missing base url",
			cfg:     testProvider(config.ProviderTypeOpenAICompat, ""),
			wantErr: "needs an explicit base_url",
		},
		{
			name:    "unsupported scheme",
			cfg:     testProvider(config.ProviderTypeOllama, "ftp://127.0.0.1:11434"),
			wantErr: "http or https",
		},
		{
			name: "unreadable ca bundle",
			cfg: &config.Provider{
				Type:    config.ProviderTypeOllama,
				BaseURL: "http://127.0.0.1:11434",
				TLS:     config.TLS{CACertFile: filepath.Join(t.TempDir(), "absent.pem")},
			},
			wantErr: "cannot read the CA bundle",
		},
		{
			name: "ca bundle without certificates",
			cfg: &config.Provider{
				Type:    config.ProviderTypeOllama,
				BaseURL: "http://127.0.0.1:11434",
				TLS:     config.TLS{CACertFile: writeTempFile(t, "ca.pem", "not a certificate")},
			},
			wantErr: "no usable certificate",
		},
		{
			name: "client certificate without key",
			cfg: &config.Provider{
				Type:    config.ProviderTypeOllama,
				BaseURL: "http://127.0.0.1:11434",
				TLS:     config.TLS{ClientCertFile: "/tmp/cert.pem"},
			},
			wantErr: "must be set together",
		},
		{
			name: "invalid proxy",
			cfg: &config.Provider{
				Type:    config.ProviderTypeOllama,
				BaseURL: "http://127.0.0.1:11434",
				Proxy:   "://bad",
			},
			wantErr: "is not a valid URL",
		},
		{
			name: "proxy without host",
			cfg: &config.Provider{
				Type:    config.ProviderTypeOllama,
				BaseURL: "http://127.0.0.1:11434",
				Proxy:   "http://",
			},
			wantErr: "missing a host",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewTransport(test.cfg)
			if err == nil {
				t.Fatalf("NewTransport() = nil, want %q", test.wantErr)
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err.Error(), test.wantErr)
			}
		})
	}
}

func TestNewTransportAcceptsValidConfiguration(t *testing.T) {
	transport, err := NewTransport(&config.Provider{
		Type:    config.ProviderTypeOpenAICompat,
		BaseURL: "https://llm.example.com/v1/",
		Headers: map[string]string{"X-Tenant": "acme"},
		Timeout: config.Duration(30_000_000_000), // 30s
	})
	if err != nil {
		t.Fatalf("NewTransport() = %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })

	if transport.BaseURL != "https://llm.example.com/v1" {
		t.Fatalf("base URL = %q, want the trailing slash trimmed", transport.BaseURL)
	}
	if got := transport.Headers.Get("X-Tenant"); got != "acme" {
		t.Fatalf("X-Tenant = %q", got)
	}
	if transport.Timeout.Seconds() != 30 {
		t.Fatalf("timeout = %s", transport.Timeout)
	}
}

func TestTransportEndpointJoin(t *testing.T) {
	tests := []struct {
		base string
		path string
		want string
	}{
		{base: "https://api.deepseek.com/v1", path: "chat/completions", want: "https://api.deepseek.com/v1/chat/completions"},
		{base: "https://api.deepseek.com/v1/", path: "/chat/completions", want: "https://api.deepseek.com/v1/chat/completions"},
		{base: "http://127.0.0.1:11434", path: "models", want: "http://127.0.0.1:11434/models"},
		{base: "http://127.0.0.1:11434", path: "", want: "http://127.0.0.1:11434"},
	}

	for _, test := range tests {
		transport, err := NewTransport(testProvider(config.ProviderTypeOpenAICompat, test.base))
		if err != nil {
			t.Fatalf("NewTransport() = %v", err)
		}
		got, err := transport.endpoint(test.path)
		_ = transport.Close()
		if err != nil {
			t.Fatalf("endpoint(%q) = %v", test.path, err)
		}
		if got != test.want {
			t.Fatalf("endpoint(%q) on %q = %q, want %q", test.path, test.base, got, test.want)
		}
	}
}

func TestTransportEndpointHidesQuery(t *testing.T) {
	transport, err := NewTransport(testProvider(config.ProviderTypeOpenAICompat,
		"https://llm.example.com/v1?token=secret"))
	if err != nil {
		t.Fatalf("NewTransport() = %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })

	if got := transport.Endpoint(); strings.Contains(got, "secret") {
		t.Fatalf("Endpoint() = %q, want the query string removed", got)
	}
}

func TestTransportCredential(t *testing.T) {
	t.Run("inline wins", func(t *testing.T) {
		transport, err := NewTransport(&config.Provider{
			Type:      config.ProviderTypeOpenAICompat,
			BaseURL:   "https://llm.example.com/v1",
			APIKey:    "sk-inline",
			APIKeyEnv: "PAWAGENTS_TEST_UNUSED_KEY",
		})
		if err != nil {
			t.Fatalf("NewTransport() = %v", err)
		}
		t.Cleanup(func() { _ = transport.Close() })

		credential, err := transport.Credential()
		if err != nil || credential != "sk-inline" {
			t.Fatalf("Credential() = %q, %v", credential, err)
		}
		if got := transport.DescribeCredential(); got != "inline" {
			t.Fatalf("DescribeCredential() = %q", got)
		}
	})

	t.Run("environment variable", func(t *testing.T) {
		t.Setenv("PAWAGENTS_TEST_KEY", "sk-from-env")
		transport, err := NewTransport(&config.Provider{
			Type:      config.ProviderTypeOpenAICompat,
			BaseURL:   "https://llm.example.com/v1",
			APIKeyEnv: "PAWAGENTS_TEST_KEY",
		})
		if err != nil {
			t.Fatalf("NewTransport() = %v", err)
		}
		t.Cleanup(func() { _ = transport.Close() })

		credential, err := transport.Credential()
		if err != nil || credential != "sk-from-env" {
			t.Fatalf("Credential() = %q, %v", credential, err)
		}
		if got := transport.DescribeCredential(); got != "env:PAWAGENTS_TEST_KEY" {
			t.Fatalf("DescribeCredential() = %q", got)
		}
	})

	t.Run("missing environment variable", func(t *testing.T) {
		t.Setenv("PAWAGENTS_TEST_ABSENT_KEY", "")
		transport, err := NewTransport(&config.Provider{
			Type:      config.ProviderTypeOpenAICompat,
			BaseURL:   "https://llm.example.com/v1",
			APIKeyEnv: "PAWAGENTS_TEST_ABSENT_KEY",
		})
		if err != nil {
			t.Fatalf("NewTransport() = %v", err)
		}
		t.Cleanup(func() { _ = transport.Close() })

		_, err = transport.Credential()
		if !apperrors.IsKind(err, apperrors.KindAuthentication) {
			t.Fatalf("kind = %q, want an authentication error", apperrors.KindOf(err))
		}
		if !strings.Contains(err.Error(), "PAWAGENTS_TEST_ABSENT_KEY") {
			t.Fatalf("error = %q, want it to name the variable", err.Error())
		}
		if got := transport.DescribeCredential(); got != "env:PAWAGENTS_TEST_ABSENT_KEY" {
			t.Fatalf("DescribeCredential() = %q", got)
		}
	})

	t.Run("required but missing", func(t *testing.T) {
		transport, err := NewTransport(testProvider(config.ProviderTypeAnthropic, "https://api.anthropic.com"))
		if err != nil {
			t.Fatalf("NewTransport() = %v", err)
		}
		t.Cleanup(func() { _ = transport.Close() })

		_, err = transport.Credential()
		if !apperrors.IsKind(err, apperrors.KindAuthentication) {
			t.Fatalf("kind = %q, want an authentication error", apperrors.KindOf(err))
		}
		if got := transport.DescribeCredential(); got != "missing" {
			t.Fatalf("DescribeCredential() = %q", got)
		}
	})

	t.Run("ollama needs no credential", func(t *testing.T) {
		transport, err := NewTransport(testProvider(config.ProviderTypeOllama, ""))
		if err != nil {
			t.Fatalf("NewTransport() = %v", err)
		}
		t.Cleanup(func() { _ = transport.Close() })

		credential, err := transport.Credential()
		if err != nil || credential != "" {
			t.Fatalf("Credential() = %q, %v", credential, err)
		}
		if got := transport.DescribeCredential(); got != "none" {
			t.Fatalf("DescribeCredential() = %q", got)
		}
	})
}

func TestTransportNewRequestHeaders(t *testing.T) {
	t.Setenv("PAWAGENTS_TEST_KEY", "sk-secret")

	transport, err := NewTransport(&config.Provider{
		Type:      config.ProviderTypeOpenAICompat,
		BaseURL:   "https://llm.example.com/v1",
		APIKeyEnv: "PAWAGENTS_TEST_KEY",
		Headers:   map[string]string{"X-Tenant": "acme"},
		Timeout:   config.Duration(5_000_000_000),
	})
	if err != nil {
		t.Fatalf("NewTransport() = %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })

	request, cancel, err := transport.NewRequest(context.Background(), http.MethodPost,
		"chat/completions", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("NewRequest() = %v", err)
	}
	defer cancel()

	if request.URL.String() != "https://llm.example.com/v1/chat/completions" {
		t.Fatalf("url = %q", request.URL)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer sk-secret" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := request.Header.Get("X-Tenant"); got != "acme" {
		t.Fatalf("X-Tenant = %q", got)
	}
	if got := request.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if request.Header.Get("User-Agent") != "pawagents" {
		t.Fatalf("User-Agent = %q", request.Header.Get("User-Agent"))
	}
	if _, ok := request.Context().Deadline(); !ok {
		t.Fatal("the request context should carry the transport timeout")
	}
}

func TestTransportUsesAPIKeyHeaderForAnthropic(t *testing.T) {
	transport, err := NewTransport(&config.Provider{
		Type:    config.ProviderTypeAnthropic,
		BaseURL: "https://api.anthropic.com",
		APIKey:  "sk-ant",
	})
	if err != nil {
		t.Fatalf("NewTransport() = %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })

	request, cancel, err := transport.NewRequest(context.Background(), http.MethodPost,
		"v1/messages", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("NewRequest() = %v", err)
	}
	defer cancel()

	if got := request.Header.Get("x-api-key"); got != "sk-ant" {
		t.Fatalf("x-api-key = %q", got)
	}
	if got := request.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want it unset for anthropic", got)
	}
}

func TestTransportNewRequestFailsOnMissingCredential(t *testing.T) {
	transport, err := NewTransport(testProvider(config.ProviderTypeAnthropic, "https://api.anthropic.com"))
	if err != nil {
		t.Fatalf("NewTransport() = %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })

	if _, cancel, err := transport.NewRequest(context.Background(), http.MethodPost,
		"v1/messages", "application/json", nil); err == nil {
		cancel()
		t.Fatal("a request without a credential must fail before it is sent")
	}
}

func writeTempFile(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("cannot write %s: %v", name, err)
	}
	return path
}
