# Changelog

All notable changes will be documented here.

## [Unreleased]

### M1.0a: the first core slice

- Every endpoint of the contract works: projects (owned by their creator), workspaces with their main branch, whole-file uploads, commits, timeline, directory listings, downloads (ETag, If-None-Match, single ranges), diffs and restores (a whole snapshot or one path), with `Idempotency-Key` on every POST.
- Incremental trees: a commit rewrites only the directories on the changed paths; paged directories are read page by page (`internal/tree`: Lookup, ListDir, Diff, Apply, Graft). A property test checks that the result equals a full rebuild.
- MySQL repositories that run on MySQL 8.0 and seekdb (`internal/store/mysql`), with one contract test suite for them and the in-memory store; a POSIX blob store with fsync-and-rename durability (`internal/blob/posix`); the tree store in its own package (`internal/treestore`).
- New migration `kernel/0001_project_owner` (project owners); existing migration files are unchanged.
- The server needs a database (503 `db_unavailable` without one); new settings: Unix domain sockets, API prefix, storage root, tenant, upload and commit limits, log pseudonym key; the default address is 127.0.0.1:8094 and graceful shutdown is bounded by 8 s. `/readyz` reports `unicode_version` and `version` (additive contract change).
- `mfgraph` gains the client commands project, workspace, upload, commit, log, ls, cat, diff, restore (a thin REST client).
- The write-time disallowed list adds the Hangul fillers, U+206A–U+206F and U+E0001; new test vectors only.
- The acceptance tests need a database and run in CI on MySQL 8.0 and seekdb.

### M0: contracts and code skeleton

- Content format version 1 specified in `docs/spec/content-format.md` (English and Chinese): whole-file SHA-256 content identity, path grammar, canonical tree nodes (`dir`, `dir_page`, `dir_index`) with fixed split thresholds, storage keys.
- Reference implementation of the formats (`internal/contenthash`, `internal/pathx`, `internal/tree`, `internal/storagekey`) with test vectors in `testdata/vectors`, checked against an independent Python implementation and `shasum`; `mfgraph debug hash|tree` for inspecting trees.
- MySQL migrations for the first core slice and a checksum-pinned migration runner (`mfgraph-server migrate`).
- OpenAPI 3.0.3 contract of the first core slice (`contracts/openapi.yaml`), generated server code, flat error envelope, request IDs and static bearer tokens; business endpoints answer `501 not_implemented`.
- Core ports with in-memory implementations, end-to-end acceptance tests (expected to fail until the first core slice), CI workflow and a dependency license check.

### Earlier

- Renamed the project from **MFGit** to **MFGraph** (module/binary/env prefixes: `mfgraph` / `MFGRAPH_`).
- Initial design/spec repository.
- Architecture, data model, storage, API, workflow, security, deployment, FAQ.
