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

.PHONY: web web-test lab lab-image lab-test profiles-index help build test test-race vet fmt-check lint check check-static check-manifests manifests generate determinism models models-install harvest vectors scenarios

help: ## Show this list
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  make %-16s %s\n", $$1, $$2}'

build: ## Compile every package (binaries arrive with their components)
	go build ./...

test: ## Run unit tests with the debug build tag
	go test -tags debug ./...

# SHARD runs one slice of the tests, as a CI test job does. The slices
# partition ./...: internal/engine, the slowest package, is split in two by
# test name (-run and -skip of one pattern), and rest is the complement of
# the other package lists, so the CI matrix runs every test exactly once.
# Without SHARD, test-race runs them all.
SHARD ?=
ENGINE_SPLIT := '^Test[A-G]'
SHARD_engine-a := -run $(ENGINE_SPLIT) ./internal/engine
SHARD_engine-b := -skip $(ENGINE_SPLIT) ./internal/engine
SHARD_engine-internal = $(shell go list ./internal/engine/... | grep -v '/internal/engine$$')
SHARD_cli := ./internal/cli/...
SHARD_rest = $(shell go list ./... | grep -v -e '/internal/engine$$' -e '/internal/engine/' -e '/internal/cli/')
TEST_ARGS = $(if $(SHARD),$(or $(SHARD_$(SHARD)),$(error unknown SHARD $(SHARD): use engine-a, engine-b, engine-internal, cli or rest)),./...)

# go test's default 10m limit per package binary is below what
# internal/engine needs under the race detector on a 4-vCPU runner.
TEST_TIMEOUT ?= 20m

test-race: ## Run unit tests with the race detector and the debug build tag (SHARD=... for one CI slice)
	go test -race -tags debug -timeout $(TEST_TIMEOUT) $(TEST_ARGS)

vet: ## Run go vet
	go vet ./...

fmt-check: ## Fail if any Go file is not gofmt-formatted
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "$$out"; exit 1; fi

lint: ## Run golangci-lint (CI installs $(GOLANGCI_LINT_VERSION))
	golangci-lint run ./...

# The built app in web/dist is committed so `make check` and `go build` need
# no Node toolchain. Run this after changing anything under web/src and
# commit the result.
web: ## Rebuild the web UI into web/dist (needs Node and npm)
	cd web && npm ci && npm run build

web-test: ## Typecheck and run the web UI's unit tests (needs Node and npm)
	cd web && npm ci && npm run typecheck && npm test

check-static: build vet fmt-check lint check-manifests ## Everything CI's static job runs

check: check-static test-race ## Everything CI runs

# controller-gen writes config/; the chart carries copies, since Helm reads
# only files inside the chart.
CHART := charts/continuum
CHART_GENERATED := $(CHART)/crds $(CHART)/files

manifests: ## Regenerate the CRD YAML and the operator ClusterRole from markers, and copy them into the chart
	$(CONTROLLER_GEN) crd paths="./api/..." output:crd:artifacts:config=config/crd/bases
	$(CONTROLLER_GEN) rbac:roleName=continuum-operator paths="./internal/operator/..." output:rbac:artifacts:config=config/rbac
	cp config/crd/bases/*.yaml $(CHART)/crds/
	cp config/rbac/role.yaml $(CHART)/files/operator-role.yaml

generate: ## Regenerate DeepCopy methods under api/
	$(CONTROLLER_GEN) object paths="./api/..."

# Drift check: regenerating must leave api/, config/ and the chart's copies
# exactly as committed. Untracked generated files also fail, since git diff
# does not list them.
check-manifests: ## Fail if regenerating api/, config/ or the chart's CRDs and role changes anything
	$(MAKE) manifests generate
	@git diff --exit-code -- api config $(CHART_GENERATED) || { echo "generated files drift: run make manifests generate and commit"; exit 1; }
	@untracked=$$(git ls-files --others --exclude-standard -- api config $(CHART_GENERATED)); if [ -n "$$untracked" ]; then echo "$$untracked"; echo "untracked generated files: commit them"; exit 1; fi

# helm runs at a pinned version and is never added to go.mod.
HELM_VERSION := v4.3.2
HELM := go run helm.sh/helm/v4/cmd/helm@$(HELM_VERSION)

# chart-check lints and renders the chart with its defaults and with
# ci/nondefault-values.yaml, and requires the engine to dial the Service the
# operator names pool 0 (render.Name, which the chart recomputes).
.PHONY: chart-check
chart-check: ## Lint and render the Helm chart (default and non-default values)
	$(HELM) lint $(CHART)
	$(HELM) lint $(CHART) -f $(CHART)/ci/nondefault-values.yaml
	$(HELM) template continuum $(CHART) -n chart-check -f $(CHART)/ci/nondefault-values.yaml >/dev/null
	@node=$$(go run ./hack/devup node chart-check) && $(HELM) template continuum $(CHART) -n chart-check | grep -qF -- "--node=$$node" || { echo "the chart's engine does not dial pool 0's bridge ($$node)"; exit 1; }

# ko runs at a pinned version and is never added to go.mod, so a developer
# needs only Go and Docker.
KO_VERSION := v0.19.1
KO := go run github.com/google/ko@$(KO_VERSION)

# Developer registry. CONTINUUM_REGISTRY is host[:port][/path]; there is no
# default, so a push without it fails instead of reaching a public registry.
# CONTINUUM_REGISTRY_INSECURE=true marks it plain HTTP and adds ko's
# --insecure-registry; image-dingo also checks the Docker daemon lists it.
CONTINUUM_REGISTRY ?=
CONTINUUM_REGISTRY_INSECURE ?=
DINGO_SRC ?=
GOUROBOROS_SRC ?=
KO_INSECURE := $(if $(filter true,$(CONTINUUM_REGISTRY_INSECURE)),--insecure-registry)
REGISTRY_HOST = $(firstword $(subst /, ,$(CONTINUUM_REGISTRY)))

.PHONY: require-registry ko-images image-dingo image-mithril2pq
require-registry:
	@test -n "$(CONTINUUM_REGISTRY)" || { echo "CONTINUUM_REGISTRY is not set"; exit 1; }

ko-images: require-registry ## Build and push continuum binaries with ko to CONTINUUM_REGISTRY
	KO_DOCKER_REPO=$(CONTINUUM_REGISTRY) $(KO) build $(KO_INSECURE) ./cmd/...

# WITH is a space-separated list of module=path|sha overlays (gouroboros,
# plutigo, ouroboros-mock). MODE is fast (host build layered over upstream
# main, uncommitted changes allowed) or repro (committed refs, dingo's own
# Dockerfile); it defaults to fast with overlays and repro without.
# REF builds a dingo commit in repro mode; FORK is a space-separated list of
# module=gomodule forks. The digest reference is printed on success.
WITH ?=
FORK ?=
REF ?=
MODE ?= $(if $(strip $(WITH)),fast,repro)
image-dingo: require-registry ## Build DINGO_SRC (WITH=mod=path ... MODE=fast|repro) and push to CONTINUUM_REGISTRY
	@test -n "$(DINGO_SRC)" || { echo "DINGO_SRC is not set"; exit 1; }
	@if [ -n "$(KO_INSECURE)" ]; then docker info | grep -q '$(REGISTRY_HOST)' || { echo "add $(REGISTRY_HOST) to insecure-registries in the Docker daemon config"; exit 1; }; fi
	go run ./cmd/continuum build --dingo '$(DINGO_SRC)' --mode '$(MODE)' $(if $(REF),--ref '$(REF)') \
	  $(foreach w,$(WITH),--with '$(w)') $(foreach f,$(FORK),--fork '$(f)')

# MITHRIL2PQ_SRC is a clean mithril2pq checkout at the commit to pin (7af010a
# or later, which has -watch). The distroless mithril2pq target has no shell,
# which the sidecar does not need. The image is tagged with the short commit.
MITHRIL2PQ_SRC ?=
image-mithril2pq: require-registry ## Build MITHRIL2PQ_SRC's mithril2pq target and push it to CONTINUUM_REGISTRY
	@test -n "$(MITHRIL2PQ_SRC)" || { echo "MITHRIL2PQ_SRC is not set"; exit 1; }
	@if [ -n "$(KO_INSECURE)" ]; then docker info | grep -q '$(REGISTRY_HOST)' || { echo "add $(REGISTRY_HOST) to insecure-registries in the Docker daemon config"; exit 1; }; fi
	@git -C '$(MITHRIL2PQ_SRC)' diff --quiet HEAD || { echo "MITHRIL2PQ_SRC has uncommitted changes"; exit 1; }
	@ref=$(CONTINUUM_REGISTRY)/mithril2pq:$$(git -C '$(MITHRIL2PQ_SRC)' rev-parse --short=7 HEAD); \
	docker build --target mithril2pq -t $$ref '$(MITHRIL2PQ_SRC)' && docker push $$ref && echo $$ref

# The determinism tests: once without the race detector (the crash sweep then
# kills the engine at every step), then under it at one and eight procs, where
# a goroutine leaking into the loop shows up as a difference. They include two
# same-seed CLI fake runs whose logs must be byte-identical, and a replay.
DETERMINISM_TESTS := Determinism|TestHandleIsPure|TestCrashInjectionConverges|TestRestartAfterOfflineRollback|TestIntersectionHandledAsRollback
determinism: ## Run the determinism tests: same seed, same log; replay; crash recovery
	go test -tags debug -count=1 -run '$(DETERMINISM_TESTS)' ./internal/engine ./internal/cli/sim ./internal/cli/attack
	GOMAXPROCS=1 go test -race -tags debug -count=1 -run '$(DETERMINISM_TESTS)' ./internal/engine ./internal/cli/sim ./internal/cli/attack
	GOMAXPROCS=8 go test -race -tags debug -count=1 -run '$(DETERMINISM_TESTS)' ./internal/engine ./internal/cli/sim ./internal/cli/attack

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
STUBS := ident \
	fuzz-smoke bisect tools
.PHONY: $(STUBS)
$(STUBS): ; @echo "$@: not yet implemented"

# Models live in models/. The TLA+ tools install into models/.tools (gitignored).
models-install: ## Install the pinned Java and TLA+ tools into models/.tools
	models/scripts/install-tla.sh

models: ## Check every TLA+ model directory under models/specs
	@set -e; cd models; for d in specs/*/; do scripts/run-model.sh "$${d%/}"; done

# TLA+ harvester. TLA_REPO and TLA_REF name the model repository and the commit
# to pin; the clone lives in the user cache directory, never in this checkout.
# Both need the 1.8.0 jar that models-install fetches.
TLA_REPO ?=
TLA_REF ?=
HARVEST_OUT ?= scenarios/seeds
VECTORS_OUT ?= scenarios/vectors

harvest: ## Run TLC on every *Bug.cfg of TLA_REPO at TLA_REF; write seeds to HARVEST_OUT
	@test -n "$(TLA_REPO)" -a -n "$(TLA_REF)" || { echo "set TLA_REPO and TLA_REF"; exit 2; }
	go run ./cmd/continuum harvest --repo "$(TLA_REPO)" --ref "$(TLA_REF)" --out "$(HARVEST_OUT)"

vectors: ## Export the Utxo.tla boundary-transaction table of TLA_REPO at TLA_REF to VECTORS_OUT
	@test -n "$(TLA_REPO)" -a -n "$(TLA_REF)" || { echo "set TLA_REPO and TLA_REF"; exit 2; }
	go run ./cmd/continuum vectors --repo "$(TLA_REPO)" --ref "$(TLA_REF)" --out "$(VECTORS_OUT)"

SCENARIOS_OUT ?= scenarios/generated
WALKS ?= 20
scenarios: ## Regenerate the scenario library from TLA_REPO at TLA_REF (Mode A traces, the codec table, WALKS Mode B walks)
	@test -n "$(TLA_REPO)" -a -n "$(TLA_REF)" || { echo "set TLA_REPO and TLA_REF"; exit 2; }
	go run ./cmd/continuum scenario harvest --repo "$(TLA_REPO)" --ref "$(TLA_REF)" --out "$(SCENARIOS_OUT)" --walks $(WALKS)

# e2e runs against an existing cluster of any distribution. E2E_CONTEXT
# names its kubectl context and is required: the suite never falls back to
# the current context. Each test installs the chart into its own
# continuum-e2e-* namespace and removes it. CONTINUUM_REGISTRY must be a
# registry the cluster pulls from; E2E_CLUSTER_REGISTRY is the same registry
# as the nodes name it, when that differs.
E2E_CONTEXT ?=
E2E_CLUSTER_REGISTRY ?=
E2E_TIMEOUT ?= 100m

.PHONY: e2e
e2e: require-registry ## Run the e2e suite against E2E_CONTEXT (about an hour; go test -short skips the slowest)
	@test -n "$(E2E_CONTEXT)" || { echo "E2E_CONTEXT is not set: name the kubectl context of the cluster to test"; exit 1; }
	E2E_CONTEXT='$(E2E_CONTEXT)' CONTINUUM_E2E_CLUSTER_REGISTRY='$(E2E_CLUSTER_REGISTRY)' \
	CONTINUUM_REGISTRY='$(CONTINUUM_REGISTRY)' CONTINUUM_REGISTRY_INSECURE='$(CONTINUUM_REGISTRY_INSECURE)' CONTINUUM_PLATFORM='$(PLATFORM)' \
	CONTINUUM_KO="$(KO)" CONTINUUM_HELM="$(HELM)" CONTINUUM_DINGO_SRC="$(DINGO_SRC)" CONTINUUM_GOUROBOROS_SRC="$(GOUROBOROS_SRC)" \
	CONTINUUM_OVERLAY_MODE="$(MODE)" go test -tags e2e -count=1 -timeout $(E2E_TIMEOUT) -v ./test/e2e/...

# make images builds the images the chart runs and pushes them to
# CONTINUUM_REGISTRY, which the cluster must be able to pull from. The digest
# references land in IMAGES_OUT for make up. PLATFORM is the cluster nodes'
# platform: set it when they differ from this machine.
PLATFORM ?= linux/$(shell go env GOARCH)
IMAGES_OUT ?= $(CURDIR)/.tools/images
CHART_IMAGES := ./cmd/continuum ./cmd/continuum-operator ./cmd/continuum-genesis

images: require-registry ## Build the chart's images with ko, push them to CONTINUUM_REGISTRY and record their digests
	@mkdir -p $(dir $(IMAGES_OUT))
	KO_DOCKER_REPO=$(CONTINUUM_REGISTRY) $(KO) build --base-import-paths --platform=$(PLATFORM) $(KO_INSECURE) $(CHART_IMAGES) >$(IMAGES_OUT).new
	@mv $(IMAGES_OUT).new $(IMAGES_OUT) && cat $(IMAGES_OUT)

# make up / make down: install the chart into NAMESPACE of the cluster
# CONTEXT names, and remove it. CONTEXT empty is kubectl's current context;
# it is never switched. See docs/single-machine.md for sizing.
CONTEXT ?=
NAMESPACE ?= continuum
RELEASE ?= continuum
PROFILES ?= preprod
POOLS ?= 3
DINGO_POOLS ?= 1
WALLETS ?= 100
SCALE ?= 200
SEED ?= 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
UI_PORT ?= 8088
UI_ADDRESS ?= 127.0.0.1
ENGINE_POOLS ?= 0
# ATTACKS: empty runs every tier-1 attack a stock genesis supports, "none"
# runs none (hack/up.sh).
ATTACKS ?=
# KEEP_RUN=1 makes `make up` refuse, rather than reset, a deploy whose
# engine cannot resume its run.
KEEP_RUN ?=
# VALUES is an extra chart values file, applied after the variables above.
VALUES ?=
UP_ENV = CONTEXT='$(CONTEXT)' NAMESPACE='$(NAMESPACE)' RELEASE='$(RELEASE)' PROFILES='$(PROFILES)' POOLS='$(POOLS)' \
	DINGO_POOLS='$(DINGO_POOLS)' WALLETS='$(WALLETS)' SCALE='$(SCALE)' SEED='$(SEED)' UI_PORT='$(UI_PORT)' UI_ADDRESS='$(UI_ADDRESS)' \
	ENGINE_POOLS='$(ENGINE_POOLS)' ATTACKS='$(ATTACKS)' KEEP_RUN='$(KEEP_RUN)' VALUES='$(VALUES)' \
	CONTINUUM_REGISTRY='$(CONTINUUM_REGISTRY)' IMAGES='$(IMAGES_OUT)' HELM='$(HELM)'

.PHONY: images up down
up: images ## Build and push the images, then install the chart into CONTEXT/NAMESPACE (POOLS, DINGO_POOLS, WALLETS, SCALE, PROFILES, SEED, ATTACKS)
	$(UP_ENV) hack/up.sh

down: ## Delete the pools, uninstall the chart and delete NAMESPACE
	$(UP_ENV) hack/down.sh

# continuum-lab: calibration notebooks. The image layers scipy and pytest over
# the mithril2pq notebook image (LAB_BASE).
LAB_IMAGE ?= continuum-lab:local
LAB_BASE ?= mithril2pq-notebook:local
LAB_DATA ?= $(CURDIR)/lab/tests/fixtures

lab-image: ## Build the lab image on top of the mithril2pq notebook image
	docker build -f lab/Dockerfile --build-arg BASE=$(LAB_BASE) -t $(LAB_IMAGE) .

profiles-index: ## Rewrite profiles/index.json (profile hash to target) after refitting
	go run ./cmd/continuum profiles index --out profiles/index.json

lab: lab-image ## Run JupyterLab on :8888 with LAB_DATA (chain and export subdirs) mounted read-only
	docker run --rm -p 8888:8888 -v $(LAB_DATA):/data:ro -v $(CURDIR)/lab:/work/lab $(LAB_IMAGE)

lab-test: lab-image ## Run the lab pytest suite in the image, then the Go test that parses the fitted profile
	docker run --rm -v $(CURDIR)/lab:/work/lab -v $(CURDIR)/profiles:/work/profiles:ro -e PYTHONDONTWRITEBYTECODE=1 --entrypoint python $(LAB_IMAGE) -m pytest -q -p no:cacheprovider /work/lab/tests
	go test ./lab/
