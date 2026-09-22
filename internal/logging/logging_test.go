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

package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		in      string
		want    slog.Level
		wantErr bool
	}{
		{in: "", want: slog.LevelInfo},
		{in: "debug", want: slog.LevelDebug},
		{in: "DEBUG", want: slog.LevelDebug},
		{in: " info ", want: slog.LevelInfo},
		{in: "warn", want: slog.LevelWarn},
		{in: "warning", want: slog.LevelWarn},
		{in: "error", want: slog.LevelError},
		{in: "off", want: slog.Level(127)},
		{in: "loud", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.in, func(t *testing.T) {
			got, err := ParseLevel(test.in)
			if test.wantErr {
				if err == nil {
					t.Fatalf("ParseLevel(%q) = %v, want an error", test.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseLevel(%q) returned %v", test.in, err)
			}
			if got != test.want {
				t.Fatalf("ParseLevel(%q) = %v, want %v", test.in, got, test.want)
			}
		})
	}
}

func TestNewRejectsUnknownFormat(t *testing.T) {
	if _, err := New(Options{Format: "xml"}); err == nil {
		t.Fatal("New should reject an unknown format")
	}
}

func TestLoggerRedactsSensitiveAttributes(t *testing.T) {
	var buf bytes.Buffer
	logger, err := New(Options{Level: "debug", Format: FormatText, Writer: &buf})
	if err != nil {
		t.Fatalf("New() returned %v", err)
	}

	logger.Info("provider request",
		slog.String("provider", "deepseek"),
		slog.String("api_key", "sk-live-should-not-appear"),
		slog.String("OPENAI_API_KEY", "sk-other-secret"),
		slog.String("authorization", "Bearer top-secret"),
		slog.String("model", "deepseek-chat"),
	)

	out := buf.String()
	for _, secret := range []string{"sk-live-should-not-appear", "sk-other-secret", "top-secret"} {
		if strings.Contains(out, secret) {
			t.Fatalf("log output leaked %q: %s", secret, out)
		}
	}
	if strings.Count(out, RedactedPlaceholder) != 3 {
		t.Fatalf("log output = %s, want three redacted values", out)
	}
	for _, kept := range []string{"deepseek", "deepseek-chat"} {
		if !strings.Contains(out, kept) {
			t.Fatalf("log output = %s, want it to keep %q", out, kept)
		}
	}
}

func TestLoggerRedactsWithAttrsAndGroups(t *testing.T) {
	var buf bytes.Buffer
	logger, err := New(Options{Level: "info", Format: FormatJSON, Writer: &buf})
	if err != nil {
		t.Fatalf("New() returned %v", err)
	}

	logger.With(slog.String("token", "abc")).WithGroup("request").Info("started",
		slog.String("password", "hunter2"),
		slog.String("path", "/v1/chat/completions"),
	)

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("output is not valid JSON: %v (%s)", err, buf.String())
	}
	if record["token"] != RedactedPlaceholder {
		t.Fatalf("token = %v, want the placeholder", record["token"])
	}
	group, ok := record["request"].(map[string]any)
	if !ok {
		t.Fatalf("request = %#v, want a nested group", record["request"])
	}
	if group["password"] != RedactedPlaceholder {
		t.Fatalf("nested password = %v, want the placeholder", group["password"])
	}
	if group["path"] != "/v1/chat/completions" {
		t.Fatalf("nested path = %v, want it preserved", group["path"])
	}
}

func TestSetupInstallsDefaultLogger(t *testing.T) {
	var buf bytes.Buffer
	logger, err := Setup(Options{Level: "warn", Format: FormatText, Writer: &buf})
	if err != nil {
		t.Fatalf("Setup() returned %v", err)
	}

	logger.Debug("hidden")
	logger.Warn("visible")

	out := buf.String()
	if strings.Contains(out, "hidden") {
		t.Fatalf("debug record was emitted at warn level: %s", out)
	}
	if !strings.Contains(out, "visible") {
		t.Fatalf("warn record was dropped: %s", out)
	}
	if slog.Default() != logger {
		t.Fatal("Setup should install the logger as the slog default")
	}
}

func TestIsSensitiveKeyDelegatesToSecurity(t *testing.T) {
	if !IsSensitiveKey("DB_PASSWORD", nil) {
		t.Fatal("DB_PASSWORD should be sensitive")
	}
	if IsSensitiveKey("workspace", nil) {
		t.Fatal("workspace should not be sensitive")
	}
	if got := DefaultRedactPatterns(); len(got) == 0 {
		t.Fatal("DefaultRedactPatterns must not be empty")
	}
}
