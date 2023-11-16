SHELL := /bin/bash

# The name of the executable (default is current directory name)
TARGET := $(shell echo $${PWD\#\#*/})

# These will be provided to the target
VERSION := 0.1.0
BUILD := `git rev-parse HEAD`
BUILD_TIME=`date +%FT%T%z`

# Use linker flags to provide version/build settings to the target.
# If we don't need debugging symbols, add -s and -w to make a smaller binary
LDFLAGS=-ldflags "-X=main.Version=$(VERSION) -X=main.Build=$(BUILD) -X=main.BuildTime=$BUILD_TIME)"

# go source files, ignore vendor directory
SRC=$(shell find . -type f -name '*.go')

all: clean build

$(TARGET): $(SRC)
	@go build $(LDFLAGS) -o $(TARGET)

build: clean $(TARGET)
	@true

clean:
	@rm -f $(TARGET)

lint:
	@gofmt -s -l -w .
	@go vet ./...

# cross compile for linux
linux: clean $(TARGET)
	@GOOS="linux" GOARCH="amd64" go build $(LDFLAGS) -o $(TARGET) .

run: build
	@./$(TARGET)

docker: clean
	@docker build . -t proxysql-agent
