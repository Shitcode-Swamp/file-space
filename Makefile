# Quick commands for file-space. Run `make help` (or just `make`) to list
# them. See CLAUDE.md for the conventions behind these and REQUIREMENTS.md
# for the full spec.

SHELL := /bin/bash
ENV_FILE := .env
DB_CONTAINER := filespace-db

.DEFAULT_GOAL := help

.PHONY: help \
	backend-build backend-vet backend-test backend-test-smoke backend-fmt backend-run \
	frontend-install frontend-dev frontend-build frontend-lint \
	desktop-build desktop-run desktop-test \
	db-up db-down migrate-up migrate-down \
	build test fmt

help:
	@echo "Backend (Go, from backend/):"
	@echo "  make backend-build        go build ./..."
	@echo "  make backend-vet          go vet ./..."
	@echo "  make backend-test         go test ./... (no DB needed)"
	@echo "  make backend-test-smoke   go test -tags smoke ./... (needs SMOKE_DATABASE_URL)"
	@echo "  make backend-fmt          gofmt -l . (lists unformatted files)"
	@echo "  make backend-run          run the API against local Postgres (starts db-up + migrate-up first)"
	@echo ""
	@echo "Frontend (React/Vite, from frontend/):"
	@echo "  make frontend-install     npm install"
	@echo "  make frontend-dev         npm run dev"
	@echo "  make frontend-build       npm run build"
	@echo "  make frontend-lint        npm run lint"
	@echo ""
	@echo "Desktop (Swift/SwiftUI, from desktop/):"
	@echo "  make desktop-build        swift build"
	@echo "  make desktop-run          swift run"
	@echo "  make desktop-test         swift test (needs full Xcode.app, not just CLT)"
	@echo ""
	@echo "Local Postgres (docker) + migrations:"
	@echo "  make db-up                start/create the local 'filespace-db' container on :5432"
	@echo "  make db-down              stop and remove it"
	@echo "  make migrate-up           apply migrations/ to it"
	@echo "  make migrate-down         roll back the last migration"
	@echo ""
	@echo "Aggregate:"
	@echo "  make build                backend-build + frontend-build + desktop-build"
	@echo "  make test                 backend-test + desktop-test"
	@echo "  make fmt                  backend-fmt"

# --- Backend (Go) ---

backend-build:
	cd backend && go build ./...

backend-vet:
	cd backend && go vet ./...

backend-test:
	cd backend && go test ./...

backend-test-smoke:
	cd backend && go test -tags smoke ./...

backend-fmt:
	cd backend && gofmt -l .

# Runs the API natively against the dockerized Postgres from db-up, with
# STORAGE_DIR overridden to a local relative path (the .env value is the
# container path used by docker-compose's own api service, not this one).
backend-run: db-up migrate-up
	set -a; source $(ENV_FILE); set +a; \
	export DATABASE_URL="$${DATABASE_URL/@db:/@localhost:}"; \
	export STORAGE_DIR=./data; \
	cd backend && go run ./cmd/api

# --- Frontend (React/Vite) ---

frontend-install:
	cd frontend && npm install

frontend-dev:
	cd frontend && npm run dev

frontend-build:
	cd frontend && npm run build

frontend-lint:
	cd frontend && npm run lint

# --- Desktop (Swift/SwiftUI) ---

desktop-build:
	cd desktop && swift build

desktop-run:
	cd desktop && swift run

desktop-test:
	cd desktop && swift test

# --- Local Postgres (docker) + migrations ---

# Starts the existing container if present, otherwise creates it fresh from
# .env's POSTGRES_* / DATABASE_URL values, published on localhost:5432.
db-up:
	@docker start $(DB_CONTAINER) >/dev/null 2>&1 || \
	docker run -d --name $(DB_CONTAINER) --env-file $(ENV_FILE) -p 5432:5432 \
		-v filespace_pgdata:/var/lib/postgresql/data postgres:16 >/dev/null
	@echo "postgres listening on localhost:5432 (container: $(DB_CONTAINER))"

db-down:
	docker rm -f $(DB_CONTAINER)

migrate-up:
	set -a; source $(ENV_FILE); set +a; \
	migrate -path migrations -database "$${DATABASE_URL/@db:/@localhost:}" up

migrate-down:
	set -a; source $(ENV_FILE); set +a; \
	migrate -path migrations -database "$${DATABASE_URL/@db:/@localhost:}" down 1

# --- Aggregate ---

build: backend-build frontend-build desktop-build

test: backend-test desktop-test

fmt: backend-fmt
