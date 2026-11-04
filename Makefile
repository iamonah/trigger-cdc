SHELL := /bin/bash

-include .env

DATABASE_URL ?= postgres://cdc:cdc@localhost:5433/cdc?sslmode=disable
CDC_CHANNEL  ?= users_changes
MIGRATIONS   := ./migrations
TERN         ?= tern
ARGS         ?=

export DATABASE_URL
export CDC_CHANNEL

.PHONY: help up down logs psql migrate-up migrate-down migrate-status migrate-create \
        run-listener run-writer build fmt vet tidy reset

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(firstword $(MAKEFILE_LIST)) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-16s\033[0m %s\n", $$1, $$2}'

up: ## Start Postgres with Docker Compose
	docker compose up -d

down: ## Stop Postgres (keeps data)
	docker compose down

logs: ## Follow Postgres logs
	docker compose logs -f postgres

psql: ## Open a psql session
	psql "$(DATABASE_URL)"

migrate-up: ## Apply all migrations
	$(TERN) migrate --migrations $(MIGRATIONS) --conn-string "$(DATABASE_URL)"

migrate-down: ## Roll back the last migration
	$(TERN) migrate --migrations $(MIGRATIONS) --conn-string "$(DATABASE_URL)" --destination -1

migrate-status: ## Show migration status
	$(TERN) status --migrations $(MIGRATIONS) --conn-string "$(DATABASE_URL)"

migrate-create: ## Create a new migration (make migrate-create name=add_widgets)
	@test -n "$(name)" || (echo "usage: make migrate-create name=add_widgets" && exit 1)
	$(TERN) new --migrations $(MIGRATIONS) $(name)

run-listener: ## Run the CDC listener
	go run ./cmd/listener

run-writer: ## Run the writer (ARGS="create -name Ada" for one-off commands)
	go run ./cmd/writer $(ARGS)

build: ## Build listener and writer into ./bin
	go build -o bin/listener ./cmd/listener
	go build -o bin/writer ./cmd/writer

fmt: ## Format the Go code
	gofmt -w .

vet: ## Run go vet
	go vet ./...

tidy: ## Tidy go.mod/go.sum
	go mod tidy

reset: ## Stop Postgres and delete its data
	docker compose down -v
