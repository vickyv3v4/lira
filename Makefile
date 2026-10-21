
# Image URL to use all building/pushing image targets
TAG ?= multi-eniip
RESOURCE_IMG ?= lira-resource:${TAG}
CONTROLLER_IMG ?= lira-controller:${TAG}
DAEMON_IMG ?= lira-daemon:${TAG}
INIT_IMG ?= lira-init-cni:${TAG}

# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.23

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

ARCH=$(shell go env GOARCH)
OS=$(shell go env GOOS)


# Setting SHELL to bash allows bash commands to be executed by recipes.
# This is a requirement for 'setup-envtest.sh' in the test target.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec



.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests:  ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=pkg/config/crd/bases output:rbac:artifacts:config=pkg/config/rbac

.PHONY: generate
generate: manifests ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
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

##@ Build
.PHONY: all
all: generate fmt vet lira lira-daemon lira-controller lira-resource

.PHONY: lira
lira:
	CGO_ENABLED=0 GOOS=$(OS) GOARCH=$(ARCH) go build -o bin/lira cmd/lira/main.go

.PHONY: lira-daemon
lira-daemon:
	CGO_ENABLED=0 GOOS=$(OS) GOARCH=$(ARCH) go build -o bin/lira-daemon cmd/lira-daemon/main.go

.PHONY: lira-controller
lira-controller:
	CGO_ENABLED=0 GOOS=$(OS) GOARCH=$(ARCH) go build -o bin/lira-controller cmd/lira-controller/main.go
.PHONY: lira-resource
lira-resource:
	CGO_ENABLED=0 GOOS=$(OS) GOARCH=$(ARCH) go build -o bin/lira-resource cmd/lira-resource/main.go

.PHONY: clean
clean:
	rm -rf bin/

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./main.go

.PHONY: images
images: controller-img daemon-img init-img resource-img

.PHONY: controller-img
controller-img:
	docker build -t ${CONTROLLER_IMG} -f build/lira-controller/Dockerfile .

.PHONY: resource-img
resource-img:
	docker build -t ${RESOURCE_IMG} -f build/lira-resource/Dockerfile .

.PHONY: daemon-img
daemon-img:
	docker build -t ${DAEMON_IMG} -f build/lira-daemon/Dockerfile .

.PHONY: init-img
init-img:
	docker build -t ${INIT_IMG} -f build/lira-daemon/Dockerfile-install .

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | kubectl apply -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/crd | kubectl delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/default | kubectl apply -f -

.PHONY: undeploy
undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/default | kubectl delete --ignore-not-found=$(ignore-not-found) -f -

##@ Build Dependencies

## Location to install dependencies to
LOCALBIN ?= /usr/local/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen-v0.14.0
ENVTEST ?= $(LOCALBIN)/setup-envtest

## Tool Versions
KUSTOMIZE_VERSION ?= v3.8.7
CONTROLLER_TOOLS_VERSION ?= v0.8.0


.PHONY: envtest
envtest: $(ENVTEST) ## Download envtest-setup locally if necessary.
$(ENVTEST): $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
