.DEFAULT_GOAL := help
SHELL := /bin/bash

# A local .env wins over the defaults below, so a machine that already runs
# something on 5432 or 8081 only has to set the ports there. docker compose
# reads the same file, so both stay in sync.
ifneq (,$(wildcard .env))
include .env
export
endif

# Local development connection strings. They match docker-compose.yml defaults.
POSTGRES_PORT ?= 5432
REDIS_PORT    ?= 6379
POSTGRES_URL ?= postgres://hoodalpha:hoodalpha@localhost:$(POSTGRES_PORT)/hoodalpha?sslmode=disable
REDIS_ADDR   ?= localhost:$(REDIS_PORT)
RH_RPC_URL   ?= https://rpc.mainnet.chain.robinhood.com
RH_CHAIN_ID  ?= 4663

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -X github.com/MuunBob/hoodalpha/internal/observability/buildinfo.Version=$(VERSION) \
           -X github.com/MuunBob/hoodalpha/internal/observability/buildinfo.Commit=$(COMMIT)

export POSTGRES_URL REDIS_ADDR RH_RPC_URL RH_CHAIN_ID

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

## ---------------------------------------------------------------- build

.PHONY: build
build: ## Build all binaries into ./bin
	go build -trimpath -ldflags "$(LDFLAGS)" -o ./bin/ ./cmd/...

.PHONY: fmt
fmt: ## Format all Go source
	gofmt -w .

.PHONY: fmt-check
fmt-check: ## Fail if any file is unformatted
	@out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then echo "unformatted files:"; echo "$$out"; exit 1; fi

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: lint
lint: ## Run golangci-lint when installed
	@if command -v golangci-lint >/dev/null 2>&1; then \
	  golangci-lint run ./...; \
	else \
	  echo "golangci-lint not installed; skipping (brew install golangci-lint)"; \
	fi

.PHONY: tidy
tidy: ## Sync go.mod/go.sum
	go mod tidy

## ---------------------------------------------------------------- test

.PHONY: test
test: ## Run unit tests (no external dependencies needed)
	go test -race ./internal/... ./migrations/...

.PHONY: test-integration
test-integration: ## Run integration tests against running infrastructure
	TEST_POSTGRES_URL="$(POSTGRES_URL)" \
	TEST_REDIS_ADDR="$(REDIS_ADDR)" \
	TEST_RH_RPC_URL="$(RH_RPC_URL)" \
	TEST_RH_WS_URL="$(RH_WS_URL)" \
	TEST_RH_CHAIN_ID="$(RH_CHAIN_ID)" \
	go test -count=1 -timeout 15m ./tests/integration/...

.PHONY: test-all
test-all: test test-integration ## Run every test

.PHONY: check
check: fmt-check vet test ## Everything CI runs on a pull request

## ---------------------------------------------------------------- database

.PHONY: migrate-up
migrate-up: ## Apply all pending migrations
	go run ./cmd/migrate up

.PHONY: migrate-down
migrate-down: ## Roll back exactly one migration
	go run ./cmd/migrate down

.PHONY: migrate-reset
migrate-reset: ## Roll back to version 0 (destroys all data)
	go run ./cmd/migrate reset

.PHONY: migrate-status
migrate-status: ## Show applied and pending migrations
	go run ./cmd/migrate status

.PHONY: db-recreate
db-recreate: migrate-reset migrate-up ## Rebuild the schema from zero

.PHONY: psql
psql: ## Open a psql shell on the development database
	docker compose exec postgres psql -U hoodalpha -d hoodalpha

## ---------------------------------------------------------------- run

.PHONY: up
up: ## Start infrastructure (postgres, redis, asynqmon) and apply migrations
	docker compose up -d postgres redis asynqmon
	@echo "waiting for postgres..."
	@until docker compose exec -T postgres pg_isready -U hoodalpha -d hoodalpha >/dev/null 2>&1; do sleep 1; done
	$(MAKE) migrate-up
	@echo "asynqmon: http://127.0.0.1:8081"

.PHONY: up-all
up-all: ## Start every service including api and worker
	docker compose up -d --build

.PHONY: down
down: ## Stop all services (volumes preserved)
	docker compose down

.PHONY: clean
clean: ## Stop all services and delete volumes
	docker compose down -v
	rm -rf ./bin

.PHONY: logs
logs: ## Tail service logs
	docker compose logs -f

.PHONY: run-api
run-api: ## Run the api process locally
	go run -ldflags "$(LDFLAGS)" ./cmd/api

.PHONY: run-worker
run-worker: ## Run the worker process locally
	go run -ldflags "$(LDFLAGS)" ./cmd/worker
