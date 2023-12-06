SHELL := /bin/bash

# The name of the executable
TARGET := 'proxysql-agent'

# Use linker flags to provide version/build settings to the target.
# If we don't need debugging symbols, add -s and -w to make a smaller binary
LDFLAGS=-ldflags "-s -w"

# go source files
SRC=$(shell find . -type f -name '*.go')

all: clean lint build

$(TARGET): $(SRC)
	@go build $(LDFLAGS) -o $(TARGET) cmd/proxysql-agent/main.go

build: clean $(TARGET)
	@true

clean:
	@rm -rf $(TARGET) coverage tmp/*

lint:
	@gofmt -s -l -w .
	@go vet ./...
	@golangci-lint run --config=.golangci.yml --allow-parallel-runners

test:
	@mkdir -p coverage
	@go test ./... -v -shuffle=on -coverprofile coverage/coverage.out

coverage: test
	@go tool cover -html=coverage/coverage.out

# cross compile for linux
linux: clean $(TARGET)
	@GOOS="linux" GOARCH="amd64" go build $(LDFLAGS) -o $(TARGET) .

run: build
	@./$(TARGET)

docker: clean lint
	@docker build --build-arg="VERSION=${VERSION}" --build-arg="BUILD_TIME=${BUILD_TIME}" --build-arg="BUILD_SHA=${BUILD_SHA}" -f build/dev.Dockerfile . -t proxysql-agent
