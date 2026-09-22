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
	"os"
	"path/filepath"
	"strings"
)

// IsWithin reports whether target is root or lives below it. Both paths must be
// absolute and clean.
func IsWithin(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if root == target {
		return true
	}
	return strings.HasPrefix(target, root+string(filepath.Separator))
}

// RelPath returns the path of target relative to root, or an empty string when
// target is outside root.
func RelPath(root, target string) string {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ""
	}
	if relative == "." {
		return ""
	}
	return filepath.ToSlash(relative)
}

// SanitizeName removes control characters from a name so that a repository
// entry cannot forge additional lines in tool output.
//
// A file named "main.go\nERROR: all clear" would otherwise inject a line into
// the text a model reads, which is a real prompt injection vector.
func SanitizeName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case r == '\n' || r == '\r':
			b.WriteRune(' ')
		case r < 0x20 || r == 0x7f:
			// Drop other control characters.
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// EnvValue returns the value of an environment variable for exposure to a
// model.
//
// The default is no exposure at all: a name must be explicitly allowed by
// security.allowed_env, and it is still refused when it looks like a
// credential. PawAgents never hands a model the environment of the machine it
// runs on.
func EnvValue(name string, allowed []string, redactPatterns []string) (string, bool) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", false
	}

	permitted := false
	for _, candidate := range allowed {
		if strings.TrimSpace(candidate) == trimmed {
			permitted = true
			break
		}
	}
	if !permitted {
		return "", false
	}
	if IsEnvSecret(trimmed, redactPatterns) {
		return "", false
	}

	value, ok := os.LookupEnv(trimmed)
	return value, ok
}

// EnvironmentNames returns the sorted names of the environment variables that
// may be exposed. It reports the names only, never the values.
func EnvironmentNames(allowed []string, redactPatterns []string) []string {
	out := make([]string, 0, len(allowed))
	for _, name := range allowed {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" || IsEnvSecret(trimmed, redactPatterns) {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}
