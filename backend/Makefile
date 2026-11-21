# PIE360 Backend Makefile

.PHONY: help build run test lint fmt vet tidy migrate-up migrate-down migrate-create seed sqlc docker-up docker-down docker-logs clean

# Variables
BINARY_NAME := server
BUILD_DIR := ./build
CMD_DIR := ./cmd/api
CONFIG_FILE := config.yaml
MIGRATIONS_DIR := ./migrations
# golang-migrate (file pairs NNNNNN_name.up.sql / .down.sql). Pinned. Override DB_URL for other databases.
MIGRATE := go run -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@v4.18.1
DB_URL ?= postgres://pie360:pie360_dev_password@localhost:5432/pie360?sslmode=disable

# Go parameters
GO := go
GOFLAGS := -ldflags="-s -w"
GOTESTFLAGS := -v -race -count=1

# Docker parameters
DOCKER_COMPOSE := docker-compose
DOCKER_COMPOSE_FILE := docker-compose.yml

# Default target
.DEFAULT_GOAL := help

help: ## Show this help message
	@echo "PIE360 Backend - Available commands:"
	@echo ""
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

# Build
build: ## Build the application
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) $(CMD_DIR)
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)"

build-prod: ## Build for production
	@echo "Building $(BINARY_NAME) for production..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) $(CMD_DIR)
	@echo "Production build complete: $(BUILD_DIR)/$(BINARY_NAME)"

# Run
run: ## Run the application locally
	@echo "Starting server..."
	$(GO) run $(CMD_DIR)

run-dev: ## Run with live reload (requires air)
	@which air > /dev/null || (echo "Installing air..." && go install github.com/air-verse/air@latest)
	air -c .air.toml

# Testing
test: ## Run all tests
	$(GO) test $(GOTESTFLAGS) ./...

test-unit: ## Run unit tests only
	$(GO) test $(GOTESTFLAGS) -short ./...

test-integration: ## Run integration tests
	$(GO) test $(GOTESTFLAGS) -run Integration ./...

test-coverage: ## Run tests with coverage
	$(GO) test $(GOTESTFLAGS) -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

test-watch: ## Run tests in watch mode (requires entr)
	find . -name "*.go" | entr -c $(GO) test $(GOTESTFLAGS) ./...

# Code quality
lint: ## Run linter (requires golangci-lint)
	@which golangci-lint > /dev/null || (echo "Installing golangci-lint..." && go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest)
	golangci-lint run ./...

fmt: ## Format code
	$(GO) fmt ./...
	@which gofumpt > /dev/null && gofumpt -w . || true

vet: ## Run go vet
	$(GO) vet ./...

tidy: ## Tidy go modules
	$(GO) mod tidy
	$(GO) mod verify

check: fmt vet lint test ## Run all checks

# Database
migrate-up: ## Apply all pending database migrations
	$(MIGRATE) -path $(MIGRATIONS_DIR) -database "$(DB_URL)" up

migrate-down: ## Roll back the last migration
	$(MIGRATE) -path $(MIGRATIONS_DIR) -database "$(DB_URL)" down 1

migrate-status: ## Show the current migration version
	$(MIGRATE) -path $(MIGRATIONS_DIR) -database "$(DB_URL)" version

migrate-create: ## Create a new migration pair (usage: make migrate-create NAME=add_something)
	@if [ -z "$(NAME)" ]; then echo "Usage: make migrate-create NAME=migration_name"; exit 1; fi
	$(MIGRATE) create -ext sql -dir $(MIGRATIONS_DIR) -seq -digits 6 $(NAME)

seed: ## Create the demo institute (idempotent). Run `make migrate-up` first
	DATABASE_URL="$(DB_URL)" $(GO) run ./cmd/seed

# SQLC
sqlc: ## Generate SQLC code
	@which sqlc > /dev/null || (echo "Installing sqlc..." && go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest)
	sqlc generate -f sqlc.yaml

sqlc-verify: ## Verify SQLC queries
	@which sqlc > /dev/null || (echo "Installing sqlc..." && go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest)
	sqlc vet -f sqlc.yaml

# Docker
docker-up: ## Start all services with docker-compose
	$(DOCKER_COMPOSE) -f $(DOCKER_COMPOSE_FILE) up -d

docker-down: ## Stop all services
	$(DOCKER_COMPOSE) -f $(DOCKER_COMPOSE_FILE) down

docker-down-volumes: ## Stop all services and remove volumes
	$(DOCKER_COMPOSE) -f $(DOCKER_COMPOSE_FILE) down -v

docker-build: ## Build Docker image
	$(DOCKER_COMPOSE) -f $(DOCKER_COMPOSE_FILE) build

docker-logs: ## Show logs for all services
	$(DOCKER_COMPOSE) -f $(DOCKER_COMPOSE_FILE) logs -f

docker-logs-api: ## Show logs for API service
	$(DOCKER_COMPOSE) -f $(DOCKER_COMPOSE_FILE) logs -f api

docker-ps: ## Show running containers
	$(DOCKER_COMPOSE) -f $(DOCKER_COMPOSE_FILE) ps

docker-shell-api: ## Open shell in API container
	$(DOCKER_COMPOSE) -f $(DOCKER_COMPOSE_FILE) exec api sh

docker-shell-db: ## Open shell in PostgreSQL
	$(DOCKER_COMPOSE) -f $(DOCKER_COMPOSE_FILE) exec postgres psql -U pie360 -d pie360

docker-shell-redis: ## Open shell in Redis
	$(DOCKER_COMPOSE) -f $(DOCKER_COMPOSE_FILE) exec redis redis-cli

# Development
dev-setup: ## Setup development environment
	@echo "Setting up development environment..."
	@which goose > /dev/null || go install github.com/pressly/goose/v3/cmd/goose@latest
	@which sqlc > /dev/null || go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
	@which golangci-lint > /dev/null || go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	@which air > /dev/null || go install github.com/air-verse/air@latest
	@which mockgen > /dev/null || go install github.com/golang/mock/mockgen@latest
	$(GO) mod download
	@echo "Development environment ready!"

dev-db: ## Start only database services
	$(DOCKER_COMPOSE) -f $(DOCKER_COMPOSE_FILE) up -d postgres pgbouncer redis minio mailhog

dev-logs: ## Show development logs
	$(DOCKER_COMPOSE) -f $(DOCKER_COMPOSE_FILE) logs -f postgres pgbouncer redis

# Keys
generate-keys: ## Generate JWT keys
	@mkdir -p keys
	@openssl genrsa -out keys/private.pem 2048
	@openssl rsa -in keys/private.pem -pubout -out keys/public.pem
	@chmod 600 keys/private.pem
	@chmod 644 keys/public.pem
	@echo "Keys generated in ./keys/"

# Clean
clean: ## Clean build artifacts
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.html
	$(GO) clean -cache -modcache -testcache

# Database utilities
db-shell: ## Open PostgreSQL shell
	$(DOCKER_COMPOSE) -f $(DOCKER_COMPOSE_FILE) exec postgres psql -U pie360 -d pie360

db-backup: ## Backup database
	@mkdir -p backups
	$(DOCKER_COMPOSE) -f $(DOCKER_COMPOSE_FILE) exec -T postgres pg_dump -U pie360 pie360 > backups/pie360_$(shell date +%Y%m%d_%H%M%S).sql
	@echo "Backup saved to backups/"

db-restore: ## Restore database (usage: make db-restore FILE=backup.sql)
	@if [ -z "$(FILE)" ]; then echo "Usage: make db-restore FILE=backup.sql"; exit 1; fi
	$(DOCKER_COMPOSE) -f $(DOCKER_COMPOSE_FILE) exec -T postgres psql -U pie360 -d pie360 < $(FILE)

# Swagger/OpenAPI
swagger: ## Generate Swagger docs (requires swag)
	@which swag > /dev/null || go install github.com/swaggo/swag/cmd/swag@latest
	swag init -g $(CMD_DIR)/main.go -o ./docs

# Benchmarks
bench: ## Run benchmarks
	$(GO) test -bench=. -benchmem ./...

# Profile
profile-cpu: ## Generate CPU profile
	$(GO) test -cpuprofile=cpu.prof -bench=. ./...
	$(GO) tool pprof cpu.prof

profile-mem: ## Generate memory profile
	$(GO) test -memprofile=mem.prof -bench=. ./...
	$(GO) tool pprof mem.prof

# Security
security-scan: ## Run security scan (requires gosec)
	@which gosec > /dev/null || go install github.com/securego/gosec/v2/cmd/gosec@latest
	gosec ./...

# Release
release-patch: ## Create patch release
	@git tag -a v$(shell git describe --tags --abbrev=0 | awk -F. '{print $$1"."$$2"."$$3+1}') -m "Patch release"
	@git push origin --tags

release-minor: ## Create minor release
	@git tag -a v$(shell git describe --tags --abbrev=0 | awk -F. '{print $$1"."$$2+1".0"}') -m "Minor release"
	@git push origin --tags

release-major: ## Create major release
	@git tag -a v$(shell git describe --tags --abbrev=0 | awk -F. '{print $$1+1".0.0"}') -m "Major release"
	@git push origin --tags

# Install tools
install-tools: ## Install all development tools
	go install github.com/pressly/goose/v3/cmd/goose@latest
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	go install github.com/air-verse/air@latest
	go install github.com/golang/mock/mockgen@latest
	go install github.com/swaggo/swag/cmd/swag@latest
	go install github.com/securego/gosec/v2/cmd/gosec@latest
	@echo "All tools installed!"

# Default: show help
all: check build