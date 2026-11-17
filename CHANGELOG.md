# Changelog

All notable changes will be documented here.

## [Unreleased]

### M1.0a module: the same binary as a module of a host platform

- `MFGRAPH_MODE=module` runs the server as a module process: a Unix domain socket only, `--config FILE` (KEY=VALUE, no expansion) and `--socket PATH` before or after the command, the host's variable names (`MODULE_ID`, `MODULE_SOCKET`, `MODULE_INTERNAL_TOKEN`, `MFGRAPH_DSN`, `MFGRAPH_OPS_DSN`, `MFGRAPH_DATA_DIR`, `MFGRAPH_ROUTE_PREFIX`, ...) as aliases of the existing ones, and refusals at start for missing or contradictory settings. See [module mode](docs/module-mode.md).
- Identity headers instead of bearer tokens (`<prefix>Actor`, `Actor-Type`, `Project`, optional `On-Behalf-Of`, ..., authenticated by an internal token; 401 otherwise); the host's request and trace IDs are used; a request sees only its project's resources (404 like an unknown ID, never 403); no `/projects` endpoints; the core never reads a projects table.
- Events: the same `mfgraph.*` kinds in both modes (`workspace_created`, `branch_created`, `commit_created`, `snapshot_restored`, `branch_updated` with a reason; `snapshot_created` registered, not emitted), a versioned envelope, an `event_id` derived from the content, at most 256 KiB; [docs/events.md](docs/events.md) is the vocabulary. Module mode forwards the local outbox to the host's outbox table strictly in order, at least once, with backoff, and once more on shutdown; `/readyz` checks that table.
- New migration `mysql/0009_actor_types` (nullable actor type columns); the actor type is recorded with every actor, and an `Idempotency-Key` is replayed only to an actor of the same type. `Snapshot` gains the optional `author_type`, `on_behalf_of_ref`, `on_behalf_of_principal_uid` and `on_behalf_of_type`; existing migration files are unchanged.
- New upload path `PUT /api/v1/objects/{workspace_id}`, the same operation as `PUT /api/v1/workspaces/{workspace_id}/objects` (additive contract change).
- A change of a branch is recorded at least two microseconds after the previous one (it used to be one after the parent), so that the outbox keeps the order of a branch's events whatever the clock does.
- `make bundle` builds reproducible module bundles (linux/amd64, linux/arm64; tar.zst and sha256sums.txt) from `packaging/host-module/`, the only directory that names the host platform; CI builds them, checks that they are reproducible and runs their acceptance script.

#### Fixes after review (M1.0a module)

- A DSN the MySQL driver cannot parse stops the start with "`<variable>` is not a valid DSN"; the driver's own message, which could quote a part of the password, is no longer printed.
- The event forwarder's connection always uses the character set utf8mb4: a DSN without `charset` gets `charset=utf8mb4`, and module mode refuses to start when the forwarder's DSN (`MFGRAPH_OPS_DSN`, or `MFGRAPH_DSN` without it) sets another character set, a list such as `utf8,utf8mb4`, a collation of another character set or a `character_set_*`/`collation_*` session variable. With `charset=utf8` an actor outside the BMP could never be delivered on MySQL, and `latin1` mangled the text.
- Every database call of the event forwarder (reading the local outbox, writing the host table, marking a row) has a time limit of 15 s; a call without an answer is a failure like any other (logged, counted as a retry, followed by the backoff) instead of a forwarder that waits silently.
- A stalled forwarder is visible. When the event at the head of the queue failed 5 times in a row or for 5 minutes, the forwarder logs an ERROR line with the backlog (number of pending events, time of the oldest), the event's local ID and `event_id` and the error, again at most every 5 minutes, and an INFO line once delivery moves on. `/readyz` stays 200 but reports `checks.outbox` `stalled` and the new optional field `outbox` (`pending`, `oldest_pending_at`, `stalled_since`, `last_error`; additive contract change). An event that can never be delivered can be set to `skipped` by hand; the forwarder reads only pending events.

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
