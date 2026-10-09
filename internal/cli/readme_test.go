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

package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// documentedCommand matches a PawAgents invocation in prose: the binary followed
// by its first argument.
var documentedCommand = regexp.MustCompile(`\bpagent ([a-z][a-z0-9-]*)`)

// commandTree returns every command path the binary implements, for example
// "agent show", so that documentation can be checked against reality.
func commandTree(t *testing.T) map[string]bool {
	t.Helper()

	root := newRootCmd(NewApp())
	paths := map[string]bool{}

	var walk func(cmd *cobra.Command, prefix string)
	walk = func(cmd *cobra.Command, prefix string) {
		for _, child := range cmd.Commands() {
			name := child.Name()
			if child.Hidden || name == "help" || name == "completion" {
				continue
			}
			path := strings.TrimSpace(prefix + " " + name)
			paths[path] = true
			walk(child, path)
		}
	}
	walk(root, "")

	return paths
}

// TestREADMEOnlyDocumentsImplementedCommands enforces the rule the development
// document sets: the README must not mention a command this tree does not
// implement.
//
// It is a test rather than a review step because documentation drifts silently:
// a command is renamed or removed, and the README keeps promising it. The check
// reads the README the way a user does and resolves every `pagent <word>` it
// finds against the command tree.
func TestREADMEOnlyDocumentsImplementedCommands(t *testing.T) {
	root := commandTree(t)

	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("cannot read the README: %v", err)
	}

	mentioned := map[string]bool{}
	for _, match := range documentedCommand.FindAllStringSubmatch(string(readme), -1) {
		mentioned[match[1]] = true
	}
	// A phantom command is the failure this test exists for: documentation that
	// promises something the binary does not do.
	var phantom []string
	for name := range mentioned {
		if !root[name] {
			phantom = append(phantom, name)
		}
	}
	sort.Strings(phantom)
	if len(phantom) > 0 {
		t.Fatalf("the README documents commands this build does not implement: %v\n"+
			"implemented: %v", phantom, sortedKeys(root))
	}

	// The other direction matters too: a command a user can only discover by
	// accident is a command that may as well not exist.
	var undocumented []string
	for _, name := range topLevel(root) {
		if !mentioned[name] {
			undocumented = append(undocumented, name)
		}
	}
	if len(undocumented) > 0 {
		t.Fatalf("the README does not mention these commands: %v", undocumented)
	}

	// Every subcommand the README names has to exist too: `pagent config show`
	// must not survive a rename of `show`.
	for _, line := range strings.Split(string(readme), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		for index, field := range fields {
			if field != "pagent" || index+2 >= len(fields) {
				continue
			}
			second := strings.Trim(fields[index+1], "`|,.")
			third := strings.Trim(fields[index+2], "`|,.")
			if !root[second] || !hasChildren(root, second) {
				continue
			}
			if !root[second+" "+third] {
				t.Fatalf("the README names \"pagent %s %s\", which does not exist",
					second, third)
			}
		}
	}
}

// topLevel lists the commands directly under the binary, in name order.
func topLevel(root map[string]bool) []string {
	var names []string
	for path := range root {
		if !strings.Contains(path, " ") {
			names = append(names, path)
		}
	}
	sort.Strings(names)
	return names
}

// hasChildren reports whether a command path has subcommands of its own.
func hasChildren(root map[string]bool, path string) bool {
	for candidate := range root {
		if strings.HasPrefix(candidate, path+" ") {
			return true
		}
	}
	return false
}

// sortedKeys returns the keys of a set, sorted, for a readable failure.
func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// TestEveryCommandHasHelp checks the rule that flags and commands are
// self-documenting: a user must be able to ask any node what it does.
func TestEveryCommandHasHelp(t *testing.T) {
	root := newRootCmd(NewApp())

	var walk func(cmd *cobra.Command, prefix string)
	walk = func(cmd *cobra.Command, prefix string) {
		path := strings.TrimSpace(prefix + " " + cmd.Name())
		if !cmd.Hidden {
			if strings.TrimSpace(cmd.Short) == "" {
				t.Fatalf("%q has no short description", path)
			}
			if cmd.Run != nil || cmd.RunE != nil {
				if strings.TrimSpace(cmd.Long) == "" {
					t.Fatalf("%q has no long description", path)
				}
			}
			// Every user-facing flag has to describe itself.
			cmd.Flags().VisitAll(func(flag *pflag.Flag) {
				if flag.Hidden {
					return
				}
				if strings.TrimSpace(flag.Usage) == "" {
					t.Fatalf("%q: flag --%s has no description", path, flag.Name)
				}
			})
		}
		for _, child := range cmd.Commands() {
			walk(child, path)
		}
	}
	for _, child := range root.Commands() {
		walk(child, "")
	}
}
