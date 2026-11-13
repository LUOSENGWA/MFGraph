# MFGraph

**English** | [简体中文](README.zh-CN.md)

**Project-level version graph for teams and agents.**

MFGraph is a design-stage, self-hosted service for versioning project workspaces as a graph. It separates the business hierarchy (`project` → `project_node`), the content layer (`workspace` → `snapshot`), the execution layer (`workstream` → `branch`), and the process layer (external workflow engine). A workspace is a node in the project graph; the project-level commit layer (superproject, MF-D28) is a **v1 core feature**.

> Status: **design/spec stage**. There is no production code in this repository yet. The documents here define the contracts to implement.
>
> ⚠️ **Snapshot notice (2026-11-12)**: this public repo is a *pre-M0 snapshot*, not the current internal contract. Some docs still describe superseded designs (e.g. workstream `assign`/`dispatch` was removed — MFGraph does not assign or dispatch tasks). The source of truth is the internal design set; this repo will be re-synced after M0 freeze.

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
6. **Human/agent neutral** — one `principal_uid` identity; no special Agent runtime or Matrix dependency.

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

- **Database:** MySQL-compatible dialect for v1 (MySQL 8.0+, OceanBase MySQL, seekdb 5.7 subset). State and indexes only; no file bytes in the database.
- **Object storage:** S3-compatible (AWS S3, RustFS, MinIO, Ceph RGW, R2, B2, OSS S3 mode, IBM COS, Oracle Object Storage S3) and POSIX/NFS. Azure Blob and GCS adapters are planned.
- **Binary/large files:** chunk + packfile + manifest; lock-based concurrency for binary content.

## Documentation

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

This repository is a **design/specification repository**. Code will follow the contracts in `docs/`.

License: **Apache-2.0** (see [LICENSE](LICENSE)).

## Development

Prerequisites: Go (version in `go.mod`), `make`, Python 3 (stdlib only, for the doc and vector scripts). Docker is needed **only** for the MySQL-backed targets; everything else runs without it.

```bash
make build        # compile all packages; binaries go to ./bin
make test         # unit tests (no database, no network)
make vet          # go vet
make fmt-check    # gofmt check
make openapi-check  # validate contracts/openapi.yaml (kin-openapi) and its conventions
make generate     # regenerate internal/api/gen from the contract (needs network)
make run          # start mfgraph-server on 127.0.0.1:8080 with a development token
```

The server is configured with `MFGRAPH_*` environment variables: `MFGRAPH_HTTP_ADDR`, `MFGRAPH_DB_DSN` (MySQL DSN, optional in this build), `MFGRAPH_MIGRATE_ON_START`, `MFGRAPH_KERNEL_MIGRATIONS`, `MFGRAPH_AUTH_TOKENS` (`token=PRINCIPAL_ULID,...`), `MFGRAPH_LOG_LEVEL`, `MFGRAPH_LOG_FORMAT`, `MFGRAPH_SHUTDOWN_TIMEOUT`. `mfgraph-server migrate` applies pending migrations and exits. In this build only `/healthz` and `/readyz` are implemented; every other endpoint of [`contracts/openapi.yaml`](contracts/openapi.yaml) answers `501 not_implemented`.

MySQL 8.0 for development runs in Docker (`deploy/docker-compose.dev.yml`, bound to `127.0.0.1`, throwaway credentials):

```bash
make dev-db-up    # start MySQL 8.0 and wait until healthy (requires Docker)
make dev-db-down  # stop it (data kept); make dev-db-reset also deletes the data volume
make test-mysql   # migration tests against it: empty database, re-run, tampered file
```

Other checks:

```bash
make ci           # everything the CI workflow runs except the MySQL job (no Docker needed)
make ci-mysql     # the CI MySQL job against the local container (requires Docker)
make test-e2e     # end-to-end acceptance tests; they fail with 501 until the first core slice exists
make vectors-verify vectors-crosscheck   # independent checks of testdata/vectors
```

CI (`.github/workflows/ci.yml`) runs the same Make targets on GitHub Actions, with a MySQL 8.0 service container for the migration job.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) · [中文](CONTRIBUTING.zh-CN.md). Security issues: see [SECURITY.md](SECURITY.md) · [中文](SECURITY.zh-CN.md). Code of conduct: [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) · [中文](CODE_OF_CONDUCT.zh-CN.md).
