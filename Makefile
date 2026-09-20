.DEFAULT_GOAL := help

.PHONY: help dev api web db-up db-down migrate sqlc seed test e2e check build

# The database the seeded collection and the browser tests use. A second
# PostgreSQL, on its own port, because seeding empties every table it finds.
#
# The default is `override` so that nothing outside this file can move it, and
# the working variable is not: `seed` names the default to the seeder as the
# one database it may empty, and passes the working one as the target, so the
# two ends of that check stay independent. `make seed E2E_DATABASE_URL=…` is
# then refused by the seeder, naming both databases, rather than confirmed by
# this file — seeding anything else is a thing to do directly, naming it.
override E2E_DATABASE_URL_DEFAULT := postgres://schall:schall@localhost:5433/schall_e2e?sslmode=disable
E2E_DATABASE_URL := $(E2E_DATABASE_URL_DEFAULT)

help:
	@awk 'BEGIN {FS = ":.*##"; printf "Schall development commands:\n"} /^[a-zA-Z0-9_-]+:.*?##/ {printf "  %-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

dev: ## Start PostgreSQL, API, and web development servers
	docker compose up -d postgres
	@trap 'kill 0' INT TERM EXIT; $(MAKE) api & $(MAKE) web & wait

api: ## Run the Go API
	go run ./cmd/schall

web: ## Run the SvelteKit development server
	npm --prefix web run dev

db-up: ## Start PostgreSQL
	docker compose up -d postgres

db-down: ## Stop local services
	docker compose down

migrate: ## Apply database migrations
	go run github.com/pressly/goose/v3/cmd/goose@v3.24.1 -dir internal/migrations postgres "$${SCHALL_DATABASE_URL}" up

sqlc: ## Generate typed database queries
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate

# --expect names the one database this target may empty, and the seeder checks
# the target against it rather than taking this file's word. It is also what
# lets the line printed below stay true — exporting this database as the one to
# click through no longer stops you refilling it, because naming a database is
# how you say emptying it is what you meant.
seed: ## Fill the test database with the seeded collection
	docker compose up -d postgres-test
	SCHALL_SEED_DATABASE_URL="$(E2E_DATABASE_URL)" go run ./cmd/schall-seed --expect "$(E2E_DATABASE_URL_DEFAULT)"
	@echo
	@echo "Seeded. To click through it, point Schall at that database:"
	@echo "  SCHALL_DATABASE_URL='$(E2E_DATABASE_URL)' go run ./cmd/schall"

test: ## Run backend and frontend tests
	go test ./...
	npm --prefix web run check
	npm --prefix web run test

e2e: ## Run the browser tests against a freshly seeded database
	docker compose up -d postgres-test
	SCHALL_E2E_DATABASE_URL="$(E2E_DATABASE_URL)" npm --prefix web run test:e2e

check: ## Format and statically check the project
	./scripts/check-env-example.sh
	gofmt -w $$(find cmd internal -name '*.go')
	go vet ./...
	npm --prefix web run check
	npm --prefix web run test

build: ## Build the production container
	docker build -t schall:local .
