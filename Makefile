# agentgateway Educates operator
#
# Conventions follow the Educates v4 installer operator: no config/ kustomize
# tree (controller-gen writes CRDs and RBAC straight into the Helm chart), and
# CI is a single `make ci-operator` target so local and CI cannot drift.

SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

IMG ?= ghcr.io/educates/agentgateway-educates-operator:latest
CONTAINER_TOOL ?= docker
YEAR ?= $(shell date +%Y)

CHART_DIR ?= charts/agentgateway-educates-operator
CRD_OUTPUT_DIR ?= $(CHART_DIR)/crds
RBAC_OUTPUT_DIR ?= $(CHART_DIR)/templates/rbac

LOCALBIN ?= $(shell pwd)/bin
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT ?= $(LOCALBIN)/golangci-lint

## Tool versions
CONTROLLER_TOOLS_VERSION ?= v0.20.1
GOLANGCI_LINT_VERSION ?= v2.11.4

# gomodver resolves a dependency's version from go.mod, honouring any replace
# directive. Deriving the envtest versions from go.mod rather than hardcoding
# them means a dependency bump cannot silently leave tests on an old API server.
define gomodver
$(shell go list -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' $(1) 2>/dev/null)
endef

ENVTEST_VERSION ?= $(shell v='$(call gomodver,sigs.k8s.io/controller-runtime)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_VERSION manually (controller-runtime replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?([0-9]+)\.([0-9]+).*/release-\1.\2/')

ENVTEST_K8S_VERSION ?= $(shell v='$(call gomodver,k8s.io/api)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_K8S_VERSION manually (k8s.io/api replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?[0-9]+\.([0-9]+).*/1.\1/')

##@ General

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-24s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate CRDs and RBAC into the Helm chart.
	@mkdir -p "$(CRD_OUTPUT_DIR)" "$(RBAC_OUTPUT_DIR)"
	"$(CONTROLLER_GEN)" \
		crd paths="./..." output:crd:artifacts:config="$(CRD_OUTPUT_DIR)" \
		rbac:roleName=agentgateway-operator-manager output:rbac:artifacts:config="$(RBAC_OUTPUT_DIR)"

.PHONY: generate
generate: controller-gen ## Generate DeepCopy implementations.
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt",year=$(YEAR) paths="./..."

.PHONY: fmt
fmt: ## Run go fmt.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet setup-envtest ## Run tests (envtest, excluding e2e).
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" \
		go test $$(go list ./... | grep -v /test/e2e) -coverprofile cover.out

.PHONY: test-e2e
test-e2e: ## Run the kind-based e2e test. Needs a cluster; not part of CI.
	@# envtest runs no garbage collector, so cascading deletion — the property
	@# ADR-0002 rests on — can only be exercised against a real cluster.
	go test ./test/e2e/... -v -timeout 20m

.PHONY: lint
lint: golangci-lint ## Run golangci-lint.
	"$(GOLANGCI_LINT)" run

.PHONY: build
build: manifests generate fmt vet ## Build the manager binary.
	go build -o bin/manager ./cmd

.PHONY: run
run: manifests generate fmt vet ## Run the operator against the current kubeconfig.
	go run ./cmd

##@ Vendored charts

# agentgateway's charts are embedded in the operator image (ADR-0005). Upgrading
# agentgateway means upgrading this operator, so these targets exist to refresh
# the tarballs deliberately rather than on every build.
#
# Note: ADR-0005 names cr.agentgateway.dev as the source, but that registry
# denies anonymous pulls. ghcr.io/agentgateway/charts serves the same charts.
CHART_REGISTRY ?= oci://ghcr.io/agentgateway/charts
AGENTGATEWAY_CHART_VERSION ?= 1.5.0

.PHONY: vendor-charts
vendor-charts: ## Re-pull the agentgateway charts and refresh SHA256SUMS.
	@set -eu; \
	tmp="$$(mktemp -d)"; \
	trap 'rm -rf "$$tmp"' EXIT; \
	for chart in agentgateway agentgateway-crds; do \
		echo "Pulling $$chart $(AGENTGATEWAY_CHART_VERSION)"; \
		helm pull "$(CHART_REGISTRY)/$$chart" --version "$(AGENTGATEWAY_CHART_VERSION)" -d "$$tmp" >/dev/null; \
	done; \
	mv "$$tmp"/*.tgz vendored-charts/; \
	cd vendored-charts && shasum -a 256 *.tgz > SHA256SUMS; \
	echo "Refreshed vendored-charts/SHA256SUMS — review the diff before committing."

.PHONY: verify-vendored-charts
verify-vendored-charts: ## Fail if a vendored tarball does not match its recorded checksum.
	@# A tarball whose checksum does not match is refused rather than installed:
	@# the operator is only tested against the charts it embeds.
	@cd vendored-charts && shasum -a 256 -c SHA256SUMS

##@ Build

.PHONY: docker-build
docker-build: ## Build the operator image.
	$(CONTAINER_TOOL) build -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push the operator image.
	$(CONTAINER_TOOL) push ${IMG}

##@ CI

.PHONY: ci-operator
ci-operator: ## Everything CI runs, in one step so local and CI cannot drift.
	$(MAKE) verify-vendored-charts
	go vet ./...
	go build ./...
	$(MAKE) manifests
	@git diff --exit-code -- $(CRD_OUTPUT_DIR) $(RBAC_OUTPUT_DIR) \
		|| { echo "ERROR: generated CRDs/RBAC drifted. Run 'make manifests' and commit."; exit 1; }
	$(MAKE) generate
	@git diff --exit-code -- api \
		|| { echo "ERROR: generated DeepCopy drifted. Run 'make generate' and commit."; exit 1; }
	$(MAKE) test
	$(MAKE) lint

##@ Tools

$(LOCALBIN):
	mkdir -p $(LOCALBIN)

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Install controller-gen.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: setup-envtest
setup-envtest: $(ENVTEST) ## Install setup-envtest and the API server binaries.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Install golangci-lint.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

# go-install-tool installs a tool into bin/ under a version-suffixed name and
# symlinks it, so a version bump reinstalls but an unchanged version does not.
define go-install-tool
@[ -f "$(1)-$(3)" ] && [ "$$(readlink -- "$(1)" 2>/dev/null)" = "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f "$(1)" ;\
GOBIN="$(LOCALBIN)" go install $${package} ;\
mv "$(LOCALBIN)/$$(basename "$(1)")" "$(1)-$(3)" ;\
} ;\
ln -sf "$$(realpath "$(1)-$(3)")" "$(1)"
endef
