.PHONY: build install test lint check run clean mutate mutate-diff

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X main.Version=$(VERSION)"
INSTALL_DIR := $(HOME)/.local/bin
GOLANGCI_LINT_VERSION := v2.13.2

build:
	go build $(LDFLAGS) -o .local/bin/vroom ./cmd/vroom

install: build
	cp .local/bin/vroom $(INSTALL_DIR)/vroom

run:
	go run ./cmd/vroom

test:
	go test -race -count=1 -cover ./...

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run

check: build lint test

mutate:
	go tool gremlins unleash --workers 4 --timeout-coefficient 3 --output report.json

mutate-diff:
	go tool gremlins unleash --diff main --workers 4 --timeout-coefficient 3 --output report.json

clean:
	rm -rf .local/bin/
