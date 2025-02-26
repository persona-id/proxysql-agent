SHELL := /bin/bash

# The name of the executable
TARGET := 'proxysql-agent'

# Use linker flags to provide version/build settings to the target.
LDFLAGS=-ldflags "-s -w"

all: clean lint build

$(TARGET):
	@go build $(LDFLAGS) -o $(TARGET) cmd/proxysql-agent/main.go

build: clean $(TARGET)
	@true

clean:
	@rm -rf $(TARGET) *.test *.out tmp/* coverage dist

lint:
	@gofmt -s -l -w .
	@go vet ./...
	@golangci-lint run --config=.golangci.yml --allow-parallel-runners

test:
	@mkdir -p coverage
	@go test ./... --shuffle=on --coverprofile coverage/coverage.out

# If the REMOTE_CONTAINERS environment variable is set to true (ie in devcontainers), just output the html file to disk
coverage: test
	@if [ "$(REMOTE_CONTAINERS)" = "true" ]; then \
		go tool cover -html=coverage/coverage.out -o=coverage/coverage.html; \
	else \
		go tool cover -html=coverage/coverage.out; \
	fi

run: build
	@./$(TARGET)

docker: clean lint
	@docker build -f build/dev.Dockerfile -t persona-id/proxysql-agent:latest .

snapshot: clean lint
	@goreleaser --snapshot --clean

release: clean lint
	@goreleaser --clean
