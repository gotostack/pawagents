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

// Package cli implements the pagent command line interface.
//
// The package is deliberately thin: every subcommand resolves the
// configuration through App, prints to the injected writers and returns a
// classified error. Nothing here contains agent or provider logic, which keeps
// the CLI usable as a debugging surface for the runtime instead of a second
// implementation of it.
package cli

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/logging"
	"github.com/pawagents/pawagents/internal/version"
)

// App holds the global CLI state shared by every subcommand: the flag values,
// the output writers, the logger and the lazily loaded configuration.
type App struct {
	// Global flag values.
	configPath string
	logLevel   string
	logFormat  string
	verbose    bool

	stdout io.Writer
	stderr io.Writer

	logger *slog.Logger

	// Configuration loading is performed at most once per process.
	once         sync.Once
	cfg          *config.Config
	validation   *config.ValidationResult
	usedDefaults bool
	loadErr      error
}

// NewApp builds an App writing to the standard streams.
func NewApp() *App {
	return &App{stdout: os.Stdout, stderr: os.Stderr}
}

// SetWriters overrides the output writers. It is used by tests.
func (a *App) SetWriters(stdout, stderr io.Writer) {
	a.stdout = stdout
	a.stderr = stderr
}

// Out returns the stdout writer reserved for command output.
func (a *App) Out() io.Writer {
	if a.stdout == nil {
		return os.Stdout
	}
	return a.stdout
}

// Err returns the stderr writer used for logs and diagnostics.
func (a *App) Err() io.Writer {
	if a.stderr == nil {
		return os.Stderr
	}
	return a.stderr
}

// Logger returns the configured logger, falling back to a disabled one when
// setupLogging has not run yet.
func (a *App) Logger() *slog.Logger {
	if a.logger != nil {
		return a.logger
	}
	return slog.New(slog.NewTextHandler(a.Err(), &slog.HandlerOptions{Level: slog.Level(127)}))
}

// LoadConfig resolves and reads the configuration once per process, then
// returns the cached result. The bool reports whether the built-in defaults
// were used because no configuration file exists.
func (a *App) LoadConfig() (*config.Config, *config.ValidationResult, bool, error) {
	a.once.Do(func() {
		a.cfg, a.validation, a.usedDefaults, a.loadErr =
			config.LoadOrDefaults(config.LoadOptions{Path: a.configPath})
	})
	return a.cfg, a.validation, a.usedDefaults, a.loadErr
}

// Validation returns the validation report, or nil when the configuration has
// not been loaded yet.
func (a *App) Validation() *config.ValidationResult {
	return a.validation
}

// Config returns a configuration that passed validation. When the report
// contains errors the aggregated error is returned instead, listing every
// problem rather than only the first one.
func (a *App) Config() (*config.Config, error) {
	cfg, result, _, err := a.LoadConfig()
	if err != nil {
		return nil, err
	}
	if result.HasErrors() {
		return nil, result.Err()
	}
	return cfg, nil
}

// PrintError renders a classified error on stderr. Continuation lines are kept
// so that aggregated validation reports stay readable.
func (a *App) PrintError(err error) {
	if err == nil {
		return
	}
	lines := strings.Split(err.Error(), "\n")
	fmt.Fprintf(a.Err(), "%s: %s\n", version.Binary, lines[0])
	for _, line := range lines[1:] {
		fmt.Fprintln(a.Err(), line)
	}
}

// setupLogging installs the structured logger. A malformed log level or format
// coming from the configuration file is ignored rather than fatal, because the
// logger must never prevent a command such as `pagent config validate` from
// running and reporting the real problem.
func (a *App) setupLogging() {
	level := a.logLevel
	format := a.logFormat
	if a.verbose && level == "" {
		level = "debug"
	}

	if level == "" || format == "" {
		if cfg, _, _, err := a.LoadConfig(); err == nil && cfg != nil {
			if level == "" {
				level = cfg.Telemetry.LogLevel
			}
			if format == "" {
				format = cfg.Telemetry.LogFormat
			}
		}
	}

	if _, err := logging.ParseLevel(level); err != nil {
		level = logging.DefaultLevel
	}
	switch strings.ToLower(strings.TrimSpace(format)) {
	case logging.FormatText, logging.FormatJSON:
	default:
		format = logging.FormatText
	}

	logger, err := logging.Setup(logging.Options{
		Level:  level,
		Format: format,
		Writer: a.Err(),
	})
	if err != nil {
		// New only fails on an invalid level or format, both of which were
		// sanitised above, so this branch is unreachable in practice.
		logger, _ = logging.New(logging.Options{
			Level:  logging.DefaultLevel,
			Format: logging.FormatText,
			Writer: a.Err(),
		})
	}
	a.logger = logger
}
