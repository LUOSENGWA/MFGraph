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

#### Fixes after review (M1.0a)

- Request bodies other than uploads are limited by `MFGRAPH_MAX_JSON_BODY_BYTES` (16 MiB; new code `request_too_large`, 413) and never read beyond the limit; POST bodies must be `application/json` (415 otherwise) and a single JSON value (400 for trailing data).
- A restore of one path checks the names it adds to the head for case conflicts (`invalid_path`, `case_conflict`); the specification (section 2.6) says so.
- An in-progress `Idempotency-Key` is held for `MFGRAPH_IDEMPOTENCY_LEASE` (60 s); afterwards a retry takes it over, and the earlier request can no longer commit its change.
- Lock wait timeouts and deadlocks (MySQL 1205/1213; seekdb 1205) give 503 `db_unavailable` with `details.reason` `lock_timeout` / `deadlock` and `Retry-After: 1` instead of 500.
- Downloads check the stored object first (410 `blob_missing`, `missing_in_storage`, for a missing or damaged object) and carry `Content-Disposition: attachment` and `X-Content-Type-Options: nosniff`.
- Every unusable cursor gives 400 with `details.reason` `invalid_cursor`; project names and workspace display names reject control characters, bidi controls and invisible characters (at most 255 bytes); a snapshot is never older than its parent.
- The blob stores no longer overflow for a limit of 2^63-1 (`MFGRAPH_MAX_UPLOAD_BYTES` is now 1 byte to 5 TiB) and make an object found on a put durable before returning; a paged directory whose page was closed by size is rewritten exactly as a full layout would do (not reachable with the frozen limits).
- `MFGRAPH_E2E_REQUIRE_DB=1` (set in CI) makes the acceptance tests fail instead of skipping without a database; new tests for paging at the frozen thresholds, upload/commit and restore/commit races, idempotency takeovers and database lock errors.

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
