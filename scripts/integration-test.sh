#!/usr/bin/env bash
#
# Copyright (c) 2026 PawAgents Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# PawAgents integration test entry point.
#
# The unit tests always run. Everything that needs something outside the
# process is gated, so this script is useful on a laptop that has no Ollama and
# neither host installed. A gated test skips with the reason it cannot run, and
# this script prints every skip, so a green run never hides an unverified chain.
#
#   PAWAGENTS_TEST_OLLAMA=1  run the tests that need a running Ollama server
#   PAWAGENTS_TEST_MCP=1     run the tests that spawn the MCP server over stdio
#   PAWAGENTS_TEST_CODEX=1   register the server with the Codex CLI
#   PAWAGENTS_TEST_CLAUDE=1  register the server with the Claude Code CLI
#
#   PAWAGENTS_TEST_MODEL            the model to review with (default: the
#                                   first model the server has installed)
#   PAWAGENTS_TEST_OLLAMA_ENDPOINT  the endpoint (default 127.0.0.1:11434)
#   PAWAGENTS_TEST_TIMEOUT          the go test timeout (default 30m, because a
#                                   local model can take minutes per task)
#
# Usage: ./scripts/integration-test.sh

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

# gate renders the state of one PAWAGENTS_TEST_* variable.
gate() {
	local name="$1"
	if [ -n "${!name:-}" ] && [ "${!name}" != "0" ]; then
		echo "on"
	else
		echo "off"
	fi
}

echo "==> building bin/pagent"
make build

echo "==> running the unit tests"
go test ./...

echo "==> running the integration tests"
echo "    ollama=$(gate PAWAGENTS_TEST_OLLAMA) mcp=$(gate PAWAGENTS_TEST_MCP) codex=$(gate PAWAGENTS_TEST_CODEX) claude=$(gate PAWAGENTS_TEST_CLAUDE)"
echo "    model=${PAWAGENTS_TEST_MODEL:-<first installed>} endpoint=${PAWAGENTS_TEST_OLLAMA_ENDPOINT:-http://127.0.0.1:11434}"

log="$(mktemp)"
trap 'rm -f "$log"' EXIT

set +e
go test -tags=integration -count=1 -timeout "${PAWAGENTS_TEST_TIMEOUT:-30m}" -v ./tests/... >"$log" 2>&1
status=$?
set -e

grep -E '^--- (PASS|FAIL|SKIP)|^(ok|FAIL)' "$log" | sed 's/^/  /' || true

passed="$(grep -c '^--- PASS' "$log" || true)"
skipped="$(grep -c '^--- SKIP' "$log" || true)"
failed="$(grep -c '^--- FAIL' "$log" || true)"
echo "  $passed passed, $skipped skipped, $failed failed"

if [ "$skipped" -gt 0 ]; then
	echo "  every skip and its reason:"
	# go test prints the reason first, from t.Skipf, and then the SKIP line, so
	# the reason is remembered until the line that closes the test.
	awk '
		/^[[:space:]]+[a-zA-Z0-9_]+_test\.go:[0-9]+: / { reason = $0; next }
		/^--- SKIP/ {
			sub(/^[[:space:]]+/, "    ", $0);
			print "    " $0;
			if (reason != "") { sub(/^[[:space:]]+/, "      ", reason); print reason }
			reason = ""
		}
	' "$log"
	echo "  set the matching PAWAGENTS_TEST_* gate to run the skipped tests"
fi

if [ "$status" -ne 0 ]; then
	echo "==> integration tests FAILED"
	tail -40 "$log"
	exit "$status"
fi

echo "==> integration test run finished"
