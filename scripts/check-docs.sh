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
# PawAgents documentation check.
#
# The README promises commands, so the commands have to run. This script builds
# the binary, creates a configuration in a scratch home and executes every
# invocation the documentation shows, asserting the exit status each one is
# documented to have. Nothing here touches the real ~/.pawagents.
#
# Commands that need a model endpoint are gated, exactly like the integration
# tests, so the script is useful on a laptop with no Ollama running:
#
#   PAWAGENTS_TEST_OLLAMA=1  also run the commands that contact an endpoint
#
# Usage: ./scripts/check-docs.sh

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

binary="$work/pagent"
home="$work/home"
mkdir -p "$home"

echo "==> building the binary"
go build -o "$binary" ./cmd/pagent

export PAWAGENTS_HOME="$home"
export PAWAGENTS_CONFIG=""
# The generated configuration names cloud providers. A placeholder keeps the
# offline checks green without pretending a real credential exists.
export OPENAI_API_KEY="docs-check-placeholder"
export ANTHROPIC_API_KEY="docs-check-placeholder"
export DEEPSEEK_API_KEY="docs-check-placeholder"

failures=0
checks=0

# expect runs one documented invocation and asserts the documented status.
#   expect <exit status> <description> <arguments...>
expect() {
  local want="$1"; shift
  local description="$1"; shift
  checks=$((checks + 1))

  set +e
  "$binary" "$@" >"$work/out" 2>"$work/err"
  local code=$?
  set -e

  if [ "$code" -ne "$want" ]; then
    failures=$((failures + 1))
    echo "  ✗ pagent $* → exit $code, want $want ($description)"
    sed 's/^/      /' "$work/err" | tail -3
    return
  fi
  echo "  ✓ pagent $* → exit $code"
}

echo "==> quick start"
expect 0 "create a starter configuration" config init
expect 0 "inspect the configuration" config show
expect 0 "inspect the configuration as JSON" config show --output json
expect 0 "check the configuration" config validate

echo "==> commands"
expect 0 "build metadata" version
expect 0 "build metadata, short form" version --output short
expect 0 "build metadata as JSON" version --output json
expect 0 "the resolved configuration path" config path
expect 0 "list the providers" provider list
expect 0 "describe a provider" provider show anthropic
expect 0 "list the model aliases" model list
expect 0 "describe a model alias" model show local-coder
expect 0 "list the agents" agent list
expect 0 "describe an agent" agent show reviewer
echo "==> sessions"
expect 0 "list an empty store" session list
if ! grep -q "no sessions recorded" "$work/out"; then
  echo "  ✗ session list did not say that the store is empty"
  failures=$((failures + 1))
fi
expect 0 "list an empty store as JSON" session list --output json

echo "==> diagnostics"
# The starter configuration names a local model on Ollama. Whether that model is
# pulled is a property of the machine, so the assertion is on the rule the
# README states: problems are named, and the exit status is the kind of the
# first failed check. A missing local model is a NotFoundError, exit 14.
checks=$((checks + 1))
set +e
"$binary" doctor --workspace "$repo_root" >"$work/doctor" 2>"$work/doctor-err"
doctor_code=$?
set -e
case "$doctor_code" in
  0)
    echo "  ✓ pagent doctor → exit 0 (every local check passed)" ;;
  14)
    if grep -q "ollama pull" "$work/doctor"; then
      echo "  ✓ pagent doctor → exit 14, the first failure is the local model"
    else
      failures=$((failures + 1))
      echo "  ✗ pagent doctor → exit 14 without naming the missing model"
      grep '✗' "$work/doctor" | sed 's/^/      /' | head -3
    fi ;;
  *)
    failures=$((failures + 1))
    echo "  ✗ pagent doctor → exit $doctor_code, want 0 or 14"
    sed 's/^/      /' "$work/doctor-err" | tail -3 ;;
esac

# The JSON report carries the same verdict in machine readable form: exit 0
# exactly when the summary counts no failure.
checks=$((checks + 1))
set +e
"$binary" doctor --output json >"$work/doctor.json" 2>/dev/null
json_code=$?
set -e
if python3 -c "
import json, sys
report = json.load(open('$work/doctor.json'))
failed = report['summary']['fail']
sys.exit(0 if (failed == 0) == ($json_code == 0) else 1)
"; then
  echo "  ✓ pagent doctor --output json → exit $json_code, consistent with the summary"
else
  failures=$((failures + 1))
  echo "  ✗ pagent doctor --output json → exit $json_code does not match its summary"
fi

# expect_any accepts any of the documented statuses. Whether a live endpoint
# serves what the configuration asks for is a property of the machine, so the
# assertion is on the contract: a passing command exits 0, a failing one exits
# with the kind of the failure and names it.
#   expect_any <description> <statuses...> -- <arguments...>
expect_any() {
  local description="$1"; shift
  local allowed=" $1 "; shift
  shift # the -- separator
  checks=$((checks + 1))

  set +e
  "$binary" "$@" >"$work/out" 2>"$work/err"
  local code=$?
  set -e

  if [[ "$allowed" != *" $code "* ]]; then
    failures=$((failures + 1))
    echo "  ✗ pagent $* → exit $code, want one of:$allowed"
    tail -3 "$work/err" | sed 's/^/      /'
    return
  fi

  # A non-zero status has to come with a named problem, otherwise the command
  # failed without telling the user what to fix.
  if [ "$code" -ne 0 ] && ! grep -q "✗" "$work/out"; then
    failures=$((failures + 1))
    echo "  ✗ pagent $* → exit $code without naming the failed check"
    tail -3 "$work/err" | sed 's/^/      /'
    return
  fi

  echo "  ✓ pagent $* → exit $code ($description)"
}

if [ "${PAWAGENTS_TEST_OLLAMA:-}" = "1" ]; then
  echo "==> commands that need an endpoint (PAWAGENTS_TEST_OLLAMA=1)"
  expect_any "reachable endpoint, models installed" "0 5" -- \
    provider test local-ollama
  expect_any "probe answers, or names what is missing" "0 5 14" -- \
    agent show local-reviewer --probe
else
  echo "==> skipping the commands that need a model endpoint"
  echo "    set PAWAGENTS_TEST_OLLAMA=1 with a live provider to run them"
fi

echo
if [ "$failures" -ne 0 ]; then
  echo "$failures of $checks documented command(s) did not behave as documented"
  exit 1
fi
echo "$checks documented command(s) behaved as documented"
