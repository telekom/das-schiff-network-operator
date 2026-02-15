include versions.env

# Image URL to use all building/pushing image targets
IMG_BASE ?= ghcr.io/telekom
IMG ?= $IMG_BASE/das-schiff-network-operator:latest
# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.35

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# Setting SHELL to bash allows bash commands to be executed by recipes.
# This is a requirement for 'setup-envtest.sh' in the test target.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

LDFLAGS := $(shell hack/version.sh)

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk commands is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ DevelopmentLDFLAGS := $(shell hack/version.sh)

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" go test ./... -coverprofile cover.out

##@ Linting

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter.
	$(GOLANGCI_LINT) run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes.
	$(GOLANGCI_LINT) run --fix

.PHONY: lint-strict
lint-strict: golangci-lint ## Run golangci-lint with strict settings (as in CI).
	$(GOLANGCI_LINT) run --timeout 10m --issues-exit-code 1

.PHONY: vulncheck
vulncheck: ## Run govulncheck to check for known vulnerabilities.
	@command -v govulncheck >/dev/null 2>&1 || go install golang.org/x/vuln/cmd/govulncheck@latest
	govulncheck ./...

.PHONY: verify
verify: lint-strict vet test vulncheck ## Run all verification checks (lint, vet, test, vulncheck).
	@echo "All verification checks passed!"

##@ Build

.PHONY: build
build: generate fmt vet ## Build manager binary.
	go build -ldflags "$(LDFLAGS)" -o bin/manager cmd/operator/main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run -ldflags "$(LDFLAGS)" ./cmd/operator/main.go

.PHONY: docker-build
docker-build: #test ## Build docker image with the manager.
	docker build --build-arg ldflags="$(LDFLAGS)" -f das-schiff-cra-frr.Dockerfile -t ${IMG_BASE}/das-schiff-cra-frr:latest .
	docker build --build-arg ldflags="$(LDFLAGS)" -f das-schiff-network-operator.Dockerfile -t ${IMG_BASE}/das-schiff-network-operator:latest .
	docker build --build-arg ldflags="$(LDFLAGS)" -f das-schiff-nwop-agent-cra-frr.Dockerfile -t ${IMG_BASE}/das-schiff-nwop-agent-cra-frr:latest .
	docker build --build-arg ldflags="$(LDFLAGS)" -f das-schiff-nwop-agent-cra-vsr.Dockerfile -t ${IMG_BASE}/das-schiff-nwop-agent-cra-vsr:latest .
	docker build --build-arg ldflags="$(LDFLAGS)" -f das-schiff-nwop-agent-hbn-l2.Dockerfile -t ${IMG_BASE}/das-schiff-nwop-agent-hbn-l2:latest .
	docker build --build-arg ldflags="$(LDFLAGS)" -f das-schiff-nwop-agent-netplan.Dockerfile -t ${IMG_BASE}/das-schiff-nwop-agent-netplan:latest .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	docker push ${IMG}

.PHONY: docker-push-sidecar
docker-push-sidecar: ## Push docker image with the manager.
	docker push ${SIDECAR_IMG}

.PHONY: kind-load
kind-load: docker-build ## Load docker image into kind cluster.
	kind load docker-image ${IMG_BASE}/das-schiff-cra-frr:latest
	kind load docker-image ${IMG_BASE}/das-schiff-network-operator:latest
	kind load docker-image ${IMG_BASE}/das-schiff-nwop-agent-cra-frr:latest
	kind load docker-image ${IMG_BASE}/das-schiff-nwop-agent-cra-vsr:latest
	kind load docker-image ${IMG_BASE}/das-schiff-nwop-agent-hbn-l2:latest
	kind load docker-image ${IMG_BASE}/das-schiff-nwop-agent-netplan:latest

##@ Release

RELEASE_DIR ?= out

$(RELEASE_DIR):
	mkdir -p $(RELEASE_DIR)/

licenses-report: go-licenses
	rm -rf $(RELEASE_DIR)/licenses
	$(GO_LICENSES) save --save_path $(RELEASE_DIR)/licenses ./...
	$(GO_LICENSES) report --template hack/licenses.md.tpl ./... > $(RELEASE_DIR)/licenses/licenses.md
	(cd out/licenses && tar -czf ../licenses.tar.gz *)

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | kubectl apply -f -

.PHONY: install-certs
install-certs: manifests kustomize ## Install certs
	$(KUSTOMIZE) build config/certmanager | kubectl apply -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/crd | kubectl delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: uninstall-certs
uninstall-certs: manifests kustomize ## Uninstall certs
	$(KUSTOMIZE) build config/certmanager | kubectl delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/operator && $(KUSTOMIZE) edit set image operator=${IMG}
	$(KUSTOMIZE) build config/default | kubectl apply -f -

.PHONY: undeploy
undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/default | kubectl delete --ignore-not-found=$(ignore-not-found) -f -

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
CONTROLLER_GEN = $(LOCALBIN)/controller-gen
KUSTOMIZE = $(LOCALBIN)/kustomize
ENVTEST = $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint
GO_LICENSES = $(LOCALBIN)/go-licenses

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: envtest
envtest: $(ENVTEST) ## Download envtest-setup locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

.PHONY: go-licenses
go-licenses: $(GO_LICENSES) ## Download go-licenses locally if necessary.
$(GO_LICENSES): $(LOCALBIN)
	$(call go-install-tool,$(GO_LICENSES),github.com/google/go-licenses,latest)

# go-install-tool will 'go install' any package with custom target and name of binary
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f $(1) || true ;\
GOBIN=$(LOCALBIN) go install $${package} ;\
mv $(1) $(1)-$(3) ;\
} ;\
ln -sf $(1)-$(3) $(1)
endef
