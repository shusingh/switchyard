# Switchyard developer entry point. Run `make help` for the target list.
#
# Works with GNU Make on Linux, macOS, and Windows (Git Bash). Targets that
# manage vLLM run inside WSL2 on Windows and natively on Linux.

SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

GO            ?= go
GOLANGCI_LINT ?= golangci-lint
BIN_DIR       := bin
BINARIES      := switchyard simengine loadgen

ifeq ($(OS),Windows_NT)
  EXE    := .exe
  # Resolve WSL through Sysnative so 32-bit shells reach the 64-bit binary.
  LINUX  := /c/Windows/Sysnative/wsl.exe -d Ubuntu-24.04 --cd "$(CURDIR)" --
else
  EXE    :=
  LINUX  :=
endif

.PHONY: help
help: ## Show this help.
	@grep -hE '^[a-zA-Z0-9_-]+:.*## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

# Build and test

.PHONY: build
build: ## Build all binaries into ./bin.
	@mkdir -p $(BIN_DIR)
	@for b in $(BINARIES); do \
		if [ -d cmd/$$b ]; then $(GO) build -trimpath -o $(BIN_DIR)/$$b$(EXE) ./cmd/$$b; fi; \
	done

.PHONY: test
test: ## Run unit and integration tests with the race detector.
	$(GO) test -race -count=1 ./...

.PHONY: test-e2e
test-e2e: build ## Run end-to-end tests against the built binaries.
	$(GO) test -race -count=1 -tags e2e ./test/e2e/...

.PHONY: cover
cover: ## Run tests with coverage and print the total.
	$(GO) test -race -count=1 -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -n 1

.PHONY: bench
bench: ## Run microbenchmarks.
	$(GO) test -run='^$$' -bench=. -benchmem ./...

# Code quality

.PHONY: lint
lint: ## Run golangci-lint.
	$(GOLANGCI_LINT) run ./...

.PHONY: fmt
fmt: ## Format code with gofmt and goimports rules.
	$(GOLANGCI_LINT) fmt ./...

.PHONY: vet
vet: ## Run go vet.
	$(GO) vet ./...

.PHONY: tidy
tidy: ## Tidy go.mod and go.sum.
	$(GO) mod tidy

.PHONY: check
check: tidy lint test ## Everything CI runs: tidy, lint, test.

# Environment

.PHONY: setup
setup: ## Install git hooks and verify the toolchain.
	./scripts/setup-dev.sh

.PHONY: vllm-install
vllm-install: ## Install vLLM into a virtual environment (WSL2 or Linux).
	$(LINUX) bash deploy/vllm/install.sh

.PHONY: vllm-up
vllm-up: ## Start the vLLM replicas.
	$(LINUX) bash deploy/vllm/replicas.sh start

.PHONY: vllm-down
vllm-down: ## Stop the vLLM replicas.
	$(LINUX) bash deploy/vllm/replicas.sh stop

.PHONY: vllm-status
vllm-status: ## Report replica health.
	$(LINUX) bash deploy/vllm/replicas.sh status

.PHONY: clean
clean: ## Remove build and coverage output.
	rm -rf $(BIN_DIR) coverage.out
