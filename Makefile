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

# controller-gen runs at a pinned version and is never added to go.mod.
CONTROLLER_GEN_VERSION := v0.23.0
CONTROLLER_GEN := go run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

.PHONY: help build test test-race vet fmt-check lint check check-manifests manifests generate models models-install

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

check: build vet fmt-check test-race lint check-manifests ## Everything CI runs

manifests: ## Regenerate the CRD YAML under config/crd/bases from api/ markers
	$(CONTROLLER_GEN) crd paths="./api/..." output:crd:artifacts:config=config/crd/bases

generate: ## Regenerate DeepCopy methods under api/
	$(CONTROLLER_GEN) object paths="./api/..."

# Drift check: regenerating must leave api/ and config/ exactly as committed.
# Untracked generated files also fail, since git diff does not list them.
check-manifests: ## Fail if regenerating api/ or config/ changes anything
	$(MAKE) manifests generate
	@git diff --exit-code -- api config || { echo "generated files drift: run make manifests generate and commit"; exit 1; }
	@untracked=$$(git ls-files --others --exclude-standard -- api config); if [ -n "$$untracked" ]; then echo "$$untracked"; echo "untracked generated files: commit them"; exit 1; fi

# Stubs for targets that need components landing in later steps. Each prints
# "not yet implemented" and exits 0 so that the target names stay reserved.
STUBS := test-envtest e2e web images ident vectors harvest \
	fuzz-smoke determinism kind-up kind-down image-dingo ko-images bisect tools
.PHONY: $(STUBS)
$(STUBS): ; @echo "$@: not yet implemented"

# Models live in models/. The TLA+ tools install into models/.tools (gitignored).
models-install: ## Install the pinned Java and TLA+ tools into models/.tools
	models/scripts/install-tla.sh

models: ## Check every TLA+ model directory under models/specs
	@set -e; cd models; for d in specs/*/; do scripts/run-model.sh "$${d%/}"; done
