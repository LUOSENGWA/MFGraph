# Roadmap

[English](roadmap.md) | [简体中文](zh-CN/roadmap.md)

## M0 — contracts (G0)

- **M1.0a scope only**: schema migrations 0000–0004/0006/0008, BlobStore SPI, API/OpenAPI, event schema, FormatHandler SPI, identity/authz;
- fixtures: upload→commit→restore, manifest tree (pure content + canonical encoding), path grammar (positive/negative cases);
- locks, GC, project commit/restore and fencing fixtures are **deferred to the M1.0b gate**;
- exit: all contracts reviewed; no destructive change after freeze. CI tests the migrations on MySQL 8.0 and seekdb.
- status: done (tag `m0-freeze`). Content formats ([spec](spec/content-format.md)) with cross-checked test vectors, migrations with a checksum-pinned runner, the OpenAPI contract, core ports, e2e acceptance tests and CI. The FormatHandler SPI and MCP tool schemas come with later slices.

## M1 — core (G1)

- **M1.0a** — **done**: single workspace / single branch (`main`) / POSIX local disk / whole-file objects; `upload → commit → browse/download → diff → restore → restart`; REST and a thin CLI. The nine acceptance tests (`test/e2e`) pass on MySQL 8.0 and on seekdb in CI.
- **M1.0b**: chunk/pack (uncompressed), resumable and multipart upload, presigned URLs and S3, locks/fencing, GC, quotas, project commit, MCP, an outbox publisher.
- **M1.1**: hardening — roles, branches by name, a full CLI.
- exit: 1 GiB upload without server-side buffering; no half snapshot after kill -9; two concurrent commits → one CAS win; lock fencing; GC never deletes committed snapshots; POSIX + S3 contract tests green.

### M1.0a: what it does

- **Projects and workspaces.** `POST /projects` creates a project in the configured tenant; its creator is its only owner. `POST /workspaces` creates a project-level workspace with its `main` branch (generation 1, no head) and its usage row in one transaction; the storage prefix is `tenants/<tenant>/projects/<project>/workspaces/<workspace>`.
- **Uploads.** `PUT /workspaces/{id}/objects` streams a whole file (hashed while it is written to a temporary file, then renamed once; memory use does not grow with the size), checks `X-Content-SHA256`, and records a staged blob only after the object is durable. Limit: 1 GiB per request (`MFGRAPH_MAX_UPLOAD_BYTES`).
- **Commits.** `POST /workspaces/{id}/commits` applies up to 10,000 changes (`MFGRAPH_MAX_COMMIT_CHANGES`) to the branch head with the path rules of the [specification](spec/content-format.md#26-applying-the-rules), rewrites only the directories on the changed paths, and records snapshot, parent, branch compare-and-swap, live blobs and outbox events in one transaction. `Idempotency-Key` is honoured on every POST.
- **Reads.** Snapshot metadata, timeline (newest first, per branch), directory listings (a paged directory is never read as a whole), downloads with ETag, `If-None-Match` and single byte ranges, diffs in path order (equal subtrees and pages are skipped).
- **Restore** of a whole snapshot (its tree reused as is) or of one path, as a new snapshot whose parent is the branch head.
- **Operations.** A database is required (without one the service answers 503); TCP or Unix domain socket; a configurable API prefix; `/readyz` reports the Unicode version V and the build version; graceful shutdown within 8 s; log pseudonyms keyed per deployment.

### M1.0a: scope and deviations

| Item | M1.0a | Later | Phase |
|---|---|---|---|
| Database | required; without one every business endpoint answers 503 | unchanged | — |
| Permissions (standalone) | the creator of a project is its only owner; everyone else gets 404 | roles: reader / editor / reviewer / owner | M1.1 |
| Permissions (embedded) | — | the host platform's access control decides; MFGraph checks that resources belong together | M2 |
| CLI | thin client of the REST API | full CLI | M1.1 |
| Uploads | whole file, one request, at most 1 GiB | multipart, resumable, presigned, S3 | M1.0b |
| Branches | only `main` | branches by name; case-insensitive uniqueness in the database | M1.0b / M1.1 |
| Events | written to `outbox_events`, not published | a publisher; the host platform's outbox when embedded | M1.0b / M2 |
| Usage and quotas | not maintained (the row exists) | quotas | M1.0b |
| Garbage collection | none; orphaned objects and staged uploads accumulate | GC | M1.0b |
| Long paths in queries | `GET` with a `path` query parameter (up to 4096 bytes) | a `POST` form when needed | later |
| Nested storage prefixes | impossible with the default prefixes (fixed number of segments) | checked when a host platform chooses prefixes | M2 |
| Audit of stored objects | an existing object is checked by size on every put | a background audit of full hashes | M4 |

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
