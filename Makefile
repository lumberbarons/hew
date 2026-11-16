.PHONY: help build test lint fmt vuln evals consumer daemon ci

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

daemon: ## Test, lint and coverage-gate the hew-daemon module
	cd daemon && go mod tidy -diff
	cd daemon && go test -race -coverprofile=coverage-daemon.out ./...
	go run github.com/vladopajic/go-test-coverage/v2@v2.19.0 --config=./daemon/.testcoverage.yml --source-dir=./daemon
	cd daemon && golangci-lint run
	cd daemon && golangci-lint fmt --diff

ci: ## Run everything CI blocks on
	$(MAKE) lint fmt test vuln evals consumer daemon

.DEFAULT_GOAL := help
