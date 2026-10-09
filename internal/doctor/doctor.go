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

package doctor

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/config"
	"github.com/pawagents/pawagents/internal/mcpserver"
	"github.com/pawagents/pawagents/internal/orchestrator"
	"github.com/pawagents/pawagents/internal/provider"
	"github.com/pawagents/pawagents/internal/security"
)

// Runtime is the part of the orchestrator the checks inspect.
//
// Taking an interface keeps the diagnostics reachable from a test without a
// provider, and it documents exactly how much of the runtime a diagnosis is
// allowed to touch.
type Runtime interface {
	// Profiles lists the configured agents.
	Profiles() []orchestrator.AgentProfile
	// Models lists the configured model aliases and their targets.
	Models() []orchestrator.ModelEntry
	// CheckAgent reports whether an agent can run. With probe false the answer
	// comes from the provider contract rather than from the endpoint.
	CheckAgent(ctx context.Context, name string, probe bool) (orchestrator.CapabilityReport, error)
}

// Endpoints is the part of the provider registry the checks inspect.
//
// *provider.Registry satisfies it.
type Endpoints interface {
	// Infos describes every configured provider without connecting.
	Infos() []provider.Info
	// Provider builds one provider, which may open a connection.
	Provider(ctx context.Context, name string) (provider.Provider, error)
	// Supports reports whether this build implements a provider type.
	Supports(providerType string) bool
}

// Options configures a run.
type Options struct {
	// Config is the loaded configuration. When it is nil, ConfigError explains
	// why, and the run reports only that.
	Config *config.Config
	// ConfigPath is the file the configuration came from, for the report.
	ConfigPath string
	// ConfigError is the failure that stopped the configuration from loading.
	ConfigError error
	// Validation is the report of `pagent config validate`, when available.
	Validation *config.ValidationResult
	// Runtime inspects agents and models.
	Runtime Runtime
	// Endpoints inspects providers. It may be nil, which skips their probes.
	Endpoints Endpoints
	// Workspace is the directory a delegation would read. Empty means the
	// configured workspace root, and then the current directory.
	Workspace string
	// Probe asks remote endpoints too. Local model servers are always probed,
	// because that is where the interesting failures are and the call is local.
	Probe bool
	// Version is the build that produced the report.
	Version string
	// Logger receives diagnostics about the checks themselves.
	Logger *slog.Logger
}

// Run performs the checks and returns the report.
//
// A run never fails: a broken installation is the thing being diagnosed, so
// every problem is a line in the report rather than an error. The caller maps
// the report onto an exit status with ExitKind.
func Run(ctx context.Context, options Options) *Report {
	report := &Report{Version: options.Version, Config: options.ConfigPath}

	// A configuration that cannot be loaded makes every other check guess, so
	// the run stops there with the reason a user needs first.
	if options.Config == nil || options.ConfigError != nil {
		report.add("Config", []Check{
			fail("configuration", describeLoadFailure(options), apperrors.KindConfig),
		})
		report.Summarize()
		return report
	}

	cfg := options.Config
	report.add("Config", checkConfig(options))
	report.add("Providers", checkProviders(ctx, cfg, options))
	report.add("Ollama", checkOllama(ctx, cfg, options))
	report.add("Agents", checkAgents(ctx, cfg, options))
	report.add("Workspace", checkWorkspace(cfg, options))
	report.add("MCP", checkMCP(options))

	report.Summarize()
	return report
}

// describeLoadFailure explains why the configuration could not be loaded.
func describeLoadFailure(options Options) string {
	if options.ConfigError == nil {
		return "no configuration was loaded"
	}
	detail := apperrors.Summary(options.ConfigError)
	if detail == "" {
		detail = options.ConfigError.Error()
	}
	return detail
}

// checkConfig reports the configuration file and its validation result.
func checkConfig(options Options) []Check {
	checks := []Check{pass(options.ConfigPath, "loaded")}

	if options.Validation == nil {
		return checks
	}
	for _, problem := range options.Validation.Errors {
		checks = append(checks, fail(apperrors.Summary(problem), "validation error", apperrors.KindConfig))
	}
	for _, warning := range options.Validation.Warnings {
		checks = append(checks, warn(warning, "validation warning", apperrors.KindConfig))
	}
	return checks
}

// checkProviders reports every configured provider.
//
// A provider problem is only a failure when something actually uses it: a
// configuration may legitimately carry a provider that no agent reaches, and
// reporting that as a failure would train a user to ignore the report.
func checkProviders(ctx context.Context, cfg *config.Config, options Options) []Check {
	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)

	used := providersInUse(options.Runtime)

	var checks []Check
	for _, name := range names {
		configuration := cfg.Providers[name]
		if configuration == nil {
			continue
		}
		checks = append(checks, checkProvider(ctx, name, configuration, used[name], options)...)
	}

	if len(checks) == 0 {
		checks = append(checks, warn("providers", "none are configured", ""))
	}
	return checks
}

// checkProvider reports one provider. A provider that needs probing reports the
// provider line and the endpoint line, so that "configured" and "reachable" stay
// two separate facts.
func checkProvider(ctx context.Context, name string, configuration *config.Provider, used bool, options Options) []Check {
	label := fmt.Sprintf("%s (%s)", name, configuration.Type)

	if configuration.Disabled {
		return []Check{warn(label, "disabled in the configuration", "")}
	}
	if options.Endpoints != nil && !options.Endpoints.Supports(configuration.Type) {
		detail := fmt.Sprintf("this build does not implement the %q provider type", configuration.Type)
		if used {
			return []Check{fail(label, detail, apperrors.KindCapability)}
		}
		return []Check{warn(label, detail+" (unused by any model alias)", "")}
	}

	// A credential is read at request time, so its absence is invisible until a
	// task fails. Checking it here is the whole point of the command.
	if missing := missingCredential(configuration); missing != "" {
		detail := fmt.Sprintf("environment variable %s is not set", missing)
		if used {
			return []Check{fail(label, detail, apperrors.KindAuthentication)}
		}
		return []Check{warn(label, detail+" (unused by any model alias)", "")}
	}

	detail := "no credential needed"
	switch {
	case strings.TrimSpace(configuration.BaseURL) != "":
		detail = configuration.BaseURL
	case configuration.Type != config.ProviderTypeOllama:
		detail = "the endpoint this build defaults to"
	}

	checks := []Check{pass(label, detail)}
	if probe := probeProvider(ctx, name, configuration, options); probe != nil {
		checks = append(checks, *probe)
	}
	return checks
}

// missingCredential returns the name of an unset credential variable, or "".
func missingCredential(configuration *config.Provider) string {
	name := strings.TrimSpace(configuration.APIKeyEnv)
	if name == "" {
		return ""
	}
	if strings.TrimSpace(configuration.APIKey) != "" {
		// An inline key wins, so the variable does not have to be set.
		return ""
	}
	// An exported but empty variable is a common misconfiguration, so it counts
	// as missing rather than as present.
	if value, ok := os.LookupEnv(name); ok && strings.TrimSpace(value) != "" {
		return ""
	}
	return name
}

// probeProvider asks an endpoint what it is, when asking is appropriate.
//
// A local model server is always asked: the call is local, cheap, and the most
// common reason a local setup does not work. A remote endpoint is only asked
// with --probe, because a diagnosis should not depend on the network and must
// never spend a credential the user did not offer.
func probeProvider(ctx context.Context, name string, configuration *config.Provider, options Options) *Check {
	if options.Endpoints == nil {
		return nil
	}
	// An Ollama endpoint is reported by the Ollama section, with the models it
	// serves; probing it twice would say the same thing twice.
	if configuration.Type == config.ProviderTypeOllama {
		return nil
	}
	if !options.Probe && !isLocalServer(configuration) {
		return nil
	}

	instance, err := options.Endpoints.Provider(ctx, name)
	if err != nil {
		check := fail(name, apperrors.Summary(err), apperrors.KindOf(err))
		return &check
	}

	reporter, ok := instance.(provider.HealthReporter)
	if !ok {
		return nil
	}

	health, err := reporter.Health(ctx)
	if err != nil {
		check := fail(fmt.Sprintf("%s endpoint", name), apperrors.Summary(err), apperrors.KindProvider)
		return &check
	}
	if !health.Reachable {
		detail := "the endpoint did not answer"
		if health.Detail != "" {
			detail = health.Detail
		}
		check := fail(fmt.Sprintf("%s endpoint", name), detail, apperrors.KindProvider)
		return &check
	}

	check := pass(fmt.Sprintf("%s endpoint", name), health.Detail)
	return &check
}

// isLocalServer reports whether a provider almost certainly runs on this
// machine: Ollama is a local model server by definition, and a loopback address
// is this machine whatever the type says.
func isLocalServer(configuration *config.Provider) bool {
	if configuration.Type == config.ProviderTypeOllama {
		return true
	}
	return isLoopback(configuration.BaseURL)
}

// isLoopback reports whether a base URL points at this machine.
func isLoopback(baseURL string) bool {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return false
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	switch host {
	case "localhost", "127.0.0.1", "::1", "0.0.0.0":
		return true
	default:
		return false
	}
}

// checkOllama reports the local model servers and the models the configuration
// asks them for.
//
// It is a section of its own because that is how a user thinks about a local
// model: which server, and is the model I named actually pulled.
func checkOllama(ctx context.Context, cfg *config.Config, options Options) []Check {
	if options.Endpoints == nil || options.Runtime == nil {
		return nil
	}

	var checks []Check
	for _, info := range sortedInfos(options.Endpoints) {
		configuration := cfg.Providers[info.Name]
		if configuration == nil || configuration.Disabled {
			continue
		}
		if configuration.Type != config.ProviderTypeOllama {
			continue
		}

		instance, err := options.Endpoints.Provider(ctx, info.Name)
		if err != nil {
			checks = append(checks, fail(info.Name, apperrors.Summary(err), apperrors.KindOf(err)))
			continue
		}

		if !appendOllamaHealth(ctx, info.Name, configuration, instance, &checks) {
			continue
		}

		models, err := listModels(ctx, instance)
		if err != nil {
			checks = append(checks, fail(info.Name, apperrors.Summary(err), apperrors.KindProvider))
			continue
		}

		checks = append(checks, checkOllamaModels(options.Runtime, info.Name, models)...)
	}
	return checks
}

// appendOllamaHealth reports whether the endpoint answered, and returns false
// when it did not: a listing from a server that is not there would only produce
// "model missing" lines that hide the real problem.
func appendOllamaHealth(ctx context.Context, name string, configuration *config.Provider, instance provider.Provider, checks *[]Check) bool {
	label := name + " endpoint"
	if strings.TrimSpace(configuration.BaseURL) != "" {
		label = configuration.BaseURL
	}

	reporter, ok := instance.(provider.HealthReporter)
	if !ok {
		return true
	}

	health, err := reporter.Health(ctx)
	switch {
	case err != nil:
		*checks = append(*checks, fail(name+" endpoint", apperrors.Summary(err), apperrors.KindProvider))
		return false
	case !health.Reachable:
		detail := "the endpoint did not answer"
		if health.Detail != "" {
			detail = health.Detail
		}
		*checks = append(*checks, fail(name+" endpoint", detail, apperrors.KindProvider))
		return false
	}

	*checks = append(*checks, pass(label, health.Detail))
	return true
}

// listModels asks a provider for the models it serves.
func listModels(ctx context.Context, instance provider.Provider) ([]string, error) {
	lister, ok := instance.(provider.ModelLister)
	if !ok {
		return nil, nil
	}
	return lister.ListModels(ctx)
}

// checkOllamaModels reports the models the aliases of one provider need.
func checkOllamaModels(runtime Runtime, providerName string, installed []string) []Check {
	if len(installed) == 0 {
		return nil
	}

	seen := map[string]bool{}
	var checks []Check
	for _, entry := range runtime.Models() {
		for _, target := range entry.Targets {
			if target.Provider != providerName || seen[target.Model] {
				continue
			}
			seen[target.Model] = true

			if provider.MatchModel(installed, target.Model) {
				checks = append(checks, pass(target.Model, fmt.Sprintf("used by %s", entry.Alias)))
				continue
			}
			checks = append(checks, fail(target.Model,
				fmt.Sprintf("not installed, but %s asks for it; run \"ollama pull %s\"",
					entry.Alias, target.Model),
				apperrors.KindNotFound))
		}
	}
	return checks
}

// checkAgents reports whether each agent can run.
func checkAgents(ctx context.Context, cfg *config.Config, options Options) []Check {
	if options.Runtime == nil {
		return nil
	}

	var checks []Check
	for _, profile := range options.Runtime.Profiles() {
		label := fmt.Sprintf("%s (%s)", profile.Name, profile.ModelAlias)

		if !profile.Executable {
			checks = append(checks, warn(label, "only single agents run in this release", apperrors.KindCapability))
			continue
		}

		report, err := options.Runtime.CheckAgent(ctx, profile.Name, options.Probe)
		if err != nil {
			checks = append(checks, fail(label, apperrors.Summary(err), apperrors.KindOf(err)))
			continue
		}

		// A capability report says nothing about credentials: the runtime reads
		// those at request time, after it has already chosen a target. An agent
		// whose alias starts with a provider that has no credential therefore
		// fails the moment it is delegated to, which is exactly what this
		// command exists to catch.
		if missing := credentialGaps(cfg, options, profile); len(missing) > 0 {
			checks = append(checks, fail(label,
				"no credential for "+strings.Join(missing, ", "), apperrors.KindAuthentication))
			continue
		}

		switch {
		case report.OK:
			detail := report.Target()
			if !report.Confirmed() {
				detail += " (capabilities are confirmed by the endpoint when a task runs)"
			}
			checks = append(checks, pass(label, detail))
		case report.Provider == "" || report.Capabilities == nil:
			// No target could be resolved at all, so this is a problem with the
			// configuration rather than a question for an endpoint.
			checks = append(checks, fail(label, describeStructuralProblem(report), apperrors.KindConfig))
		case report.Confirmed():
			checks = append(checks, fail(label, describeNotReady(report), apperrors.KindCapability))
		default:
			// The verdict came from a conservative default, so the honest report
			// is that the answer is unknown until the endpoint is asked.
			checks = append(checks, warn(label,
				"the endpoint decides whether this model can run the agent; verify with \"pagent agent show "+profile.Name+" --probe\"",
				""))
		}
	}
	return checks
}

// describeNotReady explains an authoritative refusal.
func describeNotReady(report orchestrator.CapabilityReport) string {
	switch {
	case len(report.Missing) > 0:
		return "the model does not provide " + strings.Join(report.Missing, ", ")
	case report.Error != "":
		return report.Error
	default:
		return "the agent cannot run against this model"
	}
}

// checkWorkspace reports the directory a delegation would read.
func checkWorkspace(cfg *config.Config, options Options) []Check {
	directory := strings.TrimSpace(options.Workspace)
	if directory == "" {
		directory = strings.TrimSpace(cfg.Security.WorkspaceRoot)
	}
	if directory == "" {
		current, err := os.Getwd()
		if err != nil {
			return []Check{fail("workspace", "cannot determine the current directory", apperrors.KindWorkspace)}
		}
		directory = current
	}

	workspace, err := security.NewWorkspace(directory, security.WorkspaceOptions{
		AllowOutside:   cfg.Security.AllowOutsideWorkspace,
		FollowSymlinks: cfg.Security.FollowSymlinks,
		MaxFileBytes:   cfg.Security.MaxFileSize,
		BlockedPaths:   cfg.Security.BlockedPaths,
	})
	if err != nil {
		return []Check{fail(directory, apperrors.Summary(err), apperrors.KindWorkspace)}
	}
	defer func() { _ = workspace.Close() }()

	var checks []Check
	root := workspace.Path()
	checks = append(checks, pass(root, fmt.Sprintf("readable, at most %s per file", humanBytes(workspace.MaxFileBytes()))))

	if repository := repositoryRoot(root); repository != "" {
		checks = append(checks, pass("Git repository", repository))
	} else {
		checks = append(checks, warn("Git repository",
			"not inside a repository, so the git.* tools will refuse to run", ""))
	}
	if cfg.Security.AllowOutsideWorkspace {
		checks = append(checks, warn(root,
			"security.allow_outside_workspace is on, so reads may leave the workspace",
			apperrors.KindPermission))
	}
	return checks
}

// repositoryRoot walks up from a directory looking for a repository, which is
// what makes the git tools usable.
func repositoryRoot(directory string) string {
	current := directory
	for {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

// checkMCP reports what a coding agent would see when it connects.
func checkMCP(options Options) []Check {
	checks := []Check{pass("stdio transport available",
		"pagent mcp serve --stdio exposes "+strings.Join(mcpserver.Tools(), ", "))}

	if options.Runtime == nil {
		return checks
	}
	if len(options.Runtime.Profiles()) == 0 {
		return append(checks, warn("agents",
			"no agent is configured, so a host would connect to a server with nothing to delegate to", ""))
	}

	checks = append(checks, pass("registration",
		"codex mcp add pawagents -- pagent mcp serve --stdio\n"+
			"claude mcp add pawagents -- pagent mcp serve --stdio"))
	return checks
}

// describeStructuralProblem explains an agent whose model could not be resolved.
func describeStructuralProblem(report orchestrator.CapabilityReport) string {
	switch {
	case report.Error != "":
		return report.Error
	case report.Status != "":
		return "no usable target: " + report.Status
	default:
		return "the model alias does not resolve to a provider target"
	}
}

// credentialGaps returns the providers a delegation would try that have no
// credential in the environment.
//
// Every target of the alias is checked, not only the one the report names, and
// a target whose provider type this build does not implement is skipped: the
// runtime never reaches it, so its credential is irrelevant.
func credentialGaps(cfg *config.Config, options Options, profile orchestrator.AgentProfile) []string {
	var gaps []string
	for _, target := range profile.Targets {
		configuration := cfg.Providers[target.Provider]
		if configuration == nil || configuration.Disabled {
			continue
		}
		if options.Endpoints != nil && !options.Endpoints.Supports(configuration.Type) {
			continue
		}
		if missing := missingCredential(configuration); missing != "" {
			gaps = append(gaps, fmt.Sprintf("%s (set %s)", target.Provider, missing))
		}
	}
	return gaps
}

// providersInUse returns the providers some model alias resolves to.
func providersInUse(runtime Runtime) map[string]bool {
	used := map[string]bool{}
	if runtime == nil {
		return used
	}
	for _, entry := range runtime.Models() {
		for _, target := range entry.Targets {
			used[target.Provider] = true
		}
	}
	return used
}

// sortedInfos returns the provider descriptions in name order.
func sortedInfos(endpoints Endpoints) []provider.Info {
	infos := endpoints.Infos()
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos
}

// humanBytes renders a byte count for a diagnostic line.
func humanBytes(size int64) string {
	switch {
	case size <= 0:
		return "unbounded"
	case size < 1024:
		return fmt.Sprintf("%d bytes", size)
	case size < 1024*1024:
		return fmt.Sprintf("%d KiB", size/1024)
	default:
		return fmt.Sprintf("%d MiB", size/(1024*1024))
	}
}
