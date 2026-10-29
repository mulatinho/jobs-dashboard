GO ?= go
DOCKER ?= docker
IMAGE ?= jobs-dashboard:local
BIN_DIR ?= bin
BINARY := $(BIN_DIR)/jobs-dashboard

.PHONY: all build check clean fmt fmt-check image race test vet

all: build

build:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="-s -w" -o $(BINARY) .

check: fmt-check vet test
	$(GO) mod verify

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w $$(find . -type f -name '*.go' -not -path './vendor/*')

fmt-check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }

image:
	$(DOCKER) build -t $(IMAGE) .

clean:
	rm -rf $(BIN_DIR) coverage.out coverage.html
