# 数据模型（MySQL v1）

[English](../data-model.md) | **简体中文**

MFGraph v1 只面向一种 SQL 方言：**MySQL 兼容**（MySQL 8.0+、OceanBase MySQL、seekdb 5.7 子集）。schema 只存状态与索引，不存文件字节。对象存储存字节，数据库存引用、租约与标记——两者之间没有跨存储事务。

## 核心表

| 表 | 用途 |
|---|---|
| `projects` | 内核 bootstrap（宿主 `ops` schema）：`project_id/tenant_id/name/status` |
| `project_nodes` | 宿主/内核的多级项目树（子项目/阶段/楼栋/剧集/模块/包） |
| `workspaces` | 版本化内容根；`project_id`、可选 `scope_node_id`、不可变 `storage_prefix` |
| `workstreams` | 执行车道；1:N branches（不存 assignee/dispatch 字段） |
| `branches` | 版本引用：`branch_id`、`ref_name`、`branch_name`、`head_snapshot_id`、`generation` |
| `snapshots` | 不可变快照：`root_tree_hash`、`tree_format_version`、`branch_id`（创建时所在分支，可空） |
| `snapshot_parents` | 0/1/2 个父快照 |
| `snapshot_entries` | 快照的可选路径索引（树才是真相源） |
| `blobs` | 内容索引，按 `workspace_id` 隔离 |
| `packs` / `chunks` | 大文件 chunk/pack 索引，按 `workspace_id` 隔离 |
| `workspace_locks` | 带 fencing token 的独占/共享租约 |
| `lock_exclusive_active` | 独占锁互斥辅助表：每个 active 独占前缀一行 |
| `workspace_fencing` | workspace 级 fencing 计数器（单行；串行化点） |
| `upload_sessions` / `upload_parts` | 可恢复上传 |
| `project_commits` / `project_commit_entries` | superproject commit：只存 workspace snapshot 引用 |
| `project_refs` | 项目级引用，带 `generation` CAS |
| `snapshot_pins` | GC pin（任务引用/legal hold/导出） |
| `project_restore_operations` / `project_restore_entries` | 项目级 restore 操作（可重试） |
| `derived_artifacts` | 缩略图/代理/文本（非权威内容） |
| `workspace_usage` | 配额账本 |
| `gc_state` | 每 workspace 的 GC 水位与互斥 |
| `outbox_events` | 至少一次事件投递 |
| `idempotency_keys` | API 幂等 |
| `schema_migrations` | 迁移记录 |
| `merge_operations` / `merge_conflicts` | 可选 merge 模块 |

## 关键规则

- 所有 ID 都是应用生成的 ULID；时间戳是 `BIGINT` 微秒 epoch；
- 不使用 `ENUM`、生成列、部分索引、触发器或存储过程；
- JSON 存为 `LONGTEXT`；查询在应用侧完成；
- 路径使用 NFC + `path_hash = sha256(NFC(path))` 建索引；
- **内容身份**：`content_hash` = 整文件 SHA-256（`sha256:<hex>`），与分片大小、后端、上传路径无关；chunking 只是物理布局，不参与身份；
- **manifest = 树**：树节点是纯内容 + 确定性编码；`snapshots.root_tree_hash` 指向根节点；`mtime` 放快照级 sidecar，不进 hash；
- 内容表**按 `workspace_id` 隔离** —— 不做跨 workspace 去重；
- DB 里的存储 key 是**相对 `workspaces.storage_prefix` 的路径**（创建时写入、不可变）；
- branch 使用 `ref_name`（`main`、`workstreams/<workstream_id>/<branch_name>`）；`workstream` 与 `branch` 是不同实体；
- 租约/锁的过期判定一律用 **DB 时间**（`NOW(6)`），不用应用时钟。

## Branch 引用

```text
main
workstreams/<workstream_id>/main
workstreams/<workstream_id>/<branch_name>
```

一个 workstream 可以有多个 branch。一个 branch 只属于一个 workspace，可选属于一个 workstream。

## 迁移

- `0000_project_nodes.sql` —— 内核 bootstrap（在宿主 `ops` schema 建 `projects`/`project_nodes`；不属于 mfgraph 模块迁移）
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

使用 expand/contract；不要把破坏性 DDL 与代码切换放在同一个 release。
