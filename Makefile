SHELL := bash
.ONESHELL:
.SHELLFLAGS := -eu -o pipefail -c
.DELETE_ON_ERROR:
MAKEFLAGS += --warn-undefined-variables
MAKEFLAGS += --no-builtin-rules

GO ?= go
PROJECT ?= $(notdir $(CURDIR))
BIN_DIR ?= bin
BINARY ?= wait0
CMD_PACKAGE ?= ./cmd/wait0
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')

DOCKER ?= docker
DOCKER_IMAGE ?= $(PROJECT)
DOCKER_TAG ?= $(VERSION)
CONTAINER_NAME ?= wait0-local

COVERAGE_THRESHOLD ?= 80

.DEFAULT_GOAL := help

.PHONY: help build test test-race coverage lint fmt dev clean ci ci-check print-version \
	docker-build docker-run docker-stop docker-logs docker-push docker-clean release

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*##"; printf "Usage:\n  make \033[36m<target>\033[0m\n"} \
		/^[a-zA-Z0-9_.-]+:.*## / {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2} \
		/^##@/ {printf "\n\033[1m%s\033[0m\n", substr($$0, 5)}' $(MAKEFILE_LIST)

##@ Build
build: ## Build wait0 binary
	mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -o $(BIN_DIR)/$(BINARY) $(CMD_PACKAGE)

print-version: ## Print build metadata
	@echo "project=$(PROJECT)"
	@echo "version=$(VERSION)"
	@echo "commit=$(COMMIT)"
	@echo "build_time=$(BUILD_TIME)"

##@ Quality
test: ## Run unit tests
	$(GO) test ./...

test-race: ## Run tests with race detector
	$(GO) test -race ./...

coverage: ## Run coverage gate for internal/wait0
	./scripts/coverage.sh $(COVERAGE_THRESHOLD)

lint: ## Run static checks
	$(GO) vet ./...

fmt: ## Format Go sources
	$(GO) fmt ./...

ci: lint test build ## Run fast CI checks (lint + test + build)

ci-check: lint test test-race coverage build ## Run full local quality gate

##@ Development
dev: ## Run wait0 with debug config
	$(GO) run $(CMD_PACKAGE) -config ./debug/wait0.yaml

clean: ## Remove build and coverage artifacts
	rm -rf $(BIN_DIR) coverage.out coverage.internal.filtered.out coverage-summary.txt

##@ Docker
docker-build: ## Build Docker image
	$(DOCKER) build -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

docker-run: ## Run container with debug config and local data volume
	mkdir -p .wait0-data
	$(DOCKER) run -d --rm \
		--name $(CONTAINER_NAME) \
		-p 8082:8082 \
		-v "$(CURDIR)/debug/wait0.yaml:/wait0.yaml:ro" \
		-v "$(CURDIR)/.wait0-data:/data" \
		$(DOCKER_IMAGE):$(DOCKER_TAG)

docker-stop: ## Stop local container
	-$(DOCKER) stop $(CONTAINER_NAME)

docker-logs: ## Tail local container logs
	$(DOCKER) logs -f $(CONTAINER_NAME)

docker-push: ## Push Docker image to registry
	$(DOCKER) push $(DOCKER_IMAGE):$(DOCKER_TAG)

docker-clean: ## Remove local Docker image
	-$(DOCKER) image rm $(DOCKER_IMAGE):$(DOCKER_TAG)

##@ Publish
release: ## Build and publish Docker image to Devforth DockerHub
	./scripts/publish.sh
