.DEFAULT_GOAL := help
SHELL := bash

# Organisation identifiers. These mirror internal/ident, which is the Go source
# of truth; internal/ident's test fails if the two drift apart.
MODULE       := github.com/chrisguiney/cardano-continuum
IMAGE_REPO   := ghcr.io/chrisguiney
API_GROUP    := continuum.blinklabs.io
LABEL_PREFIX := continuum.blinklabs.io
METRICS_NS   := continuum

GOLANGCI_LINT_VERSION := v2.15.0

.PHONY: help build test test-race vet fmt-check lint check

help: ## Show this list
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  make %-16s %s\n", $$1, $$2}'

build: ## Compile every package (binaries arrive with their components)
	go build ./...

test: ## Run unit tests with the debug build tag
	go test -tags debug ./...

test-race: ## Run unit tests with the race detector and the debug build tag
	go test -race -tags debug ./...

vet: ## Run go vet
	go vet ./...

fmt-check: ## Fail if any Go file is not gofmt-formatted
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "$$out"; exit 1; fi

lint: ## Run golangci-lint (CI installs $(GOLANGCI_LINT_VERSION))
	golangci-lint run ./...

check: build vet fmt-check test-race lint ## Everything CI runs

# Stubs for targets that need components landing in later steps. Each prints
# "not yet implemented" and exits 0 so that the target names stay reserved.
STUBS := test-envtest e2e manifests generate web images ident vectors harvest \
	fuzz-smoke determinism kind-up kind-down image-dingo ko-images bisect tools
.PHONY: $(STUBS)
$(STUBS): ; @echo "$@: not yet implemented"
