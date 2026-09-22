# PawAgents developer entry points.
#
# `make build` produces bin/pagent, the single binary that exposes both the
# human-facing CLI and the MCP server used by Codex / Claude Code.

BINARY      := bin/pagent
PKG         := github.com/pawagents/pawagents
CMD_PKG     := ./cmd/pagent

VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X $(PKG)/internal/version.Version=$(VERSION) \
	-X $(PKG)/internal/version.Commit=$(COMMIT) \
	-X $(PKG)/internal/version.Date=$(BUILD_DATE)

GOFLAGS ?=

.PHONY: all build install test vet fmt fmt-check lint integration-test clean tidy help

all: build

## build: compile bin/pagent
build:
	@mkdir -p bin
	go build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD_PKG)

## install: install pagent into GOBIN / GOPATH/bin
install:
	go install $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" $(CMD_PKG)

## test: run the full unit test suite
test:
	go test $(GOFLAGS) ./...

## vet: run go vet over every package
vet:
	go vet $(GOFLAGS) ./...

## fmt: format all Go sources
fmt:
	gofmt -s -w .

## fmt-check: fail when sources are not gofmt-clean
fmt-check:
	@out="$$(gofmt -s -l .)"; \
	if [ -n "$$out" ]; then echo "gofmt needed for:"; echo "$$out"; exit 1; fi

## lint: run golangci-lint when available, otherwise fall back to go vet
lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not found, falling back to 'go vet ./...'"; \
		go vet $(GOFLAGS) ./...; \
	fi

## integration-test: build the binary and run environment-gated integration checks
integration-test:
	./scripts/integration-test.sh

## tidy: sync go.mod / go.sum
tidy:
	go mod tidy

## clean: remove build output
clean:
	rm -rf bin

## help: list available targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
