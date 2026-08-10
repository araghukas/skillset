APP           := skillsd
IMG_REPO      := localhost:5005/skillsd
IMG_TAG       := dev
IMG           := $(IMG_REPO):$(IMG_TAG)
CHART         := charts/skillsd
HELM_UNITTEST_VERSION := 1.1.2
RELEASE       := skillsd
KIND_CONTEXT  := kind-skillsd
CLUSTER_CFG   := local/cluster.yaml
VALUES        := local/values.yaml

# Upstream repo the local Gitea stand-in is seeded from (see local/gitea-init.sh).
# Override on the command line: make gitea-up GITEA_SEED_URL=https://github.com/me/my-skills.git
GITEA_SEED_URL ?= https://github.com/anthropics/skills.git
GITEA_SEED_REF ?= main
export GITEA_SEED_URL
export GITEA_SEED_REF

# Whether `make dev` bootstraps the local Gitea stand-in. Defaults to off when
# local/github-app.json exists (GitHub App auth against a real repo - see the
# Tiltfile). Pass GITEA=0 for the third mode, token auth against a real repo:
# gitea-init.sh deletes token files that don't authenticate against Gitea, so
# it must not run when local/git-skillsd-*token hold real GitHub tokens.
GITHUB_APP_JSON := local/github-app.json
GITEA ?= $(if $(wildcard $(GITHUB_APP_JSON)),0,1)
ifeq ($(GITEA),1)
DEV_PREREQS := check-prereqs cluster-up gitea-up
else
DEV_PREREQS := check-prereqs cluster-up
endif

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

## --- Go ---

.PHONY: build
build: ## Build the skillsd, skillsd-registry, and skillsd-init binaries into ./bin
	go build -o bin/$(APP) ./cmd/skillsd
	go build -o bin/$(APP)-registry ./cmd/skillsd-registry
	go build -o bin/$(APP)-init ./cmd/skillsd-init

.PHONY: test
test: ## Run the test suite (race detector on - see internal/skill.Metadata.Clone)
	go test -race ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: fmt
fmt: ## Format Go source
	gofmt -l -w .

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
cluster-down: ## Delete the local kind cluster + registry, and the now-unusable Gitea tokens with it
	ctlptl delete -f $(CLUSTER_CFG)
	rm -f local/git-skillsd-token local/git-skillsd-registry-token

## --- Helm ---

.PHONY: helm-lint
helm-lint: ## Lint the skillsd chart
	helm lint $(CHART)

.PHONY: helm-template
helm-template: ## Render the skillsd chart with local values
	helm template $(RELEASE) $(CHART) -f $(VALUES)

.PHONY: print-helm-unittest-version
print-helm-unittest-version: ## Print the pinned helm-unittest plugin version (used by CI)
	@echo $(HELM_UNITTEST_VERSION)

.PHONY: helm-test
helm-test: ## Run the chart unit tests (needs the helm-unittest plugin)
	@helm plugin list | grep -q '^unittest' || { \
		echo "missing helm plugin: helm plugin install https://github.com/helm-unittest/helm-unittest --version $(HELM_UNITTEST_VERSION) --verify=false"; \
		exit 1; \
	}
	helm unittest $(CHART)

## --- Dev loop ---

.PHONY: check-prereqs
check-prereqs: ## Verify required local tools are installed
	@for bin in docker ctlptl kubectl helm tilt; do \
		command -v $$bin >/dev/null 2>&1 || { echo "missing required tool: $$bin"; exit 1; }; \
	done

.PHONY: dev
dev: $(DEV_PREREQS) ## Start the local cluster, bootstrap Gitea (unless local/github-app.json exists or GITEA=0), and start the Tilt dev loop
	tilt up --debug --stream

.PHONY: dev-down
dev-down: ## Stop the Tilt dev loop (leaves Gitea, and its tokens, up for the next `make dev`; use gitea-down to reset that too)
	tilt down

.PHONY: gitea-up
gitea-up: cluster-up ## Bootstrap the local Gitea stand-in for GitHub (idempotent; see local/gitea-init.sh)
	./local/gitea-init.sh

.PHONY: gitea-down
gitea-down: ## Tear down the local Gitea instance and its bootstrapped tokens, for a clean re-test
	kubectl --context $(KIND_CONTEXT) delete -f local/gitea.yaml --ignore-not-found
	rm -f local/git-skillsd-token local/git-skillsd-registry-token

.PHONY: logs
logs: ## Tail skillsd logs on the local cluster
	kubectl --context $(KIND_CONTEXT) logs -l app.kubernetes.io/instance=$(RELEASE) -f

## --- Verification ---

.PHONY: verify
verify: ## Run MCP verification tests against the running local deployment
	go test -tags e2e -count=1 -v ./local/verify/...
