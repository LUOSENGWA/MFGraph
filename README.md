# MFGraph

**English** | [简体中文](README.zh-CN.md)

**Project-level version graph for teams and agents.**

MFGraph is a self-hosted service, in early development, for versioning project workspaces as a graph. It separates the business hierarchy (`project` → `project_node`), the content layer (`workspace` → `snapshot`), the execution layer (`workstream` → `branch`), and the process layer (external workflow engine). A workspace is a node in the project graph; the project-level commit layer (superproject) is a **v1 core feature**.

> Status: **early development (M0)**. The content formats ([specification](docs/spec/content-format.md)), the database schema of the first core slice ([`migrations/`](migrations)), the HTTP contract ([`contracts/openapi.yaml`](contracts/openapi.yaml)) and a code skeleton are in place. Only `/healthz` and `/readyz` are implemented; every other endpoint answers `501 not_implemented` until the first core slice lands.
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

## Development

Prerequisites: Go (version in `go.mod`), `make`, Python 3 (stdlib only, for the doc and vector scripts). Docker is needed **only** for the database-backed targets (MySQL 8.0 and seekdb); everything else runs without it.

```bash
make build        # compile all packages; binaries go to ./bin
make test         # unit tests (no database, no network)
make vet          # go vet
make fmt-check    # gofmt check
make openapi-check  # validate contracts/openapi.yaml (kin-openapi) and its conventions
make generate     # regenerate internal/api/gen from the contract (needs network)
make run          # start mfgraph-server on 127.0.0.1:8080 with a development token
```

The server is configured with `MFGRAPH_*` environment variables: `MFGRAPH_HTTP_ADDR`, `MFGRAPH_DB_DSN` (MySQL DSN, optional in this build), `MFGRAPH_MIGRATE_ON_START`, `MFGRAPH_KERNEL_MIGRATIONS`, `MFGRAPH_AUTH_TOKENS` (`token=ACTOR_REF,...`, for example `token=alice` or `token=<ULID>`; see [actor references](docs/api.md#actor-references)), `MFGRAPH_LOG_LEVEL`, `MFGRAPH_LOG_FORMAT`, `MFGRAPH_SHUTDOWN_TIMEOUT`. `mfgraph-server migrate` applies pending migrations and exits. In this build only `/healthz` and `/readyz` are implemented; every other endpoint of [`contracts/openapi.yaml`](contracts/openapi.yaml) answers `501 not_implemented`.

MySQL 8.0 and seekdb for development run in Docker (`deploy/docker-compose.dev.yml`, bound to `127.0.0.1`, throwaway credentials; see [deployment](docs/deployment.md#database)):

```bash
make dev-db-up      # start MySQL 8.0 and wait until healthy (requires Docker)
make dev-seekdb-up  # start seekdb (MySQL 5.7 subset, port 2881) and wait until healthy
make dev-db-down    # stop them (data kept); make dev-db-reset also deletes the data volumes
make test-mysql     # migration tests against MySQL: empty database, re-run, tampered file, ...
make test-seekdb    # the same migration tests against seekdb
```

Other checks:

```bash
make ci           # everything the CI workflow runs except the MySQL job (no Docker needed)
make ci-mysql     # the CI MySQL job against the local container (requires Docker)
make ci-seekdb    # the CI seekdb job against the local container (requires Docker)
make test-e2e     # end-to-end acceptance tests; they fail with 501 until the first core slice exists
make vectors-verify vectors-crosscheck   # independent checks of testdata/vectors
```

CI (`.github/workflows/ci.yml`) runs the same Make targets on GitHub Actions, with a MySQL 8.0 service container and a seekdb service container for the two migration jobs.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) · [中文](CONTRIBUTING.zh-CN.md). Security issues: see [SECURITY.md](SECURITY.md) · [中文](SECURITY.zh-CN.md). Code of conduct: [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) · [中文](CODE_OF_CONDUCT.zh-CN.md).
