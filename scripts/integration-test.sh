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
# The script always runs the parts that do not need external services and
# gates the rest behind environment variables so CI never fails on a machine
# that has no Ollama, no Codex and no Claude Code installed.
#
#   PAWAGENTS_TEST_OLLAMA=1  run tests that talk to a real Ollama server
#   PAWAGENTS_TEST_MCP=1     run end-to-end MCP stdio tests
#
# Usage: ./scripts/integration-test.sh

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

echo "==> building bin/pagent"
make build

echo "==> running unit tests"
go test ./...

if [ "${PAWAGENTS_TEST_OLLAMA:-0}" = "1" ]; then
	echo "==> running Ollama integration tests"
	go test -tags=integration -count=1 ./tests/integration/...
else
	echo "==> skipping Ollama integration tests (set PAWAGENTS_TEST_OLLAMA=1)"
fi

if [ "${PAWAGENTS_TEST_MCP:-0}" = "1" ]; then
	echo "==> running MCP stdio integration tests"
	go test -tags=integration -count=1 ./tests/mcp/...
else
	echo "==> skipping MCP integration tests (set PAWAGENTS_TEST_MCP=1)"
fi

echo "==> integration test run finished"
