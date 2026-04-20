# rfc-api - Golang RFC API

## Project Variables

PROJECT_NAME  := rfc-api
PROJECT_OWNER := donaldgifford
DESCRIPTION   := Golang RFC API
PROJECT_URL   := https://github.com/$(PROJECT_OWNER)/$(PROJECT_NAME)

## Go Variables

GO          ?= go
GO_PACKAGE  := github.com/$(PROJECT_OWNER)/$(PROJECT_NAME)
GOOS        ?= $(shell $(GO) env GOOS)
GOARCH      ?= $(shell $(GO) env GOARCH)

GOIMPORTS_LOCAL_ARG := -local github.com/donaldgifford

## Build Directories

BUILD_DIR      := build
BIN_DIR        := $(BUILD_DIR)/bin

## Version Information

COMMIT_HASH ?= $(shell git rev-parse --short HEAD 2>/dev/null)
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
CUR_VERSION ?= $(shell git describe --tags --exact-match 2>/dev/null || git describe --tags 2>/dev/null || echo "v0.0.0-$(COMMIT_HASH)")

## Build Variables

COVERAGE_OUT := coverage.out



###############
##@ Go Development

.PHONY: build
.PHONY: test test-all test-coverage
.PHONY: lint lint-fix fmt clean
.PHONY: run run-local test-api ci check
.PHONY: release-check release-local

## Build Targets

build: build-core ## Build everything (core)

build-core: ## Build core binary
	@ $(MAKE) --no-print-directory log-$@
	@mkdir -p $(BIN_DIR)
	@go build -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT_HASH)" -o $(BIN_DIR)/$(PROJECT_NAME) ./cmd/$(PROJECT_NAME)
	@echo "✓ Core binaries built"

## Testing

test: ## Run all tests with race detector
	@ $(MAKE) --no-print-directory log-$@
	@go test -v -race ./...

test-all: test ## Run all tests (core + plugins)

test-pkg: ## Test specific package (usage: make test-pkg PKG=./pkg/api)
	@ $(MAKE) --no-print-directory log-$@
	@go test -v -race $(PKG)

test-report: ## Run tests with coverage report then open
	@ $(MAKE) --no-print-directory log-$@
	@go test -coverprofile=$(COVERAGE_OUT) ./...
	@go tool cover -html=$(COVERAGE_OUT)

test-coverage: ## Run tests with coverage report
	@ $(MAKE) --no-print-directory log-$@
	@go test -v -race -coverprofile=$(COVERAGE_OUT) ./...


## Code Quality

lint: ## Run golangci-lint
	@ $(MAKE) --no-print-directory log-$@
	@golangci-lint run ./...

lint-fix: ## Run golangci-lint with auto-fix
	@ $(MAKE) --no-print-directory log-$@
	@golangci-lint run --fix ./...

fmt: ## Format code with gofmt and goimports
	@ $(MAKE) --no-print-directory log-$@
	@gofmt -s -w .
	@goimports -w $(GOIMPORTS_LOCAL_ARG) .

clean: ## Remove build artifacts
	@ $(MAKE) --no-print-directory log-$@
	@rm -rf $(BIN_DIR)/
	@rm -f $(COVERAGE_OUT)
	@go clean -cache
	@find . -name "*.test" -delete
	@echo "✓ Build artifacts cleaned"

## Application Services

run: build ## Build and run the CLI
	@ $(MAKE) --no-print-directory log-$@
	@$(BIN_DIR)/$(PROJECT_NAME)

run-local: build ## Run exporter with local config
	@ $(MAKE) --no-print-directory log-$@
	@$(BIN_DIR)/$(PROJECT_NAME)

###############
##@ Development Dependencies

COMPOSE_ALL_PROFILES := --profile auth --profile tracing --profile metrics --profile logs

.PHONY: compose-up compose-up-auth compose-up-obs compose-up-full
.PHONY: compose-down compose-nuke compose-logs

compose-up: ## Start default dependencies (postgres + meilisearch)
	@ $(MAKE) --no-print-directory log-$@
	@docker compose up -d

compose-up-auth: ## Start default + keycloak (auth profile)
	@ $(MAKE) --no-print-directory log-$@
	@docker compose --profile auth up -d

compose-up-obs: ## Start default + tracing + metrics + logs
	@ $(MAKE) --no-print-directory log-$@
	@docker compose --profile tracing --profile metrics --profile logs up -d

compose-up-full: ## Start every profile
	@ $(MAKE) --no-print-directory log-$@
	@docker compose $(COMPOSE_ALL_PROFILES) up -d

compose-down: ## Stop compose services (keeps volumes)
	@ $(MAKE) --no-print-directory log-$@
	@docker compose $(COMPOSE_ALL_PROFILES) down

compose-nuke: ## Stop services and DELETE all volumes (use CONFIRM=1 to skip prompt)
	@ $(MAKE) --no-print-directory log-$@
	@if [ "$(CONFIRM)" != "1" ]; then \
		printf "This will DESTROY all compose volume data. Continue? [y/N] "; \
		read -r REPLY; \
		case "$$REPLY" in \
			[yY]|[yY][eE][sS]) ;; \
			*) echo "aborted."; exit 1 ;; \
		esac; \
	fi
	@docker compose $(COMPOSE_ALL_PROFILES) down -v
	@echo "✓ Compose volumes removed"

compose-logs: ## Tail compose logs (usage: make compose-logs SERVICE=postgres; omit SERVICE for all)
	@ $(MAKE) --no-print-directory log-$@
	@docker compose logs -f --tail=100 $(SERVICE)

###############
##@ Smoke Tests

BIN := $(BIN_DIR)/$(PROJECT_NAME)

.PHONY: smoke smoke-help smoke-version smoke-unknown smoke-serve smoke-work

smoke: smoke-help smoke-version smoke-unknown smoke-serve smoke-work ## Run every smoke test
	@echo "✓ all smoke tests passed"

smoke-help: build ## CLI with no args prints usage, exits 0
	@ $(MAKE) --no-print-directory log-$@
	@out=$$($(BIN) 2>&1); rc=$$?; \
		[ $$rc -eq 0 ] || { echo "✗ expected exit 0, got $$rc"; exit 1; }; \
		echo "$$out" | grep -q "Usage:" || { echo "✗ no Usage: in output"; exit 1; }; \
		echo "✓ help"

smoke-version: build ## `rfc-api version` prints version line, exits 0
	@ $(MAKE) --no-print-directory log-$@
	@out=$$($(BIN) version 2>&1); rc=$$?; \
		[ $$rc -eq 0 ] || { echo "✗ expected exit 0, got $$rc"; exit 1; }; \
		echo "$$out" | grep -qE "^rfc-api .+\\(.+\\)$$" || { echo "✗ unexpected version output: $$out"; exit 1; }; \
		echo "✓ version"

smoke-unknown: build ## Unknown subcommand exits 1
	@ $(MAKE) --no-print-directory log-$@
	@$(BIN) not-a-real-command >/dev/null 2>&1; rc=$$?; \
		[ $$rc -eq 1 ] || { echo "✗ expected exit 1, got $$rc"; exit 1; }; \
		echo "✓ unknown"

smoke-serve: build ## `rfc-api serve` handles SIGTERM cleanly (exit 0)
	@ $(MAKE) --no-print-directory log-$@
	@set -e; \
		DATABASE_URL=postgres://x:y@127.0.0.1:5432/z \
		MEILI_MASTER_KEY=smoke \
		RFC_API_WEBHOOK_SECRET=smoke \
		RFC_API_LISTEN=127.0.0.1:0 \
		RFC_API_ADMIN_LISTEN=127.0.0.1:0 \
		$(BIN) serve >/tmp/rfc-api.smoke-serve.log 2>&1 & \
		pid=$$!; \
		sleep 0.5; \
		kill -TERM $$pid; \
		wait $$pid; rc=$$?; \
		[ $$rc -eq 0 ] || { echo "✗ expected exit 0, got $$rc"; cat /tmp/rfc-api.smoke-serve.log; exit 1; }; \
		grep -q '"main server stopped"' /tmp/rfc-api.smoke-serve.log || { echo "✗ no 'main server stopped' log"; cat /tmp/rfc-api.smoke-serve.log; exit 1; }; \
		grep -q '"admin server stopped"' /tmp/rfc-api.smoke-serve.log || { echo "✗ no 'admin server stopped' log"; cat /tmp/rfc-api.smoke-serve.log; exit 1; }; \
		echo "✓ serve"

smoke-work: build ## `rfc-api work` handles SIGTERM cleanly (exit 0)
	@ $(MAKE) --no-print-directory log-$@
	@set -e; \
		$(BIN) work >/tmp/rfc-api.smoke-work.log 2>&1 & \
		pid=$$!; \
		sleep 0.5; \
		kill -TERM $$pid; \
		wait $$pid; rc=$$?; \
		[ $$rc -eq 0 ] || { echo "✗ expected exit 0, got $$rc"; cat /tmp/rfc-api.smoke-work.log; exit 1; }; \
		grep -q '"worker stopped"' /tmp/rfc-api.smoke-work.log || { echo "✗ no 'worker stopped' log"; cat /tmp/rfc-api.smoke-work.log; exit 1; }; \
		echo "✓ work"

## License Compliance

license-check: ## Check dependency licenses against allowed list
	@ $(MAKE) --no-print-directory log-$@
	@go-licenses check ./... --allowed_licenses=Apache-2.0,MIT,BSD-2-Clause,BSD-3-Clause,ISC,MPL-2.0

license-report: ## Generate CSV report of all dependency licenses
	@ $(MAKE) --no-print-directory log-$@
	@go-licenses report ./... --template=.github/licenses-csv.tpl

## CI/CD

ci: lint test build license-check ## Run CI pipeline (lint + test + build + license check)
	@ $(MAKE) --no-print-directory log-$@
	@echo "✓ CI pipeline complete"

check: lint test ## Quick pre-commit check (lint + test)
	@ $(MAKE) --no-print-directory log-$@
	@echo "✓ Pre-commit checks passed"

# =============================================================================
# Release Targets
# =============================================================================

release: ## Create release (use with TAG=v1.0.0)
	@ $(MAKE) --no-print-directory log-$@
	@if [ -z "$(TAG)" ]; then \
		echo "Error: TAG is required. Usage: make release TAG=v1.0.0"; \
		exit 1; \
	fi
	git tag -a $(TAG) -m "Release $(TAG)"
	git push origin $(TAG)

release-check:
	@ $(MAKE) --no-print-directory log-$@
	goreleaser check


release-local: ## Test goreleaser without publishing
	@ $(MAKE) --no-print-directory log-$@
	goreleaser release --snapshot --clean --skip=publish --skip=sign


########################################################################
## Self-Documenting Makefile Help                                     ##
## https://marmelab.com/blog/2016/02/29/auto-documented-makefile.html ##
########################################################################

########
##@ Help

.PHONY: help
help:   ## Display this help
	@awk -v "col=\033[36m" -v "nocol=\033[0m" ' \
		BEGIN { FS = ":.*##" ; printf "Usage:\n  make %s<target>%s\n\n", col, nocol } \
		/^[a-zA-Z_0-9-]+:.*?##/ { printf "  %s%-25s%s %s\n", col, $$1, nocol, $$2 } \
		/^##@/ { printf "\n%s%s%s\n", nocol, substr($$0, 5), nocol } \
	' $(MAKEFILE_LIST)

## Log Pattern
## Automatically logs what a target does by extracting its ## comment
log-%:
	@grep -h -E '^$*:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN { FS = ":.*?## " }; { printf "\033[36m==> %s\033[0m\n", $$2 }'
