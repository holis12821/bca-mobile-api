.PHONY: help setup keys dev run build infra-up infra-down infra-reset \
        migrate-create migrate-up migrate-down migrate-status verify-009 ledger-check \
        seed test test-verbose test-coverage test-concurrent lint vet check \
        docker-build prod-migrate prod-up prod-down prod-logs backup clean

DB_URL ?= postgres://bcamobile:localdev_password_123@localhost:5432/bcamobile?sslmode=disable
COMPOSE_DEV  := docker compose -f deployments/docker-compose.yml
COMPOSE_PROD := docker compose -f deployments/docker-compose.prod.yml --env-file .env.prod

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

# === Setup ===

setup: ## First-time project setup
	@echo "=== Setting up BCA Mobile API ==="
	cp -n .env.example .env || true
	$(MAKE) keys
	$(MAKE) infra-up
	@echo "waiting for postgres..."
	@until $(COMPOSE_DEV) exec -T postgres pg_isready -U bcamobile >/dev/null 2>&1; do sleep 1; done
	$(MAKE) migrate-up
	$(MAKE) seed
	@echo "=== Done. Run 'make dev' ==="

keys: ## Generate RSA key pairs for JWT and PIN encryption
	@mkdir -p keys
	openssl genrsa -out keys/private.pem 2048
	openssl rsa -in keys/private.pem -pubout -out keys/public.pem
	openssl genrsa -out keys/pin_private.pem 2048
	openssl rsa -in keys/pin_private.pem -pubout -out keys/pin_public.pem
	@chmod 600 keys/*.pem
	@echo "Keys in ./keys/ (mode 600, gitignored)"

# === Development ===

dev: ## Start dev server with hot reload
	air

run: ## Run server without hot reload
	go run ./cmd/server

build: ## Build production binary
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/server ./cmd/server

# === Infrastructure ===

infra-up: ## Start Postgres + both Redis instances
	$(COMPOSE_DEV) up -d

infra-down: ## Stop local infra
	$(COMPOSE_DEV) down

infra-reset: ## Destroy all local data and rebuild (destructive)
	$(COMPOSE_DEV) down -v
	$(MAKE) infra-up
	@until $(COMPOSE_DEV) exec -T postgres pg_isready -U bcamobile >/dev/null 2>&1; do sleep 1; done
	$(MAKE) migrate-up
	$(MAKE) seed

# === Database ===

migrate-create: ## Create a migration (make migrate-create NAME=add_foo)
	migrate create -ext sql -dir migrations -seq $(NAME)

migrate-up: ## Apply all pending migrations
	migrate -path migrations -database "$(DB_URL)" up

migrate-down: ## Roll back the last migration
	migrate -path migrations -database "$(DB_URL)" down 1

migrate-status: ## Show current migration version
	migrate -path migrations -database "$(DB_URL)" version

verify-009: ## Run the behavioural checks for migration 000009
	@psql "$(DB_URL)" -v ON_ERROR_STOP=1 -f scripts/000009_verify.sql 2>&1 | grep -E 'PASS|FAIL'

ledger-check: ## Ledger invariants — both must return zero rows
	@psql "$(DB_URL)" -c "SELECT * FROM v_unbalanced_transactions;"
	@psql "$(DB_URL)" -c "SELECT * FROM v_ledger_reconciliation;"

seed: ## Seed the database with demo data
	go run ./scripts/seed

# === Quality ===

test: ## Run all tests with race detector
	go test -race -cover ./...

test-verbose: ## Verbose test run
	go test -race -cover -v ./...

test-coverage: ## HTML coverage report
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Report: coverage.html"

test-concurrent: ## Hammer the concurrency tests — flakiness here is a real bug
	go test -race -count=20 -run Concurrent ./internal/domain/transaction/...

lint: ## Run golangci-lint
	golangci-lint run ./...

vet: ## Run go vet
	go vet ./...

check: lint vet test ## lint + vet + test

# === Production (single VM) ===

docker-build: ## Build the production image
	docker build -f deployments/Dockerfile -t bca-mobile-api:$${APP_VERSION:-latest} .

prod-migrate: ## Run migrations against production (separate step, before prod-up)
	$(COMPOSE_PROD) run --rm migrate

prod-up: ## Start the production stack
	$(COMPOSE_PROD) up -d

prod-down: ## Stop the production stack
	$(COMPOSE_PROD) down

prod-logs: ## Tail API logs
	$(COMPOSE_PROD) logs -f api

backup: ## Encrypted pg_dump into deployments/backups/
	@mkdir -p deployments/backups
	$(COMPOSE_PROD) exec -T postgres pg_dump -U $${DB_USER} $${DB_NAME} \
		| gpg --symmetric --cipher-algo AES256 \
		> deployments/backups/bcamobile-$$(date -u +%Y%m%dT%H%M%SZ).sql.gpg
	@echo "A backup you have never restored is not a backup. Test it."

# === Cleanup ===

clean: ## Remove build artifacts
	rm -rf bin/ tmp/ coverage.out coverage.html
