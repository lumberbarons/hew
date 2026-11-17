.PHONY: help build test lint fmt vuln evals consumer daemon web web-e2e daemon-build daemon-run ci

help: ## Show this help
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | \
		sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  %-8s %s\n", $$1, $$2}'

build: ## Build the hew binary into dist/
	mkdir -p dist
	go build -o dist/hew ./cmd/hew

test: ## Run the full test suite with the race detector
	go test -race ./...

lint: ## Run golangci-lint (CI-blocking)
	golangci-lint run

fmt: ## Check formatting; run `golangci-lint fmt` to fix in place
	golangci-lint fmt --diff

vuln: ## Scan for known vulnerabilities (CI-blocking)
	govulncheck ./...

evals: ## Test and lint the evals module
	cd evals && go test -race ./...
	cd evals && golangci-lint run
	cd evals && golangci-lint fmt --diff

consumer: ## Test and lint the external-consumer module
	cd consumer && go mod tidy -diff
	cd consumer && go test -race ./...
	cd consumer && golangci-lint run
	cd consumer && golangci-lint fmt --diff

daemon: ## Test, lint and coverage-gate the daemon module
	cd daemon && go mod tidy -diff
	cd daemon && go test -race -coverprofile=coverage-daemon.out ./...
	go run github.com/vladopajic/go-test-coverage/v2@v2.19.0 --config=./daemon/.testcoverage.yml --source-dir=./daemon
	cd daemon && golangci-lint run
	cd daemon && golangci-lint fmt --diff

web: ## Typecheck, lint, test (90% floor) and build the daemon's web UI
	cd daemon/web && npm ci
	cd daemon/web && npm run typecheck
	cd daemon/web && npm run lint
	cd daemon/web && npm run format
	cd daemon/web && npm run test:coverage
	cd daemon/web && npm run build

web-e2e: web ## Browser end-to-end specs (once: cd daemon/web && npx playwright install chromium)
	cd daemon/web && npm run e2e

daemon-build: web ## Build dist/hewd with the web UI embedded
	mkdir -p dist
	cd daemon && go build -o ../dist/hewd ./cmd/hewd

daemon-run: daemon-build ## Build and serve this checkout at http://127.0.0.1:7777
	dist/hewd --dir $(CURDIR) $(HEWD_ARGS)

ci: ## Run everything CI blocks on (web-e2e needs a browser, so it runs there)
	$(MAKE) lint fmt test vuln evals consumer daemon web

.DEFAULT_GOAL := help
