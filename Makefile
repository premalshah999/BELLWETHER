# Common development tasks. `make help` lists them.

GO      ?= go
NPM     ?= npm
BIN     ?= tradesys

.PHONY: help dev-db web web-dev run build test test-race vet up down logs clean

help: ## List targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-10s %s\n", $$1, $$2}'

dev-db: ## Start only Postgres (127.0.0.1:5433) for local development
	docker compose up -d postgres

web/node_modules: web/package-lock.json
	$(NPM) --prefix web ci
	@touch $@

web: web/node_modules ## Build the frontend into web/dist (embedded by the Go binary)
	$(NPM) --prefix web run build

web-dev: web/node_modules ## Frontend dev server on :5173, proxying /api to :8080
	$(NPM) --prefix web run dev

build: web ## Build the tradesys binary
	$(GO) build -o $(BIN) ./cmd/tradesys

run: build ## Build and run the backend on :8080 (reads .env)
	./$(BIN)

vet: ## go vet
	$(GO) vet ./...

test: web vet ## Frontend typecheck/build, go vet, and Go tests
	$(GO) test ./...

test-race: web vet ## As test, with the race detector (what CI runs)
	$(GO) test -race ./...

up: ## Start the full stack with Docker Compose
	docker compose up -d --build

down: ## Stop the stack, keeping data volumes
	docker compose down

logs: ## Follow the app logs
	docker compose logs -f tradesys

clean: ## Remove build output
	rm -f $(BIN)
	find web/dist -mindepth 1 ! -name .gitkeep -delete
