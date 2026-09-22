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

package config

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/pawagents/pawagents/internal/apperrors"
)

// Environment variables honoured by the loader. They exist so that containers
// and CI jobs can override a single knob without rewriting the file.
const (
	// EnvConfigPath overrides the configuration file location.
	EnvConfigPath = "PAWAGENTS_CONFIG"
	// EnvHome overrides the PawAgents home directory (~/.pawagents).
	EnvHome = "PAWAGENTS_HOME"
	// EnvLogLevel overrides telemetry.log_level.
	EnvLogLevel = "PAWAGENTS_LOG_LEVEL"
	// EnvLogFormat overrides telemetry.log_format.
	EnvLogFormat = "PAWAGENTS_LOG_FORMAT"
	// EnvSessionsDir overrides sessions.directory.
	EnvSessionsDir = "PAWAGENTS_SESSIONS_DIR"
	// EnvTelemetryEnabled overrides telemetry.enabled.
	EnvTelemetryEnabled = "PAWAGENTS_TELEMETRY_ENABLED"
	// EnvAllowOutsideWorkspace overrides security.allow_outside_workspace.
	EnvAllowOutsideWorkspace = "PAWAGENTS_ALLOW_OUTSIDE_WORKSPACE"
)

// ConfigFileName is the file name looked up inside the PawAgents home
// directory.
const ConfigFileName = "config.yaml"

// LoadOptions controls Load and LoadOrDefaults.
type LoadOptions struct {
	// Path is an explicit configuration file. Empty means "resolve from
	// PAWAGENTS_CONFIG and then the default location".
	Path string
	// SkipEnvOverrides disables the environment overrides. It exists for
	// tests that must observe the file contents verbatim.
	SkipEnvOverrides bool
}

// Home returns the PawAgents home directory, honouring PAWAGENTS_HOME.
func Home() (string, error) {
	if override := strings.TrimSpace(os.Getenv(EnvHome)); override != "" {
		return absPath(ExpandPath(override))
	}
	base, err := os.UserHomeDir()
	if err != nil {
		return "", apperrors.Wrap(apperrors.KindConfig, "config.home",
			"cannot determine the user home directory", err)
	}
	return filepath.Join(base, ".pawagents"), nil
}

// DefaultPath returns ~/.pawagents/config.yaml.
func DefaultPath() (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ConfigFileName), nil
}

// ResolvePath determines which configuration file to use. Precedence is
// explicit argument, PAWAGENTS_CONFIG, then the default path.
func ResolvePath(explicit string) (string, error) {
	if trimmed := strings.TrimSpace(explicit); trimmed != "" {
		return absPath(ExpandPath(trimmed))
	}
	if fromEnv := strings.TrimSpace(os.Getenv(EnvConfigPath)); fromEnv != "" {
		return absPath(ExpandPath(fromEnv))
	}
	return DefaultPath()
}

// ExpandPath expands a leading "~" into the user home directory. Paths that do
// not start with "~" are returned unchanged, which keeps URLs and relative
// paths intact.
func ExpandPath(path string) string {
	if path == "" {
		return ""
	}
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

// Exists reports whether a regular file exists at path.
func Exists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(ExpandPath(path))
	return err == nil && !info.IsDir()
}

// Decode parses a configuration document. Unknown fields are rejected so that
// a typo such as `base_ur:` fails loudly instead of being silently ignored.
func Decode(data []byte) (*Config, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	cfg := &Config{}
	if err := decoder.Decode(cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, apperrors.New(apperrors.KindConfig, "config.parse",
				"configuration file is empty")
		}
		return nil, apperrors.New(apperrors.KindConfig, "config.parse", "%s",
			cleanYAMLError(err.Error()))
	}

	// Reject a second YAML document: PawAgents supports exactly one.
	var extra yaml.Node
	if err := decoder.Decode(&extra); err == nil {
		return nil, apperrors.New(apperrors.KindConfig, "config.parse",
			"configuration file must contain a single YAML document")
	} else if !errors.Is(err, io.EOF) {
		return nil, apperrors.New(apperrors.KindConfig, "config.parse", "%s",
			cleanYAMLError(err.Error()))
	}

	if cfg.Providers == nil {
		cfg.Providers = map[string]*Provider{}
	}
	if cfg.Models == nil {
		cfg.Models = map[string]*Model{}
	}
	if cfg.Agents == nil {
		cfg.Agents = map[string]*Agent{}
	}
	return cfg, nil
}

// Read loads a configuration file and runs the defaulting and normalization
// steps. It returns a ConfigError when the file is missing or malformed and
// never performs validation, so that callers can print a full validation
// report instead of stopping at the first problem.
func Read(path string, skipEnvOverrides bool) (*Config, error) {
	resolved := ExpandPath(path)
	data, err := os.ReadFile(resolved)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, apperrors.Wrap(apperrors.KindConfig, "config.read",
				"configuration file %s does not exist", err, resolved)
		}
		return nil, apperrors.Wrap(apperrors.KindConfig, "config.read",
			"cannot read configuration file %s", err, resolved)
	}

	cfg, err := Decode(data)
	if err != nil {
		return nil, apperrors.New(apperrors.KindConfig, "config.parse",
			"%s: %s", resolved, apperrors.Summary(err))
	}

	cfg.Path = resolved
	if !skipEnvOverrides {
		applyEnvOverrides(cfg)
	}
	ApplyDefaults(cfg)
	cfg.Normalize()
	return cfg, nil
}

// Load resolves the path, reads the file and validates the result. The
// returned ValidationResult is non-nil even when validation fails so that the
// caller can render every problem at once.
func Load(opts LoadOptions) (*Config, *ValidationResult, error) {
	path, err := ResolvePath(opts.Path)
	if err != nil {
		return nil, nil, err
	}
	cfg, err := Read(path, opts.SkipEnvOverrides)
	if err != nil {
		return nil, nil, err
	}
	return cfg, Validate(cfg), nil
}

// LoadOrDefaults behaves like Load but falls back to the built-in defaults when
// the configuration file does not exist. The returned bool reports whether the
// defaults were used.
func LoadOrDefaults(opts LoadOptions) (*Config, *ValidationResult, bool, error) {
	path, err := ResolvePath(opts.Path)
	if err != nil {
		return nil, nil, false, err
	}

	cfg, readErr := Read(path, opts.SkipEnvOverrides)
	if readErr == nil {
		return cfg, Validate(cfg), false, nil
	}
	if !errors.Is(readErr, fs.ErrNotExist) {
		return nil, nil, false, readErr
	}

	fallback := Default()
	fallback.Path = path
	return fallback, Validate(fallback), true, nil
}

// WriteFile writes a configuration document, creating parent directories. It
// refuses to overwrite an existing file unless force is set.
func WriteFile(path, contents string, force bool) error {
	resolved, err := absPath(ExpandPath(path))
	if err != nil {
		return err
	}
	if !force && Exists(resolved) {
		return apperrors.New(apperrors.KindConfig, "config.write",
			"%s already exists (use --force to overwrite)", resolved)
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return apperrors.Wrap(apperrors.KindConfig, "config.write",
			"cannot create %s", err, filepath.Dir(resolved))
	}
	if err := os.WriteFile(resolved, []byte(contents), 0o600); err != nil {
		return apperrors.Wrap(apperrors.KindConfig, "config.write",
			"cannot write %s", err, resolved)
	}
	return nil
}

// applyEnvOverrides mutates cfg with values found in the environment.
func applyEnvOverrides(cfg *Config) {
	if cfg == nil {
		return
	}
	if value := strings.TrimSpace(os.Getenv(EnvLogLevel)); value != "" {
		cfg.Telemetry.LogLevel = value
	}
	if value := strings.TrimSpace(os.Getenv(EnvLogFormat)); value != "" {
		cfg.Telemetry.LogFormat = value
	}
	if value := strings.TrimSpace(os.Getenv(EnvSessionsDir)); value != "" {
		cfg.Sessions.Directory = ExpandPath(value)
	}
	if value, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(EnvTelemetryEnabled))); err == nil {
		cfg.Telemetry.Enabled = value
	}
	if value, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(EnvAllowOutsideWorkspace))); err == nil {
		cfg.Security.AllowOutsideWorkspace = value
	}
}

// absPath converts a path into an absolute, cleaned path.
func absPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", apperrors.New(apperrors.KindConfig, "config.path",
			"configuration path must not be empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", apperrors.Wrap(apperrors.KindConfig, "config.path",
			"cannot resolve %s", err, path)
	}
	return filepath.Clean(absolute), nil
}

// unknownFieldPattern rewrites the yaml.v3 wording for unknown keys into a
// shorter, more actionable message.
var unknownFieldPattern = regexp.MustCompile(`field ([^ ]+) not found in type [^\s]+`)

// cleanYAMLError turns the multi-line yaml.v3 error into a single readable
// line, for example `line 12: unknown field "base_ur"`.
func cleanYAMLError(message string) string {
	message = strings.TrimPrefix(message, "yaml: ")
	message = strings.ReplaceAll(message, "unmarshal errors:\n", "")
	lines := strings.Split(message, "\n")
	for i, line := range lines {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		line = unknownFieldPattern.ReplaceAllString(line, `unknown field "$1"`)
		lines[i] = line
	}
	out := strings.Join(lines, "; ")
	return strings.TrimSuffix(out, ";")
}
