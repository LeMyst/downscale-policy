# Image URL to use for all building/pushing image targets
IMG ?= ghcr.io/LeMyst/downscale-policy:latest

CONTROLLER_GEN_VERSION ?= v0.19.0
CONTROLLER_GEN = go run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)
# Explicit package paths instead of ./... so generation also works on Windows.
GEN_PATHS = paths=./api/v1 paths=./internal/controller

.PHONY: all
all: build

##@ Development

.PHONY: manifests
manifests: ## Generate CRD and RBAC manifests into config/ and sync the CRD into the Helm chart.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd $(GEN_PATHS) \
		output:crd:artifacts:config=config/crd/bases \
		output:rbac:artifacts:config=config/rbac
	cp config/crd/bases/downscaler.io_downscalepolicies.yaml charts/downscale-policy/crds/

.PHONY: generate
generate: ## Generate zz_generated.deepcopy.go.
	$(CONTROLLER_GEN) object $(GEN_PATHS)

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet ## Run tests.
	go test ./... -coverprofile cover.out

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build manager binary.
	go build -o bin/manager cmd/main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host (uses current kubeconfig).
	go run ./cmd/main.go

.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	docker build -t $(IMG) .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	docker push $(IMG)

##@ Deployment

.PHONY: install
install: manifests ## Install CRDs into the cluster.
	kubectl apply -k config/crd

.PHONY: uninstall
uninstall: ## Uninstall CRDs from the cluster.
	kubectl delete -k config/crd

.PHONY: deploy
deploy: manifests ## Deploy controller to the cluster (edit the image in config/default/kustomization.yaml).
	kubectl apply -k config/default

.PHONY: undeploy
undeploy: ## Undeploy controller from the cluster.
	kubectl delete -k config/default
