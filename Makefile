# MFGraph developer tasks. Requires Go (see go.mod); Docker only for the
# MySQL-backed targets (dev-db-*, test-mysql).

GO      ?= go
DOCKER  ?= docker
COMPOSE ?= $(DOCKER) compose -f deploy/docker-compose.dev.yml
BIN     ?= bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: all build test vet fmt fmt-check clean dev-db-up dev-db-down dev-db-reset \
	vectors-verify vectors-review

all: build test

## build: compile every package and the two binaries into ./bin
build:
	$(GO) build ./...
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BIN)/mfgraph-server ./cmd/mfgraph-server
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BIN)/mfgraph ./cmd/mfgraph

## test: unit tests (no database, no network)
test:
	$(GO) test ./...

## vet: go vet
vet:
	$(GO) vet ./...

## fmt / fmt-check: gofmt
fmt:
	gofmt -w $$(git ls-files '*.go')

fmt-check:
	@out=$$(gofmt -l $$(git ls-files '*.go')); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

## vectors-verify: independent Python cross-check of testdata/vectors
vectors-verify:
	python3 scripts/vectors.py verify

## vectors-review: regenerate testdata/vectors/REVIEW.md
vectors-review:
	python3 scripts/vectors.py review

## dev-db-up / dev-db-down / dev-db-reset: local MySQL 8.0 via Docker
dev-db-up:
	$(COMPOSE) up -d --wait mysql

dev-db-down:
	$(COMPOSE) down

dev-db-reset:
	$(COMPOSE) down -v

clean:
	rm -rf $(BIN)
