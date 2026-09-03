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

# Target platforms for the published image. The operator is deployed on both
# amd64 and arm64 clusters, so releases are manifest lists covering both.
PLATFORMS ?= linux/amd64,linux/arm64

# Gateway API CRDs shipped by the chart (ADR-0006). Tracks the
# sigs.k8s.io/gateway-api version in go.mod: the schemas the chart installs and
# the types the operator compiles against must not drift apart.
GATEWAY_API_VERSION ?= $(shell v='$(call gomodver,sigs.k8s.io/gateway-api)'; \
  [ -n "$$v" ] || { echo "Set GATEWAY_API_VERSION manually (gateway-api replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v")
GATEWAY_API_CRD_KINDS ?= gatewayclasses gateways
GATEWAY_API_TEMPLATE ?= $(CHART_DIR)/templates/gateway-api-crds.yaml
GATEWAY_API_TESTDATA_DIR ?= internal/controller/testdata/crds/gateway-api

# Local Educates development. These targets drive a local cluster only; CI
# publishes the workshop with educates-github-actions instead.
WORKSHOP_DIR ?= sample-workshop
WORKSHOP_PORTAL ?= educates-cli
SAMPLE_CATALOG ?= $(WORKSHOP_DIR)/catalog/agentgatewaycatalog.yaml
LLM_CREDENTIAL_SECRET ?= agentgateway-provider-credentials
LLM_CREDENTIAL_NAMESPACE ?= agentgateway-system

# kind e2e. The operator teardown test needs a real API server with a garbage
# collector, which envtest does not have.
KIND_CLUSTER ?= agentgateway-e2e
KIND_IMG ?= agentgateway-educates-operator:e2e
E2E_BOOTSTRAP_DIR ?= test/e2e/bootstrap

CHART_DIR ?= charts/agentgateway-educates-operator
CRD_OUTPUT_DIR ?= $(CHART_DIR)/crds
RBAC_OUTPUT_DIR ?= $(CHART_DIR)/templates/rbac

LOCALBIN ?= $(shell pwd)/bin
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT ?= $(LOCALBIN)/golangci-lint

## Tool versions
CONTROLLER_TOOLS_VERSION ?= v0.20.1
GOLANGCI_LINT_VERSION ?= v2.13.2

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
test-e2e: ## Run the e2e test against the current cluster. See ci-e2e for the full path.
	@# envtest runs no garbage collector, so cascading deletion — the property
	@# ADR-0002 rests on — can only be exercised against a real cluster.
	@# Assumes an operator and a ready platform are already installed; `make
	@# ci-e2e` stands those up in kind first.
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

.PHONY: vendor-gateway-api-crds
vendor-gateway-api-crds: ## Re-pull the Gateway API CRDs into the chart and the envtest fixtures.
	@# Refreshes both consumers from one source so they cannot drift: the chart
	@# template that installs them in production (ADR-0006) and the envtest
	@# fixtures that stand in for them in tests. The version follows go.mod.
	@set -eu; \
	version="$(GATEWAY_API_VERSION)"; \
	echo "Vendoring Gateway API CRDs $$version (standard channel)"; \
	tmp="$$(mktemp -d)"; \
	trap 'rm -rf "$$tmp"' EXIT; \
	for kind in $(GATEWAY_API_CRD_KINDS); do \
		url="https://raw.githubusercontent.com/kubernetes-sigs/gateway-api/$$version/config/crd/standard/gateway.networking.k8s.io_$$kind.yaml"; \
		curl -sSLf -o "$$tmp/$$kind.yaml" "$$url" \
			|| { echo "ERROR: could not fetch $$url" >&2; exit 1; }; \
		grep -q "bundle-version: $$version" "$$tmp/$$kind.yaml" \
			|| { echo "ERROR: $$kind.yaml is not bundle-version $$version" >&2; exit 1; }; \
		cp "$$tmp/$$kind.yaml" "$(GATEWAY_API_TESTDATA_DIR)/gateway.networking.k8s.io_$$kind.yaml"; \
	done; \
	{ \
		printf '%s\n' '{{- if .Values.gatewayAPI.install }}'; \
		printf '%s\n' '{{- /*'; \
		printf '%s\n' 'Gateway API CRDs, shipped by this chart rather than applied from a reconcile'; \
		printf '%s\n' '(ADR-0006). They are a static, versioned artefact with no per-cluster'; \
		printf '%s\n' 'variation, and the platform reconciler gates on them being present.'; \
		printf '%s\n' ''; \
		printf '%s\n' 'These live in templates/ rather than crds/ because crds/ is unconditional:'; \
		printf '%s\n' 'Helm offers no way to make it honour gatewayAPI.install, and a cluster whose'; \
		printf '%s\n' 'ingress controller already owns Gateway API must be able to opt out.'; \
		printf '%s\n' ''; \
		printf '%s\n' "sigs.k8s.io/gateway-api $$version, standard channel, GatewayClass and Gateway"; \
		printf '%s\n' 'only — the two kinds this operator touches. Generated by'; \
		printf '%s\n' '`make vendor-gateway-api-crds`; edit that target, not this file.'; \
		printf '%s\n' ''; \
		printf '%s\n' 'helm.sh/resource-policy: keep means `helm uninstall` leaves them in place.'; \
		printf '%s\n' 'Removing Gateway API from under an ingress controller that started using it'; \
		printf '%s\n' 'would be a far worse failure than leaving two CRDs behind.'; \
		printf '%s\n' '*/}}'; \
		for kind in $(GATEWAY_API_CRD_KINDS); do \
			printf '%s\n' '---'; \
			awk '/^  annotations:$$/ && !done { print; print "    helm.sh/resource-policy: keep"; done=1; next } { print }' \
				"$$tmp/$$kind.yaml"; \
		done; \
		printf '%s\n' '{{- end }}'; \
	} > "$(GATEWAY_API_TEMPLATE)"; \
	echo "Refreshed $(GATEWAY_API_TEMPLATE) and $(GATEWAY_API_TESTDATA_DIR) — review the diff before committing."

##@ Build

.PHONY: docker-build
docker-build: ## Build the operator image for the local platform.
	@# Single-arch and loaded into the local daemon, which is what a kind e2e
	@# and an edit-build-test loop need. Releases use docker-buildx.
	$(CONTAINER_TOOL) build -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push the operator image.
	$(CONTAINER_TOOL) push ${IMG}

.PHONY: docker-buildx
docker-buildx: ## Build and push the multiarch operator image.
	@# Builds and pushes in one step because a manifest list cannot be loaded
	@# into the local daemon: `--load` rejects more than one platform. The
	@# Dockerfile pins its builder stage to $BUILDPLATFORM and cross-compiles
	@# with GOOS/GOARCH, so neither architecture runs under QEMU.
	$(CONTAINER_TOOL) buildx build --platform "$(PLATFORMS)" -t ${IMG} --push .

##@ Sample workshop (local development)

# These targets drive a local Educates cluster — `educates create-cluster` — and
# are for iterating on the sample workshop. CI publishes it with
# educates-github-actions instead, so nothing here runs in a pipeline.
#
# They need the educates CLI on PATH. It is not installable with go install, so
# unlike controller-gen it is not vendored into bin/.

.PHONY: require-educates
require-educates:
	@command -v educates >/dev/null 2>&1 || { \
		echo "ERROR: the 'educates' CLI is not on PATH."; \
		echo "       These targets drive a local Educates cluster."; \
		echo "       Install: https://docs.educates.dev/getting-started/installation"; \
		exit 1; \
	}

.PHONY: publish-workshop
publish-workshop: require-educates ## Publish the sample workshop to the local cluster registry.
	@# Publishes to localhost:5001, the registry `educates create-cluster`
	@# stands up. publish-workshop substitutes the image_repository variable
	@# client-side before pushing; deploy-workshop leaves it for the cluster to
	@# expand to registry.default.svc.cluster.local at session time.
	educates publish-workshop "$(WORKSHOP_DIR)"

.PHONY: deploy-workshop
deploy-workshop: require-educates ## Deploy the sample workshop, creating a training portal.
	@# deploy-workshop creates the TrainingPortal if it does not exist. It does
	@# not print the portal URL, so that is read back from the CR below.
	educates deploy-workshop --file "$(WORKSHOP_DIR)" --portal "$(WORKSHOP_PORTAL)"
	@echo
	@echo "Portal URL: $$(kubectl get trainingportal "$(WORKSHOP_PORTAL)" -o jsonpath='{.status.educates.url}' 2>/dev/null || echo '<not ready yet>')"
	@echo "Access code: run 'educates view-credentials --portal $(WORKSHOP_PORTAL)'"

.PHONY: undeploy-workshop
undeploy-workshop: require-educates ## Remove the sample workshop from the training portal.
	@# Removes the workshop entry only; the portal itself survives. Use
	@# `educates delete-portal` to remove that. Idempotent when already absent.
	educates delete-workshop --file "$(WORKSHOP_DIR)" --portal "$(WORKSHOP_PORTAL)"

.PHONY: deploy-sample-catalog
deploy-sample-catalog: ## Apply the sample model catalog, checking its credential exists.
	@# The catalog references a Secret holding a real provider API key. Nothing
	@# here creates it: a credential is the cluster operator's to supply.
	@kubectl get secret "$(LLM_CREDENTIAL_SECRET)" -n "$(LLM_CREDENTIAL_NAMESPACE)" >/dev/null 2>&1 || { \
		echo "ERROR: Secret $(LLM_CREDENTIAL_SECRET) not found in namespace $(LLM_CREDENTIAL_NAMESPACE)."; \
		echo "       The catalog needs a provider API key. Create one with:"; \
		echo; \
		echo "       kubectl create secret generic $(LLM_CREDENTIAL_SECRET) \\"; \
		echo "         --namespace $(LLM_CREDENTIAL_NAMESPACE) \\"; \
		echo "         --from-literal=api-key=sk-ant-..."; \
		echo; \
		exit 1; \
	}
	kubectl apply -f "$(SAMPLE_CATALOG)"

##@ kind end-to-end

# The teardown test asserts on cascading deletion, which needs a real garbage
# collector. envtest has none, so this is the only path that can run it.
#
# Plain kind rather than `educates create-cluster`: the test needs the operator
# and a ready platform, not a training platform. Standing up all of Educates to
# assert on garbage collection would cost minutes for no extra coverage.

.PHONY: kind-create
kind-create: ## Create the kind cluster for the e2e test.
	@kind get clusters 2>/dev/null | grep -qx "$(KIND_CLUSTER)" \
		|| kind create cluster --name "$(KIND_CLUSTER)"

.PHONY: kind-load
kind-load: ## Build the operator image and load it into kind.
	$(MAKE) docker-build IMG="$(KIND_IMG)"
	kind load docker-image "$(KIND_IMG)" --name "$(KIND_CLUSTER)"

.PHONY: kind-deploy
kind-deploy: ## Install the chart and bootstrap a platform and catalog in kind.
	@# Installs the chart itself rather than raw manifests: it is the artefact
	@# that ships, so every e2e run smoke-tests it — including the Gateway API
	@# CRDs it now installs (ADR-0006).
	helm upgrade --install agentgateway-educates-operator "$(CHART_DIR)" \
		--namespace agentgateway-operator-system --create-namespace \
		--set image.repository="$(firstword $(subst :, ,$(KIND_IMG)))" \
		--set image.tag="$(lastword $(subst :, ,$(KIND_IMG)))" \
		--set image.pullPolicy=IfNotPresent \
		--wait --timeout 5m
	kubectl apply -f "$(E2E_BOOTSTRAP_DIR)/platform.yaml"
	@echo "Waiting for the platform to become ready..."
	kubectl wait --for=condition=Ready agentgatewayplatform/cluster --timeout=10m
	@# A placeholder credential. The teardown test never makes an upstream call,
	@# so the catalog only has to render and become ready — it needs a Secret to
	@# reference, not a working key.
	kubectl create namespace "$(LLM_CREDENTIAL_NAMESPACE)" --dry-run=client -o yaml | kubectl apply -f -
	kubectl create secret generic "$(LLM_CREDENTIAL_SECRET)" \
		--namespace "$(LLM_CREDENTIAL_NAMESPACE)" \
		--from-literal=api-key=sk-e2e-placeholder-not-a-real-key \
		--dry-run=client -o yaml | kubectl apply -f -
	kubectl apply -f "$(E2E_BOOTSTRAP_DIR)/catalog.yaml"
	kubectl wait --for=condition=Ready agentgatewaycatalog/cluster --timeout=5m

.PHONY: kind-delete
kind-delete: ## Delete the kind cluster.
	@kind delete cluster --name "$(KIND_CLUSTER)" 2>/dev/null || true

.PHONY: ci-e2e
ci-e2e: ## Everything the e2e job runs, in one step so local and CI cannot drift.
	$(MAKE) kind-create
	$(MAKE) kind-load
	$(MAKE) kind-deploy
	$(MAKE) test-e2e

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
