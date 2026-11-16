# MFGraph

**English** | [简体中文](README.zh-CN.md)

**Project-level version graph for teams and agents.**

MFGraph is a self-hosted service, in early development, for versioning project workspaces as a graph. It separates the business hierarchy (`project` → `project_node`), the content layer (`workspace` → `snapshot`), the execution layer (`workstream` → `branch`), and the process layer (external workflow engine). A workspace is a node in the project graph; the project-level commit layer (superproject) is a **v1 core feature**.

> Status: **early development (M1.0a)**. The first core slice works: projects, a workspace with its `main` branch, whole-file uploads, commits, browsing, downloads, diffs and restores over REST and a thin CLI, with MySQL 8.0 or seekdb as the database and a local (POSIX) file system for the objects. See the [roadmap](docs/roadmap.md#m10a-scope-and-deviations) for what it does not do yet (multipart uploads, S3, locks, GC, quotas, roles, more branches).
>
> Note: the byte formats in [`docs/spec/`](docs/spec/content-format.md), the migrations and the OpenAPI contract are authoritative. The other documents describe the planned v1 design; where they differ, the contracts win. MFGraph does not assign or dispatch tasks.

## Why MFGraph

Most tools solve one slice:

- Git-like data versioning for object storage;
- large-file pointers for Git;
- document management with metadata and approvals;
- AEC/ media/ PLM vertical systems;
- workflow engines.

MFGraph focuses on the missing combination:

1. **Project hierarchy** — `project` is the business root; `project_node` can nest (phase, building, episode, module, package); `workstream` is the execution lane.
2. **Workstream ≠ branch** — a workstream can have multiple branches; `branch_id` / `ref_name` are independent version refs.
3. **Binary/large files are first-class** — content-addressed chunks, packfiles, manifests, exclusive locks; no unsafe auto-merge.
4. **Format and industry templates** — text/office/PDF/images/audio-video/CAD/BIM/datasets/archives/DICOM, configured as templates, not hardcoded vertical branches.
5. **Workflow is optional** — a built-in HTTP adapter can connect to an external workflow engine; MFGraph still works with REST/CLI/MCP when no engine is present.
6. **Human/agent neutral** — every caller is one actor, recorded by its actor reference (the host platform's identity string, or a ULID in standalone mode); no special Agent runtime or Matrix dependency.

## What it is not

- not a Git server or Git-compatible protocol;
- not a full document management/ECM system;
- not a PLM/BOM system;
- not a workflow engine;
- not an agent-only or Matrix-only product.

## Architecture at a glance

```text
tenant
└─ project
   └─ project_node*        (optional: subproject/phase/building/episode/module)
      └─ workspace         (versioned content root)
         └─ workstream 1:N branches
            └─ snapshot     (immutable manifest)
workflow (external, optional)
```

## Storage and database

- **Database:** MySQL-compatible dialect for v1 (MySQL 8.0+, OceanBase MySQL, seekdb 5.7 subset); CI tests the migrations on MySQL 8.0 and seekdb. State and indexes only; no file bytes in the database.
- **Object storage:** S3-compatible (AWS S3, RustFS, MinIO, Ceph RGW, R2, B2, OSS S3 mode, IBM COS, Oracle Object Storage S3) and POSIX/NFS. Azure Blob and GCS adapters are planned.
- **Binary/large files:** chunk + packfile + manifest; lock-based concurrency for binary content.

## Documentation

- [Content format specification](docs/spec/content-format.md) · [中文](docs/spec/zh-CN/content-format.md)
- [HTTP API contract (OpenAPI)](contracts/openapi.yaml)
- [Architecture](docs/architecture.md) · [中文](docs/zh-CN/architecture.md)
- [Data model](docs/data-model.md) · [中文](docs/zh-CN/data-model.md)
- [Storage](docs/storage.md) · [中文](docs/zh-CN/storage.md)
- [API](docs/api.md) · [中文](docs/zh-CN/api.md)
- [Workflow integration](docs/workflow.md) · [中文](docs/zh-CN/workflow.md)
- [Security](docs/security.md) · [中文](docs/zh-CN/security.md)
- [Deployment](docs/deployment.md) · [中文](docs/zh-CN/deployment.md)
- [Roadmap](docs/roadmap.md) · [中文](docs/zh-CN/roadmap.md)
- [FAQ](docs/faq.md) · [中文](docs/zh-CN/faq.md)

## Status and license

The code follows the contracts in `docs/spec/`, `contracts/` and `migrations/`; test vectors for the formats are in `testdata/vectors/`.

License: **Apache-2.0** (see [LICENSE](LICENSE)).

## Quick start

Requires Go (version in `go.mod`), `make` and Docker (for MySQL).

```bash
make build                      # ./bin/mfgraph-server and ./bin/mfgraph
make dev-db-up                  # MySQL 8.0 on 127.0.0.1:3306 (throwaway credentials)
export MFGRAPH_DB_DSN='root:mfgraph@tcp(127.0.0.1:3306)/mfgraph'
./bin/mfgraph-server migrate    # create the tables
mkdir -p data/objects
MFGRAPH_STORAGE_ROOT=data/objects MFGRAPH_AUTH_TOKENS='dev-token-change-me-0000=alice' ./bin/mfgraph-server
```

The server listens on `127.0.0.1:8094` (`make run` does the same). In another terminal:

```bash
export MFGRAPH_TOKEN=dev-token-change-me-0000       # MFGRAPH_URL defaults to http://127.0.0.1:8094
P=$(./bin/mfgraph project create demo)
WS=$(./bin/mfgraph workspace create "$P")         # <workspace_id> TAB main_branch=<branch_id>
W=${WS%%$'\t'*}; B=${WS##*=}
echo 'hello' > hello.txt && echo 'draft' > plan.md
S1=$(./bin/mfgraph commit "$W" -m 'first' hello.txt=hello.txt docs/plan.md=plan.md)
echo 'final' > plan.md
S2=$(./bin/mfgraph commit "$W" -m 'second' docs/plan.md=plan.md)
./bin/mfgraph log "$W"                  # two snapshots, newest first
./bin/mfgraph ls "$S2" docs             # one directory of a snapshot
./bin/mfgraph cat "$S1" docs/plan.md    # draft
./bin/mfgraph diff "$S2"                # modified docs/plan.md
S3=$(./bin/mfgraph restore "$S1" --branch "$B")   # a new snapshot with the tree of S1
./bin/mfgraph cat "$S3" docs/plan.md    # draft again
```

The same calls are plain REST: see [API](docs/api.md) and the [contract](contracts/openapi.yaml). Configuration (Unix domain sockets, API prefix, limits) is described in [deployment](docs/deployment.md).

## Development

Prerequisites: Go (version in `go.mod`), `make`, Python 3 (stdlib only, for the doc and vector scripts). Docker is needed **only** for the database-backed targets (MySQL 8.0 and seekdb); everything else runs without it.

```bash
make build        # compile all packages; binaries go to ./bin
make test         # unit tests (no database, no network)
make vet          # go vet
make fmt-check    # gofmt check
make openapi-check  # validate contracts/openapi.yaml (kin-openapi) and its conventions
make generate     # regenerate internal/api/gen from the contract (needs network)
make run          # start mfgraph-server on 127.0.0.1:8094 on the dev MySQL with a development token
```

The server is configured with `MFGRAPH_*` environment variables (table in [deployment](docs/deployment.md#running-mfgraph-server-m10a)): `MFGRAPH_HTTP_ADDR` (`host:port` or `unix:<path>`), `MFGRAPH_API_PREFIX`, `MFGRAPH_DB_DSN` (required for the service to work), `MFGRAPH_MIGRATE_ON_START`, `MFGRAPH_KERNEL_MIGRATIONS`, `MFGRAPH_STORAGE_ROOT`, `MFGRAPH_TENANT_ID`, `MFGRAPH_MAX_UPLOAD_BYTES`, `MFGRAPH_MAX_JSON_BODY_BYTES`, `MFGRAPH_MAX_COMMIT_CHANGES`, `MFGRAPH_AUTH_TOKENS` (`token=ACTOR_REF,...`, for example `token=alice` or `token=<ULID>`; see [actor references](docs/api.md#actor-references)), `MFGRAPH_LOG_LEVEL`, `MFGRAPH_LOG_FORMAT`, `MFGRAPH_LOG_PSEUDONYM_KEY`, `MFGRAPH_SHUTDOWN_TIMEOUT`, `MFGRAPH_IDEMPOTENCY_LEASE`. `mfgraph-server migrate` applies pending migrations and exits.

MySQL 8.0 and seekdb for development run in Docker (`deploy/docker-compose.dev.yml`, bound to `127.0.0.1`, throwaway credentials; see [deployment](docs/deployment.md#database)):

```bash
make dev-db-up      # start MySQL 8.0 and wait until healthy (requires Docker)
make dev-seekdb-up  # start seekdb (MySQL 5.7 subset, port 2881) and wait until healthy
make dev-db-down    # stop them (data kept); make dev-db-reset also deletes the data volumes
make test-mysql     # migration and repository tests against MySQL: empty database, re-run, tampered file, ...
make test-seekdb    # the same tests against seekdb
make test-e2e-mysql # the acceptance tests (test/e2e) against the dev MySQL; test-e2e-seekdb for seekdb
```

Other checks:

```bash
make ci           # everything the CI workflow runs except the database jobs (no Docker needed)
make ci-mysql     # the CI MySQL jobs against the local container: migrations, repositories, acceptance tests
make ci-seekdb    # the same against the local seekdb container
make test-e2e     # acceptance tests; they need MFGRAPH_E2E_DB_DSN and are skipped without it
make vectors-verify vectors-crosscheck   # independent checks of testdata/vectors
```

CI (`.github/workflows/ci.yml`) runs the same Make targets on GitHub Actions, with MySQL 8.0 and seekdb service containers for the database jobs (migrations and repositories, and the acceptance tests).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) · [中文](CONTRIBUTING.zh-CN.md). Security issues: see [SECURITY.md](SECURITY.md) · [中文](SECURITY.zh-CN.md). Code of conduct: [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) · [中文](CODE_OF_CONDUCT.zh-CN.md).
