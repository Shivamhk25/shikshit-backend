# shikshit-backend — one entry point for the Go API and the OpenAPI contract.
.DEFAULT_GOAL := help
.PHONY: help install infra-up infra-down infra-logs api test check migrate-up migrate-status api-lint api-bundle api-gen-go api-gen-ts api-gen api-check mock

# Only the supporting services; the API runs on the host for fast reload.
INFRA_SERVICES := postgres pgbouncer redis minio mailhog
COMPOSE := docker-compose -f backend/docker-compose.yml

# Contract-first API (see backend/api/openapi/README.md). Tool versions are pinned.
OPENAPI_DIR := backend/api/openapi
OPENAPI_BUNDLE := $(OPENAPI_DIR)/openapi.bundled.yaml
REDOCLY := npx --yes @redocly/cli@1.34.24
OAPI_CODEGEN := go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.4.1
PRISM := npx --yes @stoplight/prism-cli@5.14.2
API_GENERATED := $(OPENAPI_BUNDLE) backend/internal/api/gen packages/api-client/src/generated

help: ## List available commands
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

install: ## Install tooling dependencies and Go modules
	npm install
	cd backend && go mod download

infra-up: ## Start Postgres, PgBouncer, Redis, MinIO and Mailhog (Docker)
	$(COMPOSE) up -d $(INFRA_SERVICES)

infra-down: ## Stop the infrastructure containers
	$(COMPOSE) stop $(INFRA_SERVICES)

infra-logs: ## Tail infrastructure logs
	$(COMPOSE) logs -f $(INFRA_SERVICES)

api: ## Run the Go API on :8080
	$(MAKE) -C backend run

test: ## Run backend and api-client tests
	$(MAKE) -C backend test
	npm test

check: test api-check ## Tests plus generated-code freshness (what CI runs)

migrate-up: ## Apply database migrations
	$(MAKE) -C backend migrate-up

migrate-status: ## Show migration status
	$(MAKE) -C backend migrate-status

api-lint: ## Lint the OpenAPI contract (every operation must declare its access rule)
	cd $(OPENAPI_DIR) && $(REDOCLY) lint openapi.yaml --config redocly.yaml

api-bundle: api-lint ## Bundle the multi-file contract into one spec
	cd $(OPENAPI_DIR) && $(REDOCLY) bundle openapi.yaml --config redocly.yaml -o openapi.bundled.yaml

api-gen-go: api-bundle ## Generate Go models, Echo routes and the strict server interface
	cd backend/api && $(OAPI_CODEGEN) -config oapi-codegen.yaml openapi/openapi.bundled.yaml

api-gen-ts: api-bundle ## Generate TypeScript types and Zod schemas for the clients
	npm run generate -w @shikshit/api-client

api-gen: api-gen-go api-gen-ts ## Regenerate everything from the contract

api-check: api-gen ## Fail if generated code differs from what is committed (CI)
	@test -z "$$(git status --porcelain -- $(API_GENERATED))" || { echo "Generated API code is out of date. Run 'make api-gen' and commit the result."; git status --short -- $(API_GENERATED); exit 1; }

mock: api-bundle ## Serve the contract as a mock API on :4010 (serves routes without the /api/v1 prefix)
	$(PRISM) mock -p 4010 -h 127.0.0.1 $(OPENAPI_BUNDLE)
