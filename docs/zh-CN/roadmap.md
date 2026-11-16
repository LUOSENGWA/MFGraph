# 路线图

[English](../roadmap.md) | **简体中文**

## M0 — 契约（G0）

- **只冻结 M1.0a 面**：schema 迁移 0000–0004/0006/0008、BlobStore SPI、API/OpenAPI、事件 schema、FormatHandler SPI、身份/授权；
- fixture：upload→commit→restore、manifest tree（纯内容 + 确定性编码）、路径文法正反例；
- 锁/GC/project commit/restore/fencing 的 fixture **移到 M1.0b 门**；
- 门：所有契约评审通过；冻结后不做破坏性变更。CI 在 MySQL 8.0 和 seekdb 上测试迁移。
- 现状：已完成（tag `m0-freeze`）。内容格式（[规范](../spec/zh-CN/content-format.md)）及交叉核对过的测试向量、按 checksum 校验的迁移执行器、OpenAPI 契约、核心 ports、端到端验收测试与 CI。FormatHandler SPI 与 MCP 工具 schema 随后续切片提供。

## M1 — 核心（G1）

- **M1.0a** — **已完成**：单 workspace / 单 branch（`main`）/ POSIX 本地盘 / 整文件对象；`上传 → 提交 → 浏览/下载 → diff → 恢复 → 重启`；REST 与薄 CLI。9 个验收用例（`test/e2e`）在 CI 中于 MySQL 8.0 和 seekdb 上通过。
- **M1.0b**：chunk/pack（不压缩）、断点续传与分片上传、预签名与 S3、锁/fencing、GC、配额、project commit、MCP、outbox 发布器。
- **M1.1**：硬化——角色、按名字创建分支、完整 CLI。
- 门：1 GiB 上传服务端不缓冲；kill -9 后无半成品 snapshot；两并发 commit 只有一个 CAS 成功；锁 fencing；GC 不删已提交快照；POSIX + S3 contract test 全绿。

### M1.0a：能做什么

- **项目与工作区。** `POST /projects` 在配置的租户下创建项目，创建者是唯一拥有者。`POST /workspaces` 在一个事务里创建项目级工作区、它的 `main` 分支（generation 1、无 head）和用量行；存储前缀为 `tenants/<tenant>/projects/<project>/workspaces/<workspace>`。
- **上传。** `PUT /workspaces/{id}/objects` 流式接收整个文件（边写临时文件边计算 hash，最后只 rename 一次；内存占用不随文件大小增长），校验 `X-Content-SHA256`，对象可靠落盘之后才记录暂存 blob。上限：每个请求 1 GiB（`MFGRAPH_MAX_UPLOAD_BYTES`）。
- **提交。** `POST /workspaces/{id}/commits` 按[规范](../spec/zh-CN/content-format.md)的路径规则把最多 10000 条改动（`MFGRAPH_MAX_COMMIT_CHANGES`）应用到分支 head，只重写改动路径上的目录，并在一个事务里记录快照、父快照、分支 compare-and-swap、blob 转 live 和 outbox 事件。所有 POST 都支持 `Idempotency-Key`。
- **读取。** 快照元数据、时间线（新的在前，可按分支过滤）、目录列表（分页目录不会整个读入）、下载（ETag、`If-None-Match`、单段 Range）、按路径顺序的 diff（相同的子树和页直接跳过）。
- **恢复**整个快照（原样复用其树）或其中一个路径，生成以分支 head 为父快照的新快照。
- **运维。** 必须连数据库（没有数据库时服务返回 503）；监听 TCP 或 Unix domain socket；API 路径前缀可配置；`/readyz` 报告 Unicode 版本 V 和服务版本；8 秒内优雅退出；日志假名使用部署自己的密钥。

### M1.0a：范围与偏差

| 项 | M1.0a 的做法 | 以后补成什么 | 阶段 |
|---|---|---|---|
| 数据库 | 必须连；不连时所有业务接口返回 503 | 不变 | — |
| 权限（独立部署） | 项目创建者是唯一拥有者，其他人一律 404 | 角色：reader / editor / reviewer / owner | M1.1 |
| 权限（嵌入模式） | — | 由宿主平台的访问控制判定；MFGraph 只检查资源归属 | M2 |
| CLI | REST 的薄客户端 | 完整 CLI | M1.1 |
| 上传 | 整文件，单请求，上限 1 GiB | 分片、断点续传、预签名、S3 | M1.0b |
| 分支 | 只有 `main` | 按名字创建分支；数据库层面的大小写唯一约束 | M1.0b / M1.1 |
| 事件 | 写入 `outbox_events`，不发布 | 发布器；嵌入模式写宿主平台的 outbox | M1.0b / M2 |
| 用量和配额 | 不维护（只建行） | 配额 | M1.0b |
| 垃圾回收 | 不做，孤立对象和暂存上传会累积 | GC | M1.0b |
| 长路径查询 | `GET` 加 `path` 查询参数（最长 4096 字节） | 需要时新增 `POST` 形式的接口 | 以后 |
| 存储前缀嵌套 | 默认前缀段数固定，不会嵌套 | 宿主平台自选前缀时检查 | M2 |
| 存储对象巡检 | 每次写入时按大小检查已有对象 | 后台全量 hash 巡检 | M4 |

## M2 — 集成（G2）

- 可选 `mfgraph-merge`（文本三方合并，默认关闭；二进制仅锁）；
- **只投递事件**的工作流适配器（MFGraph 不指派、不派发任务）；
- 可选 webhook/治理适配器；
- 门：工作流 e2e；无引擎路径仍可用；MFGraph 与工作流引擎只通过 ID 互相引用。

## M3 — 格式（G3）

- 文本、图片、PDF、音视频、数据集、归档、通用二进制；
- 预览/代理提取；
- CAD/BIM/DICOM/PSD 通过插件。

## M4 — 规模化（G4/G5）

- pack compaction、GC 压力测试、多租户配额、备份/恢复、HA；
- Azure Blob/GCS 适配器；
- PostgreSQL 适配器（后续，不是 v1 门）。

## v1 默认（冻结）

- 内容身份 = 整文件 SHA-256；树节点存对象存储（TreeStore SPI，MySQL 可选）；
- 默认启用 bucket versioning；GC 只按 version id 删除；
- pack 不压缩；树拆分 1024 条目 / 1 MiB；
- 支持 NFS 锁（探针；只做物理层协调，逻辑锁仍以 DB lease 为准）。

## v1 非目标

Git 协议、完整二进制合并、完整 DMS/ECM、PLM/BOM、工作流引擎、只服务 Agent 或只依赖 Matrix 的运行时。
