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

package version

import (
	"strings"
	"testing"
)

func TestInfoReportsPlatformAndBinary(t *testing.T) {
	info := Info()

	if info.Binary != Binary {
		t.Fatalf("binary = %q, want %q", info.Binary, Binary)
	}
	if info.Name != Name {
		t.Fatalf("name = %q, want %q", info.Name, Name)
	}
	if !strings.Contains(info.Platform, "/") {
		t.Fatalf("platform = %q, want os/arch", info.Platform)
	}
	if !strings.HasPrefix(info.Go, "go") {
		t.Fatalf("go = %q, want a go version", info.Go)
	}
	if info.Version == "" {
		t.Fatal("version must never be empty")
	}
}

func TestStringIncludesCommitWhenKnown(t *testing.T) {
	original := Commit
	t.Cleanup(func() { Commit = original })

	Commit = "unknown"
	if got := String(); strings.Contains(got, "(") {
		t.Fatalf("String() = %q, want no commit suffix when the commit is unknown", got)
	}

	Commit = "abc1234"
	got := String()
	if !strings.Contains(got, "abc1234") {
		t.Fatalf("String() = %q, want the commit", got)
	}
	if !strings.HasPrefix(got, Binary+" ") {
		t.Fatalf("String() = %q, want it to start with the binary name", got)
	}
}

func TestInfoUsesInjectedVersion(t *testing.T) {
	original := Version
	t.Cleanup(func() { Version = original })

	Version = "9.9.9"
	if got := Info().Version; got != "9.9.9" {
		t.Fatalf("version = %q, want the injected value", got)
	}
}
