APP           := skillsd
IMG_REPO      := localhost:5005/skillsd
IMG_TAG       := dev
IMG           := $(IMG_REPO):$(IMG_TAG)
CHART         := charts/skillsd
RELEASE       := skillsd
KIND_CONTEXT  := kind-skillsd
CLUSTER_CFG   := local/cluster.yaml
VALUES        := local/values.yaml
GRPC_PORT     := 8080
GIT_HOST      := github.com

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

## --- Go ---

.PHONY: build
build: ## Build the skillsd and skillsd-registry binaries into ./bin
	go build -o bin/$(APP) ./cmd/skillsd
	go build -o bin/$(APP)-registry ./cmd/skillsd-registry

.PHONY: test
test: ## Run the test suite
	go test ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: fmt
fmt: ## Format Go source
	gofmt -l -w .

.PHONY: proto
proto: ## Regenerate Go code from proto/
	buf generate

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin/

## --- Docker ---

.PHONY: docker-build
docker-build: ## Build the skillsd image
	docker build -t $(IMG) .

## --- Local cluster (ctlptl + kind) ---

.PHONY: cluster-up
cluster-up: ## Create the local kind cluster + registry
	ctlptl apply -f $(CLUSTER_CFG)

.PHONY: cluster-down
cluster-down: ## Delete the local kind cluster + registry
	ctlptl delete -f $(CLUSTER_CFG)

## --- Helm ---

.PHONY: helm-lint
helm-lint: ## Lint the skillsd chart
	helm lint $(CHART)

.PHONY: helm-template
helm-template: ## Render the skillsd chart with local values
	helm template $(RELEASE) $(CHART) -f $(VALUES)

## --- Dev loop ---

.PHONY: git-known-hosts
git-known-hosts: ## Generate local/git-known-hosts for GIT_HOST (used by the Tiltfile's optional git-auth Secret)
	ssh-keyscan $(GIT_HOST) > local/git-known-hosts

.PHONY: check-prereqs
check-prereqs: ## Verify required local tools are installed
	@for bin in docker ctlptl kubectl helm tilt; do \
		command -v $$bin >/dev/null 2>&1 || { echo "missing required tool: $$bin"; exit 1; }; \
	done

.PHONY: dev
dev: check-prereqs cluster-up ## Start the local cluster (if needed) and the Tilt dev loop
	tilt up

.PHONY: logs
logs: ## Tail skillsd logs on the local cluster
	kubectl --context $(KIND_CONTEXT) logs -l app.kubernetes.io/instance=$(RELEASE) -f
