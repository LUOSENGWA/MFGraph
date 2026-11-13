# MFGraph developer tasks. Requires Go (see go.mod); Docker only for the
# MySQL-backed targets (dev-db-*, test-mysql).

GO      ?= go
DOCKER  ?= docker
COMPOSE ?= $(DOCKER) compose -f deploy/docker-compose.dev.yml
BIN     ?= bin
# DSN of a MySQL user that may create/drop databases (the dev container's root).
MYSQL_TEST_DSN ?= root:mfgraph@tcp(127.0.0.1:3306)/
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: all build test test-heavy test-mysql generate generate-check openapi-check run vet fmt fmt-check clean \
	dev-db-up dev-db-down dev-db-reset \
	vectors-verify vectors-verify-heavy vectors-crosscheck vectors-generate vectors-review

all: build test

## build: compile every package and the two binaries into ./bin
build:
	$(GO) build ./...
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BIN)/mfgraph-server ./cmd/mfgraph-server
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BIN)/mfgraph ./cmd/mfgraph

## test: unit tests (no database, no network)
test:
	$(GO) test ./...

## test-heavy: also run the multi-million-entry tree vectors (~10 s)
test-heavy:
	MFGRAPH_HEAVY_VECTORS=1 $(GO) test -count=1 ./internal/tree/...

## test-mysql: migration tests against MySQL 8.0 (start it with `make dev-db-up`; requires Docker)
test-mysql:
	MFGRAPH_TEST_MYSQL_DSN='$(MYSQL_TEST_DSN)' $(GO) test -count=1 -run MySQL -v ./internal/migrate/... ./cmd/mfgraph-server/...

## generate: regenerate internal/api/gen from contracts/openapi.yaml (needs network for oapi-codegen)
generate:
	$(GO) generate ./internal/api/...

## generate-check: fail if the generated code is not up to date
generate-check: generate
	@git diff --exit-code -- internal/api/gen || (echo "generated code is stale: run make generate and commit" && exit 1)

## openapi-check: validate contracts/openapi.yaml and its conventions (kin-openapi)
openapi-check:
	$(GO) test -count=1 -run 'OpenAPI' ./internal/api/...

## run: start the server locally without a database (business endpoints answer 501)
DEV_TOKENS ?= dev-token-change-me-0000=01JD3X5Q8Z9W2V4T6R8P0N2M4K
run: build
	MFGRAPH_AUTH_TOKENS='$(DEV_TOKENS)' ./$(BIN)/mfgraph-server

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

vectors-verify-heavy:
	python3 scripts/vectors.py verify --heavy

## vectors-crosscheck: run the Go CLI on every tree case and check node files with shasum
vectors-crosscheck:
	python3 scripts/vectors.py crosscheck

## vectors-generate: recompute expected values in testdata/vectors/tree.json (Python reference)
vectors-generate:
	python3 scripts/vectors.py generate

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
