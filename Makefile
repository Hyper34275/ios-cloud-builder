.PHONY: build test lint clean install all help

# Binary name
BINARY=builder

# Build directory
BUILD_DIR=build

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOTEST=$(GOCMD) test
GOFMT=$(GOCMD) fmt
GOVET=$(GOCMD) vet
GOMOD=$(GOCMD) mod

# Version info (can be overridden)
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT?=$(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME?=$(shell date -u '+%Y-%m-%d_%H:%M:%S')

# Build flags
LDFLAGS=-ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildTime=$(BUILD_TIME)"

## help: Show this help message
help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@sed -n 's/^##//p' $(MAKEFILE_LIST) | column -t -s ':' | sed -e 's/^/ /'

## all: Build and test
all: lint test build

## build: Build the binary
build:
	$(GOBUILD) $(LDFLAGS) -o $(BINARY) ./cmd/builder

## build-all: Build for all platforms
build-all: clean
	@mkdir -p $(BUILD_DIR)
	GOOS=darwin GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY)-darwin-amd64 ./cmd/builder
	GOOS=darwin GOARCH=arm64 $(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY)-darwin-arm64 ./cmd/builder
	GOOS=linux GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY)-linux-amd64 ./cmd/builder
	GOOS=linux GOARCH=arm64 $(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY)-linux-arm64 ./cmd/builder
	GOOS=windows GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY)-windows-amd64.exe ./cmd/builder

## test: Run tests
test:
	$(GOTEST) -v ./...

## test-race: Run tests with race detection
test-race:
	$(GOTEST) -race -v ./...

## test-cover: Run tests with coverage
test-cover:
	$(GOTEST) -coverprofile=coverage.out ./...
	$(GOCMD) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

## lint: Run linters
lint: fmt vet

## fmt: Format code
fmt:
	$(GOFMT) ./...

## vet: Run go vet
vet:
	$(GOVET) ./...

## clean: Remove build artifacts
clean:
	rm -f $(BINARY)
	rm -f $(BINARY).exe
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.html

## install: Install binary to GOPATH/bin (or ~/go/bin if GOPATH not set)
install:
	$(GOBUILD) $(LDFLAGS) -o $(or $(GOPATH),$(HOME)/go)/bin/$(BINARY) ./cmd/builder

## deps: Download dependencies
deps:
	$(GOMOD) download

## tidy: Tidy go.mod
tidy:
	$(GOMOD) tidy

## run: Run the CLI
run: build
	./$(BINARY)
