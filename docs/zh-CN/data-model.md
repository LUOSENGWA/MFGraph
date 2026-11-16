# 数据模型（MySQL v1）

[English](../data-model.md) | **简体中文**

MFGraph v1 只面向一种 SQL 方言：**MySQL 兼容**（MySQL 8.0+、OceanBase MySQL、seekdb 5.7 子集）；CI 在 MySQL 8.0 和 seekdb 上执行迁移，由于 seekdb 没有 `ascii` 字符集，ID 和 hash 列使用 `utf8mb4_bin`。schema 只存状态与索引，不存文件字节。对象存储存字节，数据库存引用、租约与标记——两者之间没有跨存储事务。

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
- **manifest = 树**：树节点是纯内容 + 确定性编码；`snapshots.root_tree_hash` 指向根节点；v1 不保存修改时间（只保留可执行位，即 mode 420/493）——见[内容格式规范](../spec/zh-CN/content-format.md)；
- 内容表**按 `workspace_id` 隔离** —— 不做跨 workspace 去重；
- DB 里的存储 key 是**相对 `workspaces.storage_prefix` 的路径**（创建时写入、不可变）；
- **名字、路径、ref 和 key 类的列一律为 `VARBINARY`**，按字节比较（`branch_name`、`ref_name`、`default_branch_name`、`scope_path`、`upload_sessions.path`、`storage_prefix`、`storage_key`、`idempotency_key`）：`utf8mb4_bin` 比较时忽略结尾空格，而路径规则允许结尾空格，所以 `dev` 与 `dev ` 是两个分支。长度与路径规则一致：名字 255 字节、路径 4096、ref 512、存储前缀 942；
- `workspaces.storage_prefix_key` 是 ASCII 小写后的前缀的 SHA-256 hex，并有 `UNIQUE (storage_backend_id, storage_prefix_key)`：同一个后端上不能有只在大小写上不同的两个前缀（嵌套的前缀由应用拒绝，见[规范](../spec/zh-CN/content-format.md#4-存储-key)）；
- `snapshots.root_tree_hash_algo` 记录 `root_tree_hash` 的算法（`sha256`）；
- **操作者用身份引用表示**，不是 ULID：拥有者、创建者、作者都存为 `*_ref VARBINARY(255)`（`owner_ref`、`created_by_ref`、`author_ref`、`on_behalf_of_ref`），按调用者身份字符串原样保存、按字节比较（见[身份引用](api.md#身份引用actor-reference)）；每列另有可空的 `*_principal_uid CHAR(26)`，在宿主平台为其操作者提供 ULID 之前保持 `NULL`。独立部署时，身份引用就是 `principals.principal_uid` 的字符串形式。MFGraph 自己生成的 ID（workspace、branch、snapshot、upload 等）和项目 ID 仍是 `CHAR(26)`；
- 幂等记录的主键是 `(actor_ref, route, idempotency_key)`（1403 字节）；
- **ID 列**：`CHAR(26)` 存放 MFGraph 自己生成的 ULID，以及宿主平台传入、已核实为 ULID 的项目 ID；`tenant_id` 是 MFGraph 自己的租户（独立部署时来自 kernel 表，嵌入时来自部署配置），`scope_node_id` 是 MFGraph 生成的项目节点 ID。宿主平台生成、格式未核实的 ID 是**外部引用**，`VARBINARY(255)`，规则与身份引用相同（`snapshots.workflow_instance_id`；Go：`internal/extref`）；
- branch 使用 `ref_name`（`main`、`workstreams/<workstream_id>/<branch_name>`）；`workstream` 与 `branch` 是不同实体；同一个 workstream 内（包括 main）不能同时存在只在大小写上不同的分支名（如 `Dev` 与 `dev`），由应用检查，不靠列约束；
- 租约/锁的过期判定一律用 **DB 时间**（`NOW(6)`），不用应用时钟。

## Branch 引用

```text
main
workstreams/<workstream_id>/main
workstreams/<workstream_id>/<branch_name>
```

一个 workstream 可以有多个 branch。一个 branch 只属于一个 workspace，可选属于一个 workstream。

## 迁移

迁移脚本在 [`migrations/`](../../migrations)，由 `mfgraph-server migrate`（或 `MFGRAPH_MIGRATE_ON_START=true`）执行。第一个核心切片定义：

- `kernel/0000_bootstrap.sql` —— 独立部署时的内核表（`projects`、最小的 `principals`）；宿主平台自带这些表，不执行此文件
- `kernel/0001_project_owner.sql` —— `projects.owner_ref`（`VARBINARY(255) NULL`，创建者的身份引用；独立部署时创建者就是项目拥有者）、`owner_principal_uid` 和索引；没有拥有者的项目对任何人都不可见
- `mysql/0001_workspaces.sql`
- `mysql/0002_workstreams_branches.sql`
- `mysql/0003_snapshots.sql` —— `snapshots`、`snapshot_parents`
- `mysql/0004_blobs.sql` —— `blobs`，含 `hash_algo` 列；状态 `staged` / `live`
- `mysql/0006_uploads.sql`
- `mysql/0008_usage_outbox_idempotency.sql`

编号 0005 和 0007 预留。后续切片以新文件增加锁、packs/chunks、可选路径索引（`snapshot_entries`）、派生工件、project commit、fencing 和 GC 状态。已执行的文件不可修改：执行器在 `schema_migrations(component, version, checksum, applied_at_us)` 中记录每个文件的 SHA-256，发现变化即拒绝继续。后来的迁移也可以用 `ALTER TABLE ... ADD COLUMN`（可为 NULL）或 `ADD KEY` 扩展已有的表，每条语句只加一列或一个索引；列或索引已经存在的语句会被跳过，因此中断的执行可以重跑。

仓储实现（`internal/store/mysql`）只使用 MySQL 8.0 与 seekdb 都支持的写法：不用 CTE、窗口函数、JSON 函数、`SKIP LOCKED`/`NOWAIT`、`RETURNING` 和生成列；分页用键集条件而不是 `OFFSET`；VARBINARY 列按字节传入和读取。提交在插入快照和 outbox 事件的同一个事务里用 `UPDATE branches SET head_snapshot_id = ?, generation = generation + 1 ... WHERE branch_id = ? AND workspace_id = ? AND generation = ?` 移动分支 head。

使用 expand/contract；不要把破坏性 DDL 与代码切换放在同一个 release。
