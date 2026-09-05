.PHONY: help build test lint fmt vuln evals ci

help: ## Show this help
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | \
		sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  %-8s %s\n", $$1, $$2}'

build: ## Compile all packages
	go build ./...

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

ci: ## Run everything CI blocks on
	$(MAKE) lint fmt test vuln evals

.DEFAULT_GOAL := help
