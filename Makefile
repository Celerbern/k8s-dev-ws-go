# devws — Kubernetes development workspaces
#
# Common targets:
#   make build        — build frontend + both Go binaries into bin/
#   make generate     — regenerate deepcopy + CRD manifests (after editing api/)
#   make test         — run Go tests
#   make kind-e2e     — full end-to-end install on a local kind cluster
#   make kind-install — (re)deploy the chart on an existing kind cluster

MODULE        := github.com/celerbern/k8s-dev-ws-go
APISERVER_BIN := bin/devws-apiserver
CLI_BIN       := bin/devws
APISERVER_IMG ?= devws-apiserver:dev
WORKSPACE_IMG ?= devws-workspace:dev
KIND_NAME     ?= devws
RELEASE_NS    ?= devws-system
RELEASE_NAME  ?= devws
WEB_DIR       := web

export KIND_NAME

.PHONY: all build build-go build-web generate test vet fmt clean \
	docker-apiserver docker-workspace kind-up kind-down kind-load kind-install \
	kind-e2e helm-lint

all: build

## generate: regenerate deepcopy functions and CRD manifests
generate:
	go tool controller-gen object:headerFile="hack/boilerplate.go.txt" paths="./..."
	go tool controller-gen crd paths="./api/..." output:crd:artifacts:config=charts/devws/crds

## build: build frontend and Go binaries
build: build-web build-go

build-go:
	CGO_ENABLED=0 go build -trimpath -o $(APISERVER_BIN) ./cmd/devws-apiserver
	CGO_ENABLED=0 go build -trimpath -o $(CLI_BIN) ./cmd/devws

## build-web: build the Vue SPA into internal/web/dist (requires node/npm)
build-web:
	cd $(WEB_DIR) && npm install --no-fund --no-audit && npm run build

test:
	go test ./...

vet:
	go vet ./...

fmt:
	go fmt ./...

clean:
	rm -rf bin

## docker-apiserver: build the control-plane image (includes web assets + CLI)
docker-apiserver:
	docker build -f images/apiserver/Dockerfile -t $(APISERVER_IMG) .

## docker-workspace: build the workspace image (sshd + toolchains)
docker-workspace:
	docker build -t $(WORKSPACE_IMG) images/workspace

kind-up:
	kind create cluster --name $(KIND_NAME) --config hack/kind-config.yaml

kind-down:
	kind delete cluster --name $(KIND_NAME)

kind-load: docker-apiserver docker-workspace
	kind load docker-image $(APISERVER_IMG) --name $(KIND_NAME)
	kind load docker-image $(WORKSPACE_IMG) --name $(KIND_NAME)

## kind-install: deploy/upgrade the helm chart using locally built images
kind-install:
	helm upgrade --install $(RELEASE_NAME) charts/devws \
		--namespace $(RELEASE_NS) --create-namespace \
		--set image.repository=$(shell echo $(APISERVER_IMG) | cut -d: -f1) \
		--set image.tag=$(shell echo $(APISERVER_IMG) | cut -d: -f2) \
		--set image.pullPolicy=Never \
		--set workspaceImage=$(WORKSPACE_IMG) \
		--set config.sshHost=127.0.0.1:2222 \
		--set service.ssh.type=NodePort \
		--set service.ssh.nodePort=30022

## kind-e2e: create a kind cluster, build images, install, run smoke tests
kind-e2e:
	./hack/e2e-kind.sh

helm-lint:
	helm lint charts/devws