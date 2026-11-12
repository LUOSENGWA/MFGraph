# Data model (MySQL v1)

[English](data-model.md) | [简体中文](zh-CN/data-model.md)

MFGraph v1 targets one SQL dialect: **MySQL-compatible** (MySQL 8.0+, OceanBase MySQL, seekdb 5.7 subset). The schema stores state and indexes, not file bytes. Object storage keeps the bytes; the database keeps references, leases and markers — there is no cross-store transaction.

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
- **manifest = tree**: tree nodes are pure content with deterministic encoding; `snapshots.root_tree_hash` points to the root node; `mtime` lives in a snapshot sidecar and is not hashed;
- content tables are **scoped by `workspace_id`** — no cross-workspace deduplication;
- storage keys in the DB are **relative to `workspaces.storage_prefix`** (immutable, chosen at create time);
- branches use `ref_name` (`main`, `workstreams/<workstream_id>/<branch_name>`); `workstream` and `branch` are separate entities;
- lease/lock expiry always compares **DB time** (`NOW(6)`), never application clocks.

## Branch refs

```text
main
workstreams/<workstream_id>/main
workstreams/<workstream_id>/<branch_name>
```

A workstream may have multiple branches. A branch belongs to exactly one workspace and optionally to one workstream.

## Migrations

- `0000_project_nodes.sql` — kernel bootstrap (creates `projects`/`project_nodes` in the host `ops` schema; not an mfgraph module migration)
- `0001_workspaces.sql`
- `0002_workstreams_branches.sql`
- `0003_snapshots_entries.sql`
- `0004_blobs_packs_chunks.sql`
- `0005_locks.sql`
- `0006_uploads.sql`
- `0007_derived_artifacts.sql`
- `0008_usage_outbox_idempotency.sql`
- `0009_project_commits_restore_pins.sql`
- `0010_workspace_fencing.sql`
- `0011_gc_state.sql`

Use expand/contract; never combine destructive DDL and code switch in one release.
