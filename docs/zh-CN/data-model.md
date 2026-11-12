# 数据模型（MySQL v1）

[English](../data-model.md) | **简体中文**

MFGraph v1 只面向一种 SQL 方言：**MySQL 兼容**（MySQL 8.0+、OceanBase MySQL、seekdb 5.7 子集）。schema 只存状态与索引，不存文件字节。

## 核心表

| 表 | 用途 |
|---|---|
| `project_nodes` | 宿主/内核的多级项目树（子项目/阶段/楼栋/剧集/模块/包） |
| `workspaces` | 版本化内容根；`project_id`、可选 `scope_node_id` |
| `workstreams` | 执行车道；1:N branches |
| `branches` | 版本引用：`branch_id`、`ref_name`、`branch_name`、`head_snapshot_id`、`generation` |
| `snapshots` | 不可变快照：`branch_id`、`ref_name`、manifest 指针 |
| `snapshot_parents` | 0/1/2 个父快照 |
| `snapshot_entries` | 快照的路径索引 |
| `blobs` | 小文件内容索引 |
| `packs` / `chunks` | 大文件 chunk/pack 索引 |
| `workspace_locks` | 带 fencing token 的独占/共享租约 |
| `upload_sessions` / `upload_parts` | 可恢复上传 |
| `derived_artifacts` | 缩略图/代理/文本（非权威内容） |
| `workspace_usage` | 配额账本 |
| `outbox_events` | 至少一次事件投递 |
| `idempotency_keys` | API 幂等 |
| `merge_operations` / `merge_conflicts` | 可选 merge 模块 |

## 关键规则

- 所有 ID 都是应用生成的 ULID；
- 时间戳是 `BIGINT` 微秒 epoch；
- 不使用 `ENUM`、生成列、部分索引、触发器或存储过程；
- JSON 存为 `LONGTEXT`；查询在应用侧完成；
- 路径使用 NFC + `path_hash = sha256(NFC(path))` 建索引；
- branch 使用 `ref_name`（`main`、`workstreams/<workstream_id>/<branch_name>`）；
- `workstream` 与 `branch` 是不同实体。

## Branch 引用

```text
main
workstreams/<workstream_id>/main
workstreams/<workstream_id>/<branch_name>
```

一个 workstream 可以有多个 branch。一个 branch 只属于一个 workspace，可选属于一个 workstream。

## 迁移

- `0000_project_nodes.sql`
- `0001_workspaces.sql`
- `0002_workstreams_branches.sql`
- `0003_snapshots_entries.sql`
- `0004_blobs_packs_chunks.sql`
- `0005_locks.sql`
- `0006_uploads.sql`
- `0007_derived_artifacts.sql`
- `0008_usage_outbox_idempotency.sql`

使用 expand/contract；不要把破坏性 DDL 与代码切换放在同一个 release。
