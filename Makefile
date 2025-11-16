.PHONY: help build test test-unit test-integration itest-up itest itest-stability itest-down lint clean install release fmt vet security coverage deps

# Default target
.DEFAULT_GOAL := help

# Variables
BINARY_NAME := rdhpf
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
LDFLAGS := -ldflags "-X main.version=$(VERSION) -X main.buildDate=$(BUILD_DATE) -X main.commit=$(COMMIT) -s -w"

# Build settings
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
CGO_ENABLED ?= 0

# Directories
BUILD_DIR := build
COVERAGE_DIR := coverage

# Verbose mode
VERBOSE ?= 0
ifeq ($(VERBOSE),1)
	GOTEST_FLAGS = -v
	GOBUILD_FLAGS = -v
else
	GOTEST_FLAGS =
	GOBUILD_FLAGS =
endif

# Colors for output
CYAN := \033[0;36m
GREEN := \033[0;32m
YELLOW := \033[0;33m
RED := \033[0;31m
NC := \033[0m # No Color

help: ## Show this help message
	@printf "$(CYAN)Remote Docker Host Port Forwarder - Makefile$(NC)\n"
	@printf "\n"
	@printf "$(GREEN)Available targets:$(NC)\n"
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*?##/ { printf "  $(CYAN)%-18s$(NC) %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@printf "\n"
	@printf "$(YELLOW)Examples:$(NC)\n"
	@printf "  make build              # Build binary for current platform\n"
	@printf "  make test               # Run all tests\n"
	@printf "  make test VERBOSE=1     # Run tests with verbose output\n"
	@printf "  make install            # Install binary to GOPATH/bin\n"
	@printf "  make release            # Build for all platforms\n"

build: ## Build binary for current platform
	@printf "$(GREEN)Building $(BINARY_NAME) for $(GOOS)/$(GOARCH)...$(NC)\n"
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) go build $(GOBUILD_FLAGS) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/rdhpf
	@printf "$(GREEN)✓ Build complete: $(BUILD_DIR)/$(BINARY_NAME)$(NC)\n"

test: test-unit ## Run all tests (unit tests)
	@printf "$(GREEN)✓ All tests passed$(NC)\n"

test-unit: ## Run unit tests
	@printf "$(GREEN)Running unit tests...$(NC)\n"
	@go test $(GOTEST_FLAGS) -race -timeout=5m ./...
	@printf "$(GREEN)✓ Unit tests passed$(NC)\n"

itest-up: ## Start integration test harness (SSH container with Docker socket mount)
	@bash scripts/itest-up.sh

itest: ## Run integration tests (requires itest-up first)
	@printf "$(GREEN)Running integration tests...$(NC)\n"
	@HOME=$(PWD)/.itests/home \
		SSH_TEST_HOST=ssh://testuser@localhost:2222 \
		SSH_TEST_KEY_PATH=$(PWD)/.itests/home/.ssh/id_ed25519 \
		go test $(GOTEST_FLAGS) -timeout=15m ./tests/integration/...
	@printf "$(GREEN)✓ Integration tests passed$(NC)\n"

itest-stability: ## Run only stability tests (requires itest-up first, ~7 min)
	@printf "$(GREEN)Running tunnel stability tests (this will take ~7 minutes)...$(NC)\n"
	@printf "$(YELLOW)Note: Tests run serially to prevent interference$(NC)\n"
	@HOME=$(PWD)/.itests/home \
		SSH_TEST_HOST=ssh://testuser@localhost:2222 \
		SSH_TEST_KEY_PATH=$(PWD)/.itests/home/.ssh/id_ed25519 \
		go test $(GOTEST_FLAGS) -timeout=10m -run 'TestManager_(LongRunning|TunnelStability)' ./tests/integration/
	@printf "$(GREEN)✓ Stability tests passed$(NC)\n"

itest-down: ## Stop integration test harness
	@bash scripts/itest-down.sh

lint: ## Run linters
	@printf "$(GREEN)Running linters...$(NC)\n"
	@printf "  - go vet\n"
	@go vet ./...
	@printf "  - gofmt\n"
	@if [ -n "$$(find . -name '*.go' -not -path './.itests/*' -not -path './vendor/*' -exec gofmt -s -l {} \;)" ]; then \
		printf "$(RED)✗ Code is not formatted. Run 'make fmt'$(NC)\n"; \
		find . -name '*.go' -not -path './.itests/*' -not -path './vendor/*' -exec gofmt -s -d {} \;; \
		exit 1; \
	fi
	@printf "  - golangci-lint (if available)\n"
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run --timeout=5m ./...; \
	else \
		printf "$(YELLOW)⚠ golangci-lint not installed, skipping$(NC)\n"; \
		printf "  Install: curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b \$$(go env GOPATH)/bin\n"; \
	fi
	@printf "$(GREEN)✓ Lint checks passed$(NC)\n"

fmt: ## Format code with gofmt
	@printf "$(GREEN)Formatting code...$(NC)\n"
	@gofmt -s -w $$(find . -name '*.go' -not -path './.itests/*' -not -path './vendor/*')
	@printf "$(GREEN)✓ Code formatted$(NC)\n"

vet: ## Run go vet
	@printf "$(GREEN)Running go vet...$(NC)\n"
	@go vet ./...
	@printf "$(GREEN)✓ Vet checks passed$(NC)\n"

security: ## Run security checks
	@printf "$(GREEN)Running security checks...$(NC)\n"
	@if command -v gosec >/dev/null 2>&1; then \
		gosec -fmt=text ./...; \
	else \
		printf "$(YELLOW)⚠ gosec not installed$(NC)\n"; \
		printf "  Install: go install github.com/securego/gosec/v2/cmd/gosec@latest\n"; \
		exit 1; \
	fi
	@printf "$(GREEN)✓ Security checks passed$(NC)\n"

coverage: ## Generate test coverage report
	@printf "$(GREEN)Generating coverage report...$(NC)\n"
	@mkdir -p $(COVERAGE_DIR)
	@go test -race -coverprofile=$(COVERAGE_DIR)/coverage.out -covermode=atomic ./...
	@go tool cover -html=$(COVERAGE_DIR)/coverage.out -o $(COVERAGE_DIR)/coverage.html
	@go tool cover -func=$(COVERAGE_DIR)/coverage.out | grep total | awk '{print "Total coverage: " $$3}'
	@printf "$(GREEN)✓ Coverage report generated: $(COVERAGE_DIR)/coverage.html$(NC)\n"

deps: ## Download dependencies
	@printf "$(GREEN)Downloading dependencies...$(NC)\n"
	@go mod download
	@go mod tidy
	@printf "$(GREEN)✓ Dependencies updated$(NC)\n"

clean: ## Remove build artifacts
	@printf "$(GREEN)Cleaning build artifacts...$(NC)\n"
	@rm -rf $(BUILD_DIR)
	@rm -rf $(COVERAGE_DIR)
	@rm -f $(BINARY_NAME)
	@rm -f coverage.out coverage.html
	@printf "$(GREEN)✓ Clean complete$(NC)\n"

install: build ## Install binary to GOPATH/bin
	@printf "$(GREEN)Installing $(BINARY_NAME) to $(GOPATH)/bin...$(NC)\n"
	@mkdir -p $(GOPATH)/bin
	@cp $(BUILD_DIR)/$(BINARY_NAME) $(GOPATH)/bin/$(BINARY_NAME)
	@printf "$(GREEN)✓ Installed: $(GOPATH)/bin/$(BINARY_NAME)$(NC)\n"

uninstall: ## Remove binary from GOPATH/bin
	@printf "$(GREEN)Uninstalling $(BINARY_NAME)...$(NC)\n"
	@rm -f $(GOPATH)/bin/$(BINARY_NAME)
	@printf "$(GREEN)✓ Uninstalled$(NC)\n"

release: ## Build for all platforms (local)
	@printf "$(GREEN)Building for all platforms...$(NC)\n"
	@mkdir -p $(BUILD_DIR)
	
	@printf "  - linux/amd64\n"
	@CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 ./cmd/rdhpf
	
	@printf "  - linux/arm64\n"
	@CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 ./cmd/rdhpf
	
	@printf "  - darwin/amd64\n"
	@CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 ./cmd/rdhpf
	
	@printf "  - darwin/arm64\n"
	@CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 ./cmd/rdhpf
	
	@printf "  - windows/amd64\n"
	@CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-windows-amd64.exe ./cmd/rdhpf
	
	@printf "$(GREEN)✓ Release builds complete in $(BUILD_DIR)/$(NC)\n"
	@ls -lh $(BUILD_DIR)

run: build ## Build and run the binary
	@printf "$(GREEN)Running $(BINARY_NAME)...$(NC)\n"
	@$(BUILD_DIR)/$(BINARY_NAME)

check: lint test ## Run all checks (lint + test)
	@printf "$(GREEN)✓ All checks passed$(NC)\n"

ci: deps lint test build ## Run CI pipeline locally
	@printf "$(GREEN)✓ CI pipeline complete$(NC)\n"

version: ## Show version information
	@printf "Version: $(VERSION)\n"
	@printf "Commit:  $(COMMIT)\n"
	@printf "Date:    $(BUILD_DATE)\n"
	@printf "GOOS:    $(GOOS)\n"
	@printf "GOARCH:  $(GOARCH)\n"

doctor: ## Check development environment
	@printf "$(CYAN)Checking development environment...$(NC)\n"
	@printf "\n"
	@printf "$(GREEN)Go:$(NC)\n"
	@go version || printf "$(RED)✗ Go not installed$(NC)\n"
	@printf "\n"
	@printf "$(GREEN)Docker:$(NC)\n"
	@docker --version || printf "$(YELLOW)⚠ Docker not installed (required for integration tests)$(NC)\n"
	@printf "\n"
	@printf "$(GREEN)Optional tools:$(NC)\n"
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint version || printf "  golangci-lint: $(YELLOW)not installed$(NC)\n"
	@command -v gosec >/dev/null 2>&1 && gosec -version || printf "  gosec: $(YELLOW)not installed$(NC)\n"
	@command -v goreleaser >/dev/null 2>&1 && goreleaser --version || printf "  goreleaser: $(YELLOW)not installed$(NC)\n"
	@printf "\n"
	@printf "$(GREEN)✓ Environment check complete$(NC)\n"