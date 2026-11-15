# Roadmap

[English](roadmap.md) | [简体中文](zh-CN/roadmap.md)

## M0 — contracts (G0)

- **M1.0a scope only**: schema migrations 0000–0004/0006/0008, BlobStore SPI, API/OpenAPI, event schema, FormatHandler SPI, identity/authz;
- fixtures: upload→commit→restore, manifest tree (pure content + canonical encoding), path grammar (positive/negative cases);
- locks, GC, project commit/restore and fencing fixtures are **deferred to the M1.0b gate**;
- exit: all contracts reviewed; no destructive change after freeze. CI tests the migrations on MySQL 8.0 and seekdb.
- status: content formats ([spec](spec/content-format.md)) with cross-checked test vectors, migrations 0000–0004/0006/0008 with a checksum-pinned runner, the OpenAPI contract with generated server stubs, core ports with in-memory implementations, e2e acceptance tests (failing with 501 until M1.0a) and CI are in place. The FormatHandler SPI and MCP tool schemas come with later slices.

## M1 — core (G1)

- **M1.0a** (1–2 weeks): single workspace / single branch / POSIX local disk / whole-file objects; `upload→commit→diff→restore→restart`; REST + CLI. No lock subsystem, no GC, no project commit.
- **M1.0b** (rest): chunk/pack (uncompressed), resumable upload, locks/fencing, GC, project commit, MCP; then M1.1 hardening.
- exit: 1 GiB upload without server-side buffering; no half snapshot after kill -9; two concurrent commits → one CAS win; lock fencing; GC never deletes committed snapshots; POSIX + S3 contract tests green.

## M2 — integrations (G2)

- optional `mfgraph-merge` (text 3-way, default off; binary lock-only);
- event-only workflow adapter (MFGraph does not assign or dispatch tasks);
- optional webhook/governance adapter;
- exit: workflow e2e; no-engine path still works; MFGraph and the workflow engine reference each other by IDs only.

## M3 — formats (G3)

- text, image, PDF, audio/video, dataset, archive, generic binary;
- preview/proxy extraction;
- CAD/BIM/DICOM/PSD via plugins.

## M4 — scale (G4/G5)

- pack compaction, GC pressure tests, multi-tenant quotas, backup/restore, HA;
- Azure Blob/GCS adapters;
- PostgreSQL adapter (later, not a v1 gate).

## v1 defaults (frozen)

- content identity = whole-file SHA-256; tree nodes in object storage (TreeStore SPI, MySQL optional);
- bucket versioning enabled; GC deletes specific version ids only;
- packs are not compressed; tree split at 1024 entries / 1 MiB;
- NFS locks supported (probe; physical coordination only — DB lease remains authoritative).

## Non-goals for v1

Git protocol, full binary merge, full DMS/ECM, PLM/BOM, workflow engine, agent-only/Matrix-only runtime.
