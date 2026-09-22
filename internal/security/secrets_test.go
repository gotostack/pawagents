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

package security

import (
	"strings"
	"testing"
)

func TestIsSensitiveKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{key: "GITHUB_TOKEN", want: true},
		{key: "OPENAI_API_KEY", want: true},
		{key: "DEEPSEEK_API_KEY", want: true},
		{key: "DB_PASSWORD", want: true},
		{key: "APP_SECRET", want: true},
		{key: "api_key", want: true},
		{key: "Authorization", want: true},
		{key: "Proxy-Authorization", want: true},
		{key: "Set-Cookie", want: true},
		{key: "access_token", want: true},
		{key: "session_key", want: true},
		{key: "HOME", want: false},
		{key: "PATH", want: false},
		{key: "GOPATH", want: false},
		{key: "workspace", want: false},
		{key: "", want: false},
		{key: "keyboard", want: false},
	}

	for _, test := range tests {
		t.Run(test.key, func(t *testing.T) {
			if got := IsSensitiveKey(test.key, nil); got != test.want {
				t.Fatalf("IsSensitiveKey(%q) = %v, want %v", test.key, got, test.want)
			}
		})
	}
}

func TestIsSensitiveKeyHonoursCustomPatterns(t *testing.T) {
	patterns := []string{"CORP_*"}
	if !IsSensitiveKey("CORP_CREDENTIAL", patterns) {
		t.Fatal("custom pattern should match")
	}
	if IsSensitiveKey("GITHUB_TOKEN", patterns) {
		t.Fatal("custom patterns replace the defaults, so the default pattern must not match")
	}
}

func TestRedactStringMap(t *testing.T) {
	if got := RedactStringMap(nil, nil); got != nil {
		t.Fatalf("RedactStringMap(nil) = %v, want nil", got)
	}

	in := map[string]string{
		"Authorization": "Bearer abc123",
		"X-Trace-Id":    "trace-1",
	}
	out := RedactStringMap(in, nil)

	if out["Authorization"] != RedactedPlaceholder {
		t.Fatalf("Authorization = %q, want the placeholder", out["Authorization"])
	}
	if out["X-Trace-Id"] != "trace-1" {
		t.Fatalf("X-Trace-Id = %q, want it preserved", out["X-Trace-Id"])
	}
	if in["Authorization"] != "Bearer abc123" {
		t.Fatal("RedactStringMap must not mutate the input")
	}
}

func TestRedactAnyMapIsRecursive(t *testing.T) {
	in := map[string]any{
		"model": "qwen3-coder",
		"options": map[string]any{
			"api_key": "sk-secret",
			"region":  "us-east-1",
		},
	}
	out := RedactAnyMap(in, nil)

	nested, ok := out["options"].(map[string]any)
	if !ok {
		t.Fatalf("options = %#v, want a map", out["options"])
	}
	if nested["api_key"] != RedactedPlaceholder {
		t.Fatalf("nested api_key = %q, want the placeholder", nested["api_key"])
	}
	if nested["region"] != "us-east-1" {
		t.Fatalf("nested region = %q, want it preserved", nested["region"])
	}
	if out["model"] != "qwen3-coder" {
		t.Fatalf("model = %q, want it preserved", out["model"])
	}
}

func TestIsEnvSecret(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "AWS_ACCESS_KEY_ID", want: true},
		{name: "AWS_SECRET_ACCESS_KEY", want: true},
		{name: "GOOGLE_APPLICATION_CREDENTIALS", want: true},
		{name: "MY_PASSWORD", want: true},
		{name: "SSH_KEY", want: true},
		{name: "ANTHROPIC_API_KEY", want: true},
		{name: "HOME", want: false},
		{name: "PWD", want: false},
		{name: "LANG", want: false},
		{name: "PAWAGENTS_HOME", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsEnvSecret(test.name, nil); got != test.want {
				t.Fatalf("IsEnvSecret(%q) = %v, want %v", test.name, got, test.want)
			}
		})
	}
}

func TestRedactValueLeavesOrdinaryKeys(t *testing.T) {
	if got := RedactValue("model", "qwen3-coder", nil); got != "qwen3-coder" {
		t.Fatalf("RedactValue = %q", got)
	}
	if got := RedactValue("OPENAI_API_KEY", "sk-1", nil); got != RedactedPlaceholder {
		t.Fatalf("RedactValue = %q", got)
	}
}

func TestDefaultSecretPatternsCoverCommonCases(t *testing.T) {
	for _, suffix := range []string{"_TOKEN", "_KEY", "_SECRET", "_PASSWORD"} {
		name := "SOMETHING" + suffix
		if !IsSensitiveKey(name, DefaultSecretPatterns()) {
			t.Fatalf("%s should be covered by the default patterns", name)
		}
	}
	if len(DefaultSecretPatterns()) == 0 {
		t.Fatal("DefaultSecretPatterns must not be empty")
	}
	if !strings.Contains(RedactedPlaceholder, "REDACTED") {
		t.Fatalf("RedactedPlaceholder = %q", RedactedPlaceholder)
	}
}
