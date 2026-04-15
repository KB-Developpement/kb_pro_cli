# kb — KB-Developpement Frappe app installer

BINARY    := kb
BINARY_DIR := bin
CMD_PATH  := ./cmd/kb

# Build-time version injection
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X github.com/KB-Developpement/kb_pro_cli/internal/version.Version=$(VERSION) \
	-X github.com/KB-Developpement/kb_pro_cli/internal/version.Commit=$(COMMIT) \
	-X github.com/KB-Developpement/kb_pro_cli/internal/version.Date=$(DATE)

.PHONY: build install clean test tidy vet fmt help

## build: compile linux/amd64 binary to ./bin/kb
build:
	@mkdir -p $(BINARY_DIR)
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o ./$(BINARY_DIR)/$(BINARY) $(CMD_PATH)

## install: install binary to $GOPATH/bin
install:
	go install -ldflags "$(LDFLAGS)" $(CMD_PATH)

## test: run all tests with race detector
test:
	go test -race ./...

## tidy: tidy and verify module dependencies
tidy:
	go mod tidy
	go mod verify

## vet: run go vet
vet:
	go vet ./...

## fmt: format all Go files
fmt:
	gofmt -w .

## clean: remove compiled binary
clean:
	rm -f ./$(BINARY_DIR)/$(BINARY)

## help: print this help
help:
	@sed -n 's/^##//p' $(MAKEFILE_LIST) | column -t -s ':' | sed -e 's/^/ /'
