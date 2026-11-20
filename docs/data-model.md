# Data model (MySQL v1)

[English](data-model.md) | [简体中文](zh-CN/data-model.md)

MFGraph v1 targets one SQL dialect: **MySQL-compatible** (MySQL 8.0+, OceanBase MySQL, seekdb 5.7 subset); CI applies the migrations on MySQL 8.0 and on seekdb, and ID and hash columns use `utf8mb4_bin` because seekdb has no `ascii` character set. The schema stores state and indexes, not file bytes. Object storage keeps the bytes; the database keeps references, leases and markers — there is no cross-store transaction.

## Core tables

| Table | Purpose |
|---|---|
| `projects` | kernel bootstrap (host `ops` schema): `project_id/tenant_id/name/status` |
| `project_nodes` | host/kernel multi-level project tree (subproject/phase/building/episode/module/package) |
| `workspaces` | versioned content root; `project_id`, optional `scope_node_id`, immutable `storage_prefix` |
| `workstreams` | execution lane; 1:N branches (no assignee/dispatch fields) |
| `branches` | version refs: `branch_id`, `ref_name`, `branch_name`, `head_snapshot_id`, `generation` |
| `snapshots` | immutable snapshot: `root_tree_hash`, `tree_format_version`, `branch_id` = branch it was created on (nullable) |
| `snapshot_parents` | 0/1/2 parents |
| `snapshot_entries` | optional path index for a snapshot (tree is the source of truth) |
| `blobs` | content index, scoped by `workspace_id` |
| `packs` / `chunks` | large file chunk/pack index, scoped by `workspace_id` |
| `workspace_locks` | exclusive/shared leases with fencing token |
| `lock_exclusive_active` | exclusivity helper: one row per active exclusive lock prefix |
| `workspace_fencing` | workspace-level fencing counter (single row; serialization point) |
| `upload_sessions` / `upload_parts` | resumable uploads |
| `project_commits` / `project_commit_entries` | superproject commit: workspace snapshot references only |
| `project_refs` | project-level refs with `generation` CAS |
| `snapshot_pins` | GC pins (task ref / legal hold / export) |
| `project_restore_operations` / `project_restore_entries` | project restore operations (retryable) |
| `derived_artifacts` | thumbnails/proxies/text (not canonical) |
| `workspace_usage` | quota ledger |
| `gc_state` | GC watermark and mutex per workspace |
| `outbox_events` | at-least-once event delivery |
| `idempotency_keys` | API idempotency |
| `schema_migrations` | migration bookkeeping |
| `merge_operations` / `merge_conflicts` | optional merge module |

## Key rules

- all IDs are application-generated ULIDs; timestamps are `BIGINT` epoch microseconds;
- no `ENUM`, generated columns, partial indexes, triggers, or stored procedures;
- JSON is stored as `LONGTEXT`; queries are application-side;
- paths use NFC + `path_hash = sha256(NFC(path))` for indexes;
- **content identity**: `content_hash` = whole-file SHA-256 (`sha256:<hex>`), independent of chunk size, backend and upload path; chunking is physical layout only, not part of the identity;
- **manifest = tree**: tree nodes are pure content with deterministic encoding; `snapshots.root_tree_hash` points to the root node; v1 stores no modification times (only the executable bit, as mode 420/493) — see the [content format specification](spec/content-format.md);
- content tables are **scoped by `workspace_id`** — no cross-workspace deduplication;
- storage keys in the DB are **relative to `workspaces.storage_prefix`** (immutable, chosen at create time);
- **names, paths, refs and keys are `VARBINARY`** and compared byte for byte (`branch_name`, `ref_name`, `default_branch_name`, `scope_path`, `upload_sessions.path`, `storage_prefix`, `storage_key`, `idempotency_key`): `utf8mb4_bin` ignores trailing spaces, but the path rules allow them, so `dev` and `dev ` are two branches. Lengths follow the path rules: names 255 bytes, paths 4096, refs 512, storage prefixes 942;
- `workspaces.storage_prefix_key` is the hex SHA-256 of the ASCII-lowercased prefix, with `UNIQUE (storage_backend_id, storage_prefix_key)`: two prefixes that differ only in case cannot exist on one backend (nested prefixes are rejected by the application, see the [specification](spec/content-format.md#4-storage-keys));
- `snapshots.root_tree_hash_algo` names the algorithm of `root_tree_hash` (`sha256`);
- **actors are actor references**, not ULIDs: owners, creators and authors are stored as `*_ref VARBINARY(255)` (`owner_ref`, `created_by_ref`, `author_ref`, `on_behalf_of_ref`), exactly as the caller's identity string arrives and compared byte for byte (see [actor references](api.md#actor-references)); each has an optional `*_principal_uid CHAR(26)` that stays `NULL` until a host platform provides ULIDs for its actors. In a standalone deployment the reference is the string form of `principals.principal_uid`. IDs that MFGraph generates (workspace, branch, snapshot, upload, ...) and project IDs stay `CHAR(26)`;
- idempotency records are keyed by `(actor_ref, route, idempotency_key)` (1403 bytes);
- **ID columns**: `CHAR(26)` holds ULIDs that MFGraph generates and the project ID a host platform passes (verified to be a ULID); `tenant_id` is MFGraph's own tenant (from the kernel tables in a standalone deployment, from the deployment configuration when embedded) and `scope_node_id` a project node ID that MFGraph generates. IDs a host platform generates in an unverified format are **external references**, `VARBINARY(255)` with the same rules as actor references (`snapshots.workflow_instance_id`; Go: `internal/extref`);
- branches use `ref_name` (`main`, `workstreams/<workstream_id>/<branch_name>`); `workstream` and `branch` are separate entities; branch names that only differ in case (`Dev`, `dev`) must not coexist in one workstream, including the main branch (checked by the application, not by a column);
- lease/lock expiry always compares **DB time** (`NOW(6)`), never application clocks.

## Branch refs

```text
main
workstreams/<workstream_id>/main
workstreams/<workstream_id>/<branch_name>
```

A workstream may have multiple branches. A branch belongs to exactly one workspace and optionally to one workstream.

## Migrations

Migrations live in [`migrations/`](../migrations) and are applied by `mfgraph-server migrate` (or `MFGRAPH_MIGRATE_ON_START=true`). The first core slice defines:

- `kernel/0000_bootstrap.sql` — kernel tables for standalone deployments (`projects`, minimal `principals`); a host platform provides its own and does not run this file
- `kernel/0001_project_owner.sql` — `projects.owner_ref` (`VARBINARY(255) NULL`, the actor reference of the creator, who owns the project in a standalone deployment), `owner_principal_uid` and an index; projects without an owner are visible to nobody
- `mysql/0001_workspaces.sql`
- `mysql/0002_workstreams_branches.sql`
- `mysql/0003_snapshots.sql` — `snapshots`, `snapshot_parents`
- `mysql/0004_blobs.sql` — `blobs` with a `hash_algo` column; status `staged` / `live`
- `mysql/0006_uploads.sql`
- `mysql/0008_usage_outbox_idempotency.sql`
- `mysql/0009_actor_types.sql` — nullable `VARCHAR(16)` actor types next to actor references: `snapshots.author_type` and `on_behalf_of_type`, `idempotency_keys.actor_type`, `workspaces.owner_type`; written when a host platform names the type of its actors (module mode), `NULL` in a standalone deployment
- `mysql/0010_outbox_order.sql` records a commit sequence for event batches and indexes pending delivery.

Numbers 0005 and 0007 are reserved. Later slices add locks, packs/chunks, the optional path index (`snapshot_entries`), derived artifacts, project commits, fencing and GC state as new files. Applied files are immutable: the runner records a SHA-256 per file in `schema_migrations(component, version, checksum, applied_at_us)` and refuses to continue when it changes. A later migration may also extend an existing table with `ALTER TABLE ... ADD COLUMN` (nullable) or `ADD KEY`, one per statement; a statement whose column or index exists is skipped, so an interrupted run can be repeated.

The repositories (`internal/store/mysql`) use only what MySQL 8.0 and seekdb share: no CTEs, window functions, JSON functions, `SKIP LOCKED`/`NOWAIT`, `RETURNING` or generated columns; keyset pagination instead of `OFFSET`; VARBINARY values are passed and read as bytes. A commit moves the branch head with `UPDATE branches SET head_snapshot_id = ?, generation = generation + 1 ... WHERE branch_id = ? AND workspace_id = ? AND generation = ?` in the transaction that inserts the snapshot and its outbox events. An outbox row keeps its own ULID in `event_id`, the event kind in `event_type` and the [event envelope](events.md) (with the derived 64-digit event ID) in `payload_json`; in module mode the forwarder reads the pending rows by `(commit_seq, created_at_us, event_id)` and marks them `published`. An operator may set a row that can never be delivered to `skipped` by hand ([module mode](module-mode.md#a-stalled-forwarder)): the forwarder reads only `pending` rows, and the skipped row stays in the table.

Use expand/contract; never combine destructive DDL and code switch in one release.
