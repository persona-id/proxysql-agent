BINARY := proxysql-agent

.PHONY: help bench build check clean coverage coverage-html fmt lint run test

default: help

bench: ## Run benchmarks
	@go test --race --shuffle=on --bench=. --benchmem ./...

build: ## Build the application
	@go build -o $(BINARY) cmd/proxysql-agent/main.go

check: fmt lint test ## Run formatting (via golangci-lint), vetting (also via golangci-lint), linting, tests, benchmarks, and race detection

clean: ## Clean build artifacts and coverage files
	@go clean
	@rm -rf dist coverage $(BINARY)

coverage: ## Generate test coverage report
	@mkdir -p coverage
	@go test --race --shuffle=on --coverprofile=coverage/coverage.out ./...
	@go tool cover --func=coverage/coverage.out

coverage-html: coverage ## Generate HTML coverage report and open in browser
	@go tool cover --html=coverage/coverage.out -o coverage/coverage.html
	@open coverage/coverage.html

docker: clean lint
	@docker build -f build/dev.Dockerfile -t persona-id/proxysql-agent:latest .

fmt: ## Format code
	@golangci-lint-v2 fmt ./...

help: ## Show this help message
	@echo "Available targets:"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-15s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

lint: ## Run golangci-lint
	@golangci-lint-v2 run

run: clean build ## Run the application. not really useful outside of a k8s cluster.
	@./$(BINARY) --log.level=DEBUG --proxysql.password=radmin --run_mode=satellite --shutdown.draining_file=/tmp/proxysql-draining

test: ## Run tests
	@go test --race --shuffle=on ./...
