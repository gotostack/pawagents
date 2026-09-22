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

// Package prompts ships the built-in agent system prompts.
//
// The markdown files in this directory are the canonical copies referenced by
// the default configuration (`prompt: ~/.pawagents/prompts/reviewer.md`). They
// are embedded in the binary so that `pagent config init` can materialise them
// on a machine that only has the compiled executable.
package prompts

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed *.md
var files embed.FS

// Names returns the bundled prompt file names, sorted alphabetically.
func Names() []string {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			out = append(out, entry.Name())
		}
	}
	sort.Strings(out)
	return out
}

// Read returns the content of a bundled prompt by base name, for example
// "reviewer.md".
func Read(name string) (string, error) {
	data, err := files.ReadFile(name)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// WriteDefaults writes every bundled prompt into dir. Existing files are kept
// unless force is set. It returns the paths it wrote.
func WriteDefaults(dir string, force bool) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	written := make([]string, 0, len(Names()))
	for _, name := range Names() {
		target := filepath.Join(dir, name)
		if !force {
			if _, err := os.Stat(target); err == nil {
				continue
			}
		}
		content, err := Read(name)
		if err != nil {
			return written, err
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			return written, err
		}
		written = append(written, target)
	}
	return written, nil
}
