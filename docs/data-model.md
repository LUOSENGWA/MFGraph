# Data model (MySQL v1)

[English](data-model.md) | [简体中文](zh-CN/data-model.md)

MFGit v1 targets one SQL dialect: **MySQL-compatible** (MySQL 8.0+, OceanBase MySQL, seekdb 5.7 subset). The schema stores state and indexes, not file bytes.

## Core tables

| Table | Purpose |
|---|---|
| `project_nodes` | host/kernel multi-level project tree (subproject/phase/building/episode/module/package) |
| `workspaces` | versioned content root; `project_id`, optional `scope_node_id` |
| `workstreams` | execution lane; 1:N branches |
| `branches` | version refs: `branch_id`, `ref_name`, `branch_name`, `head_snapshot_id`, `generation` |
| `snapshots` | immutable snapshot: `branch_id`, `ref_name`, manifest pointer |
| `snapshot_parents` | 0/1/2 parents |
| `snapshot_entries` | path index for a snapshot |
| `blobs` | small file content index |
| `packs` / `chunks` | large file chunk/pack index |
| `workspace_locks` | exclusive/shared leases with fencing token |
| `upload_sessions` / `upload_parts` | resumable uploads |
| `derived_artifacts` | thumbnails/proxies/text (not canonical) |
| `workspace_usage` | quota ledger |
| `outbox_events` | at-least-once event delivery |
| `idempotency_keys` | API idempotency |
| `merge_operations` / `merge_conflicts` | optional merge module |

## Key rules

- all IDs are application-generated ULIDs;
- timestamps are `BIGINT` epoch microseconds;
- no `ENUM`, generated columns, partial indexes, triggers, or stored procedures;
- JSON is stored as `LONGTEXT`; queries are application-side;
- paths use NFC + `path_hash = sha256(NFC(path))` for indexes;
- branches use `ref_name` (`main`, `workstreams/<workstream_id>/<branch_name>`);
- `workstream` and `branch` are separate entities.

## Branch refs

```text
main
workstreams/<workstream_id>/main
workstreams/<workstream_id>/<branch_name>
```

A workstream may have multiple branches. A branch belongs to exactly one workspace and optionally to one workstream.

## Migrations

- `0000_project_nodes.sql`
- `0001_workspaces.sql`
- `0002_workstreams_branches.sql`
- `0003_snapshots_entries.sql`
- `0004_blobs_packs_chunks.sql`
- `0005_locks.sql`
- `0006_uploads.sql`
- `0007_derived_artifacts.sql`
- `0008_usage_outbox_idempotency.sql`

Use expand/contract; never combine destructive DDL and code switch in one release.
