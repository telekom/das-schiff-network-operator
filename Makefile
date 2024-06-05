
# Image URL to use all building/pushing image targets
IMG ?= ghcr.io/telekom/das-schiff-network-operator:latest
# Sidecar image URL to use all building/pushing image targets
SIDECAR_IMG ?= ghcr.io/telekom/frr-exporter:latest
# Sidecar image URL to use all building/pushing image targets
CONFIGURATOR_IMG ?= ghcr.io/telekom/configurator:latest
# Agent image URL to use all building/pushing image targets
AGENT_IMG ?= ghcr.io/telekom/agent:latest
# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.25

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

##@ Development

.PHONY: bpf-generate
bpf-generate: ## Run go generate for the BPF program (builds it as well)
	cd pkg/bpf/ && go generate

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen bpf-generate ## Generate code containing DeepCopy, DeepCopyInto and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."
	PATH=$(PATH):$(shell pwd)/bin $(PROTOC) --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative pkg/agent/pb/agent.proto

.PHONY: generate-protobuff
generate-protobuff: protoc ## Generate code containing DeepCopy, DeepCopyInto and DeepCopyObject method implementations.
	PATH=$(PATH):$(shell pwd)/bin $(PROTOC) --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative pkg/agent/pb/agent.proto

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" go test ./... -coverprofile cover.out

##@ Build

.PHONY: build
build: generate fmt vet ## Build manager binary.
	go build -o bin/manager cmd/manager/main.go

.PHONY: sidecar-build ## Build sidecar binary.
sidecar-build: generate fmt vet
	go build -o bin/frr-exporter cmd/frr-exporter/main.go

.PHONY: configurator-build ## Build configurator binary.
configurator-build: generate fmt vet
	go build -o bin/configurator cmd/configurator/main.go

.PHONY: agent-build ## Build agent binary.
agent-build: generate fmt vet
	go build -o bin/agent cmd/agent/main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/manager/main.go

.PHONY: docker-build
docker-build: test ## Build docker image with the manager.
	docker build -t ${IMG} .

.PHONY: docker-build-sidecar
docker-build-sidecar: test ## Build docker image with the manager.
	docker build -t ${SIDECAR_IMG} -f frr-exporter.Dockerfile .

.PHONY: docker-build-configurator
docker-build-configurator: test ## Build docker image with the manager.
	docker build -t ${CONFIGURATOR_IMG} -f configurator.Dockerfile .

.PHONY: docker-build-agent
docker-build-agent: test ## Build docker image with the manager.
	docker build -t ${AGENT_IMG} -f agent.Dockerfile .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	docker push ${IMG}

.PHONY: docker-push-sidecar
docker-push-sidecar: ## Push docker image with the sidcar.
	docker push ${SIDECAR_IMG}

.PHONY: docker-push-configurator
docker-push-configurator: ## Push docker image with the configurator.
	docker push ${CONFIGURATOR_IMG}

.PHONY: docker-push-agent
docker-push-agent: ## Push docker image with the agent.
	docker push ${AGENT_IMG}

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
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	cd config/manager && $(KUSTOMIZE) edit set image frr-exporter=${SIDECAR_IMG}
	cd config/configurator && $(KUSTOMIZE) edit set image configurator=${CONFIGURATOR_IMG}
	cd config/agent && $(KUSTOMIZE) edit set image agent=${AGENT_IMG}
	$(KUSTOMIZE) build config/default | kubectl apply -f -

.PHONY: undeploy
undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/default | kubectl delete --ignore-not-found=$(ignore-not-found) -f -

PROTOC_DIR = $(shell pwd)/bin/protoc
PROTOC = $(shell pwd)/bin/protoc/bin/protoc
.PHONY: protoc
protoc: ## Download controller-gen locally if necessary.
	mkdir -p $(PROTOC_DIR) && cd $(PROTOC_DIR) && wget -nc https://github.com/protocolbuffers/protobuf/releases/download/v27.0/protoc-27.0-linux-x86_64.zip 2> /dev/null && unzip -nqq protoc-27.0-linux-x86_64.zip
	$(call go-get-tool,$(PROTOC_DIR),google.golang.org/protobuf/cmd/protoc-gen-go@v1.33.0)

CONTROLLER_GEN = $(shell pwd)/bin/controller-gen
.PHONY: controller-gen
controller-gen: ## Download controller-gen locally if necessary.
	$(call go-get-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen@v0.14.0)

KUSTOMIZE = $(shell pwd)/bin/kustomize
.PHONY: kustomize
kustomize: ## Download kustomize locally if necessary.
	$(call go-get-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5@v5.0.3)

ENVTEST = $(shell pwd)/bin/setup-envtest
.PHONY: envtest
envtest: ## Download envtest-setup locally if necessary.
	$(call go-get-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest@latest)

GO_LICENSES = $(shell pwd)/bin/go-licenses
.PHONY: go-licenses
go-licenses: ## Download go-licenses locally if necessary.
	$(call go-get-tool,$(GO_LICENSES),github.com/google/go-licenses@latest)

# go-get-tool will 'go get' any package $2 and install it to $1.
PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
define go-get-tool
@[ -f $(1) ] || { \
set -e ;\
TMP_DIR=$$(mktemp -d) ;\
cd $$TMP_DIR ;\
go mod init tmp ;\
echo "Downloading $(2)" ;\
GOBIN=$(PROJECT_DIR)/bin go install $(2) ;\
rm -rf $$TMP_DIR ;\
}
endef
