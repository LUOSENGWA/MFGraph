# MFGraph developer tasks. Requires Go (see go.mod); Docker only for the
# MySQL-backed targets (dev-db-*, test-mysql).

GO      ?= go
DOCKER  ?= docker
COMPOSE ?= $(DOCKER) compose -f deploy/docker-compose.dev.yml
BIN     ?= bin
# DSN of a MySQL user that may create/drop databases (the dev container's root).
MYSQL_TEST_DSN ?= root:mfgraph@tcp(127.0.0.1:3306)/
# DSN of the seekdb dev container (root without a password) and how long the
# tests wait for it to answer.
SEEKDB_TEST_DSN ?= root@tcp(127.0.0.1:2881)/
SEEKDB_WAIT ?= 300s
# Unicode version that the Python of vectors-verify* must have; empty: any
# (cases that need another version are skipped). CI sets 15.0.0 (V).
VECTORS_REQUIRE_UCD ?=
VECTORS_VERIFY_FLAGS = $(if $(VECTORS_REQUIRE_UCD),--require-ucd $(VECTORS_REQUIRE_UCD))
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: all build test test-e2e test-e2e-mysql test-e2e-seekdb e2e-compile test-heavy test-mysql test-seekdb generate generate-check openapi-check run vet fmt fmt-check clean \
	dev-db-up dev-db-down dev-db-reset dev-seekdb-up \
	vectors-verify vectors-verify-heavy vectors-crosscheck vectors-generate vectors-review \
	tidy-check deps-check docs-check ci ci-mysql ci-seekdb

all: build test

## ci: run locally everything the CI workflow runs, except the MySQL job (see ci-mysql)
ci: tidy-check fmt-check vet e2e-compile build test test-heavy deps-check \
	openapi-check generate-check vectors-verify-heavy vectors-crosscheck docs-check

## ci-mysql: the CI database jobs against the local Docker MySQL (requires Docker)
ci-mysql: dev-db-up test-mysql test-e2e-mysql

## ci-seekdb: the CI seekdb database jobs against the local Docker seekdb (requires Docker)
ci-seekdb: dev-seekdb-up test-seekdb test-e2e-seekdb

## tidy-check: go.mod/go.sum are tidy and verified
tidy-check:
	$(GO) mod tidy -diff
	$(GO) mod verify

## deps-check: every compiled dependency is allowlisted with a matching license
deps-check:
	python3 scripts/check_deps.py

## docs-check: relative Markdown links resolve; no secrets or private markers
docs-check:
	python3 scripts/check_links.py .
	python3 scripts/scan_sensitive.py .

## build: compile every package and the two binaries into ./bin
build:
	$(GO) build ./...
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BIN)/mfgraph-server ./cmd/mfgraph-server
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BIN)/mfgraph ./cmd/mfgraph

## test: unit tests (no database, no network)
test:
	$(GO) test ./...

## test-e2e: black-box acceptance tests against the real server binary (build tag e2e).
## They need a database: set MFGRAPH_E2E_DB_DSN (see test-e2e-mysql / test-e2e-seekdb);
## without it every test is skipped with the reason.
test-e2e:
	$(GO) test -tags e2e -count=1 -v ./test/e2e/...

## test-e2e-mysql / test-e2e-seekdb: the acceptance tests on the dev MySQL 8.0 / seekdb
test-e2e-mysql:
	MFGRAPH_E2E_DB_DSN='$(MYSQL_TEST_DSN)' $(GO) test -tags e2e -count=1 -v ./test/e2e/...

test-e2e-seekdb:
	MFGRAPH_E2E_DB_DSN='$(SEEKDB_TEST_DSN)' MFGRAPH_E2E_DB_WAIT=$(SEEKDB_WAIT) $(GO) test -tags e2e -count=1 -v ./test/e2e/...

## e2e-compile: make sure the e2e tests compile (used by CI)
e2e-compile:
	$(GO) vet -tags e2e ./test/e2e/...

## test-heavy: also run the multi-million-entry tree vectors (~10 s)
test-heavy:
	MFGRAPH_HEAVY_VECTORS=1 $(GO) test -count=1 ./internal/tree/...

# Packages with database tests (tests named *MySQL*): the migrations and the
# MySQL repositories (contract tests).
DB_TEST_PKGS = ./internal/migrate/... ./internal/store/mysql/... ./cmd/mfgraph-server/...

## test-mysql: migration and repository tests against MySQL 8.0 (start it with `make dev-db-up`; requires Docker)
test-mysql:
	MFGRAPH_TEST_MYSQL_DSN='$(MYSQL_TEST_DSN)' $(GO) test -count=1 -run MySQL -v $(DB_TEST_PKGS)

## test-seekdb: the same tests against seekdb (start it with `make dev-seekdb-up`);
## the tests first wait up to SEEKDB_WAIT for the server to answer queries
test-seekdb:
	MFGRAPH_TEST_MYSQL_DSN='$(SEEKDB_TEST_DSN)' MFGRAPH_TEST_DB_WAIT=$(SEEKDB_WAIT) $(GO) test -count=1 -run MySQL -v $(DB_TEST_PKGS)

## generate: regenerate internal/api/gen from contracts/openapi.yaml (needs network for oapi-codegen)
generate:
	$(GO) generate ./internal/api/...

## generate-check: fail if the generated code is not up to date
generate-check: generate
	@git diff --exit-code -- internal/api/gen || (echo "generated code is stale: run make generate and commit" && exit 1)

## openapi-check: validate contracts/openapi.yaml and its conventions (kin-openapi)
openapi-check:
	$(GO) test -count=1 -run 'OpenAPI' ./internal/api/...

## run: start the server on 127.0.0.1:8094 with the dev MySQL (make dev-db-up), migrations
## applied on start, objects in ./data/objects and a development token
DEV_TOKENS ?= dev-token-change-me-0000=01JD3X5Q8Z9W2V4T6R8P0N2M4K
DEV_DB_DSN ?= root:mfgraph@tcp(127.0.0.1:3306)/mfgraph
DEV_STORAGE_ROOT ?= data/objects
run: build
	mkdir -p $(DEV_STORAGE_ROOT)
	MFGRAPH_AUTH_TOKENS='$(DEV_TOKENS)' MFGRAPH_DB_DSN='$(DEV_DB_DSN)' MFGRAPH_MIGRATE_ON_START=true \
		MFGRAPH_STORAGE_ROOT='$(DEV_STORAGE_ROOT)' ./$(BIN)/mfgraph-server

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
	python3 scripts/vectors.py verify $(VECTORS_VERIFY_FLAGS)

vectors-verify-heavy:
	python3 scripts/vectors.py verify --heavy $(VECTORS_VERIFY_FLAGS)

## vectors-crosscheck: run the Go CLI on every tree case and check node files with shasum
vectors-crosscheck:
	python3 scripts/vectors.py crosscheck

## vectors-generate: recompute expected values in testdata/vectors/tree.json (Python reference)
vectors-generate:
	python3 scripts/vectors.py generate

## vectors-review: regenerate testdata/vectors/REVIEW.md
vectors-review:
	python3 scripts/vectors.py review

## dev-db-up / dev-db-down / dev-db-reset: local MySQL 8.0 via Docker (down and reset
## also stop seekdb; reset deletes the data of both)
dev-db-up:
	$(COMPOSE) up -d --wait mysql

## dev-seekdb-up: local seekdb via Docker (image pinned in deploy/docker-compose.dev.yml)
dev-seekdb-up:
	$(COMPOSE) up -d --wait seekdb

dev-db-down:
	$(COMPOSE) down

dev-db-reset:
	$(COMPOSE) down -v

clean:
	rm -rf $(BIN)
