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

// Package version exposes build metadata for the pagent binary.
//
// The values are injected at build time through -ldflags, for example:
//
//	go build -ldflags "-X github.com/pawagents/pawagents/internal/version.Version=0.1.0"
//
// When the binary is built without those flags the development defaults below
// are used so that `pagent version` always produces meaningful output.
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

const (
	// Name is the project name.
	Name = "PawAgents"

	// Binary is the name of the installed executable.
	Binary = "pagent"
)

// Build metadata, overridable via -ldflags.
var (
	// Version is the semantic version of the build.
	Version = "0.1.0-dev"

	// Commit is the short git commit the binary was built from.
	Commit = "unknown"

	// Date is the UTC build timestamp.
	Date = "unknown"
)

// BuildInfo is a machine-readable description of the current build.
type BuildInfo struct {
	Name     string `json:"name"`
	Binary   string `json:"binary"`
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	Date     string `json:"date"`
	Go       string `json:"go"`
	Platform string `json:"platform"`
	Modified bool   `json:"modified"`
}

// Info collects the build metadata for the running binary, enriching it from
// the embedded Go build info when -ldflags were not provided (for example when
// running `go run ./cmd/pagent`).
func Info() BuildInfo {
	info := BuildInfo{
		Name:     Name,
		Binary:   Binary,
		Version:  Version,
		Commit:   Commit,
		Date:     Date,
		Go:       runtime.Version(),
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
	}

	if build, ok := debug.ReadBuildInfo(); ok {
		if info.Version == "0.1.0-dev" && build.Main.Version != "" &&
			build.Main.Version != "(devel)" {
			info.Version = build.Main.Version
		}
		for _, setting := range build.Settings {
			switch setting.Key {
			case "vcs.revision":
				if info.Commit == "unknown" && len(setting.Value) >= 7 {
					info.Commit = setting.Value[:7]
				}
			case "vcs.time":
				if info.Date == "unknown" {
					info.Date = setting.Value
				}
			case "vcs.modified":
				info.Modified = setting.Value == "true"
			}
		}
	}

	return info
}

// String returns a single-line summary such as "pagent 0.1.0-dev (abc1234)".
func String() string {
	info := Info()
	out := fmt.Sprintf("%s %s", info.Binary, info.Version)
	if info.Commit != "unknown" {
		out += fmt.Sprintf(" (%s)", info.Commit)
	}
	if info.Modified {
		out += " dirty"
	}
	return out
}
