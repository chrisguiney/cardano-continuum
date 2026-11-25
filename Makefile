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

.PHONY: help build test test-race vet fmt-check lint check check-manifests manifests generate determinism models models-install

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

manifests: ## Regenerate the CRD YAML and the operator ClusterRole from markers
	$(CONTROLLER_GEN) crd paths="./api/..." output:crd:artifacts:config=config/crd/bases
	$(CONTROLLER_GEN) rbac:roleName=continuum-operator paths="./internal/operator/..." output:rbac:artifacts:config=config/rbac

generate: ## Regenerate DeepCopy methods under api/
	$(CONTROLLER_GEN) object paths="./api/..."

# Drift check: regenerating must leave api/ and config/ exactly as committed.
# Untracked generated files also fail, since git diff does not list them.
check-manifests: ## Fail if regenerating api/ or config/ changes anything
	$(MAKE) manifests generate
	@git diff --exit-code -- api config || { echo "generated files drift: run make manifests generate and commit"; exit 1; }
	@untracked=$$(git ls-files --others --exclude-standard -- api config); if [ -n "$$untracked" ]; then echo "$$untracked"; echo "untracked generated files: commit them"; exit 1; fi

# ko and kind run at pinned versions and are never added to go.mod, so a
# developer needs only Go and Docker.
KO_VERSION := v0.19.1
KO := go run github.com/google/ko@$(KO_VERSION)
KIND_VERSION := v0.33.0
KIND := go run sigs.k8s.io/kind@$(KIND_VERSION)

# Developer registry. CONTINUUM_REGISTRY is host[:port][/path]; there is no
# default, so a push without it fails instead of reaching a public registry.
# CONTINUUM_REGISTRY_INSECURE=true marks it plain HTTP and adds ko's
# --insecure-registry; image-dingo also checks the Docker daemon lists it.
CONTINUUM_REGISTRY ?=
CONTINUUM_REGISTRY_INSECURE ?=
DINGO_SRC ?=
KO_INSECURE := $(if $(filter true,$(CONTINUUM_REGISTRY_INSECURE)),--insecure-registry)
REGISTRY_HOST = $(firstword $(subst /, ,$(CONTINUUM_REGISTRY)))

.PHONY: require-registry ko-images image-dingo
require-registry:
	@test -n "$(CONTINUUM_REGISTRY)" || { echo "CONTINUUM_REGISTRY is not set"; exit 1; }

ko-images: require-registry ## Build and push continuum binaries with ko to CONTINUUM_REGISTRY
	KO_DOCKER_REPO=$(CONTINUUM_REGISTRY) $(KO) build $(KO_INSECURE) ./cmd/...

image-dingo: require-registry ## Build DINGO_SRC with its Dockerfile and push to CONTINUUM_REGISTRY
	@test -n "$(DINGO_SRC)" || { echo "DINGO_SRC is not set"; exit 1; }
	@if [ -n "$(KO_INSECURE)" ]; then docker info | grep -q '$(REGISTRY_HOST)' || { echo "add $(REGISTRY_HOST) to insecure-registries in the Docker daemon config"; exit 1; }; fi
	rev=$$(git -C $(DINGO_SRC) rev-parse HEAD); tag=dev-$$(echo $$rev | cut -c1-7); \
	docker build --build-arg COMMIT_HASH=$$rev \
	  --label org.opencontainers.image.revision=$$rev \
	  --label $(LABEL_PREFIX)/build-mode=repro \
	  -t $(CONTINUUM_REGISTRY)/dingo:$$tag $(DINGO_SRC) && docker push $(CONTINUUM_REGISTRY)/dingo:$$tag

# The determinism tests: once without the race detector (the crash sweep then
# kills the engine at every step), then under it at one and eight procs, where
# a goroutine leaking into the loop shows up as a difference. They include two
# same-seed CLI fake runs whose logs must be byte-identical, and a replay.
DETERMINISM_TESTS := Determinism|TestHandleIsPure|TestCrashInjectionConverges|TestRestartAfterOfflineRollback|TestIntersectionHandledAsRollback
determinism: ## Run the determinism tests: same seed, same log; replay; crash recovery
	go test -tags debug -count=1 -run '$(DETERMINISM_TESTS)' ./internal/engine ./cmd/continuum
	GOMAXPROCS=1 go test -race -tags debug -count=1 -run '$(DETERMINISM_TESTS)' ./internal/engine ./cmd/continuum
	GOMAXPROCS=8 go test -race -tags debug -count=1 -run '$(DETERMINISM_TESTS)' ./internal/engine ./cmd/continuum

# envtest runs a real kube-apiserver and etcd. setup-envtest is pinned and
# installs, with the control-plane binaries it downloads, under .tools/.
SETUP_ENVTEST_VERSION := v0.25.3
ENVTEST_K8S_VERSION   := 1.37.0
TOOLS_BIN             := $(CURDIR)/.tools/bin
SETUP_ENVTEST         := $(TOOLS_BIN)/setup-envtest

.PHONY: test-envtest
$(SETUP_ENVTEST):
	GOBIN=$(TOOLS_BIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(SETUP_ENVTEST_VERSION)

test-envtest: $(SETUP_ENVTEST) ## Run the operator tests against envtest's API server
	KUBEBUILDER_ASSETS="$$($(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(CURDIR)/.tools/envtest -p path)" \
		go test -race -tags 'debug envtest' ./internal/operator/...

# Stubs for targets that need components landing in later steps. Each prints
# "not yet implemented" and exits 0 so that the target names stay reserved.
STUBS := web images ident vectors harvest \
	fuzz-smoke bisect tools
.PHONY: $(STUBS)
$(STUBS): ; @echo "$@: not yet implemented"

# Models live in models/. The TLA+ tools install into models/.tools (gitignored).
models-install: ## Install the pinned Java and TLA+ tools into models/.tools
	models/scripts/install-tla.sh

models: ## Check every TLA+ model directory under models/specs
	@set -e; cd models; for d in specs/*/; do scripts/run-model.sh "$${d%/}"; done

# kind e2e. kind-up starts a throwaway plain-HTTP registry next to the cluster;
# e2e points CONTINUUM_REGISTRY at it. kind-down removes both.
E2E_PORT ?= 5001
E2E_TIMEOUT ?= 50m

.PHONY: kind-up kind-down e2e
kind-up: ## Start the kind cluster and its plain-HTTP registry (names continuum-e2e*)
	KIND="$(KIND)" CONTINUUM_E2E_PORT=$(E2E_PORT) hack/kind-up.sh

kind-down: ## Remove the kind cluster and the registry
	KIND="$(KIND)" hack/kind-down.sh

e2e: kind-up ## Run the kind e2e suite (about 25 minutes; go test -short skips the slowest)
	CONTINUUM_REGISTRY=localhost:$(E2E_PORT)/continuum CONTINUUM_REGISTRY_INSECURE=true \
	CONTINUUM_KO="$(KO)" go test -tags e2e -count=1 -timeout $(E2E_TIMEOUT) -v ./test/e2e/...
