# 路线图

[English](../roadmap.md) | **简体中文**

## M0 — 契约（G0）

- **只冻结 M1.0a 面**：schema 迁移 0000–0004/0006/0008、BlobStore SPI、API/OpenAPI、事件 schema、FormatHandler SPI、身份/授权；
- fixture：upload→commit→restore、manifest tree（纯内容 + 确定性编码）、路径文法正反例；
- 锁/GC/project commit/restore/fencing 的 fixture **移到 M1.0b 门**；
- 门：所有契约评审通过；冻结后不做破坏性变更。CI 在 MySQL 8.0 和 seekdb 上测试迁移。
- 现状：已完成（tag `m0-freeze`）。内容格式（[规范](../spec/zh-CN/content-format.md)）及交叉核对过的测试向量、按 checksum 校验的迁移执行器、OpenAPI 契约、核心 ports、端到端验收测试与 CI。FormatHandler SPI 与 MCP 工具 schema 随后续切片提供。

## M1 — 核心（G1）

- **M1.0a** — **已完成**：单 workspace / 单 branch（`main`）/ POSIX 本地盘 / 整文件对象；`上传 → 提交 → 浏览/下载 → diff → 恢复 → 重启`；REST 与薄 CLI。验收测试（`test/e2e`：9 条验收标准、一个恢复与提交的竞争用例和 CLI 冒烟测试）在 CI 中于 MySQL 8.0 和 seekdb 上通过。
- **M1.0a 模块化** — **已完成**：同一个二进制作为宿主平台的模块进程运行（[模块模式](../module-mode.md)，英文）：Unix domain socket、身份头与项目包含性检查、按顺序把[事件](../events.md)转发到宿主的 outbox 表、身份类型、上传路径 `/objects/`、可复现的 bundle。模块模式的验收测试和独立模式的一起在 MySQL 8.0 和 seekdb 上通过；CI 会用 bundle 跑一遍验收脚本。
- **M1.0b**：chunk/pack（不压缩）、断点续传与分片上传（放在 `/uploads/` 下）、预签名与 S3、锁/fencing（以及模块的 `lock` 能力）、显式快照和 `mfgraph.snapshot_created`、GC、配额、project commit、MCP、独立部署的 outbox 发布器。
- **M1.1**：硬化——角色、按名字创建分支、完整 CLI。
- 门：1 GiB 上传服务端不缓冲；kill -9 后无半成品 snapshot；两并发 commit 只有一个 CAS 成功；锁 fencing；GC 不删已提交快照；POSIX + S3 contract test 全绿。

### M1.0b 开发进展

独立部署已接入 S3、分片续传、显式启用的固定分块与 pack，以及整文件旧快照的升级读取。存储测试覆盖 POSIX、MinIO、RustFS。服务被强制终止后可继续上传，重复完成同一会话可取回原结果。进行中的会话会预留空间。工作区配额上限的数据来源仍待确定。

嵌入模式的分片路由、客户端能否直连对象存储、锁事件和管理权限、checkpoint、项目级恢复及 MCP 对接待宿主确认。锁与对象 GC 的完整里程碑仍待实现。目前的清理覆盖遗弃上传会话、过期幂等记录，以及显式配置保留期的已投递 outbox 行。

### M1.0a：能做什么

- **项目与工作区。** `POST /projects` 在配置的租户下创建项目，创建者是唯一拥有者。`POST /workspaces` 在一个事务里创建项目级工作区、它的 `main` 分支（generation 1、无 head）和用量行；存储前缀为 `tenants/<tenant>/projects/<project>/workspaces/<workspace>`。
- **上传。** `PUT /workspaces/{id}/objects` 流式接收整个文件（边写临时文件边计算 hash，最后只 rename 一次；内存占用不随文件大小增长），校验 `X-Content-SHA256`，对象可靠落盘之后才记录暂存 blob。上限：每个请求 1 GiB（`MFGRAPH_MAX_UPLOAD_BYTES`）。
- **提交。** `POST /workspaces/{id}/commits` 按[规范](../spec/zh-CN/content-format.md)的路径规则把最多 10000 条改动（`MFGRAPH_MAX_COMMIT_CHANGES`）应用到分支 head，只重写改动路径上的目录，并在一个事务里记录快照、父快照、分支 compare-and-swap、blob 转 live 和 outbox 事件。所有 POST 都支持 `Idempotency-Key`。
- **读取。** 快照元数据、时间线（新的在前，可按分支过滤）、目录列表（分页目录不会整个读入）、下载（ETag、`If-None-Match`、单段 Range）、按路径顺序的 diff（相同的子树和页直接跳过）。
- **恢复**整个快照（原样复用其树）或其中一个路径，生成以分支 head 为父快照的新快照。head 对应目录里没有的恢复名字，不能与该目录已有的名字有大小写冲突。
- **请求。** JSON 请求体必须是 `application/json`、单个 JSON 值，最多 `MFGRAPH_MAX_JSON_BODY_BYTES`（16 MiB）；下载是带 `nosniff` 的附件，开始前先检查存储对象；请求在租约期内持有其 `Idempotency-Key`（`MFGRAPH_IDEMPOTENCY_LEASE`，60 秒），之后重试的请求会接管；数据库的锁等待超时和死锁返回 503 并带 `Retry-After: 1`。
- **运维。** 必须连数据库（没有数据库时服务返回 503）；监听 TCP 或 Unix domain socket；API 路径前缀可配置；`/readyz` 报告 Unicode 版本 V 和服务版本；8 秒内优雅退出；日志假名使用部署自己的密钥。
- **模块模式**（M1.0a 模块化）：`MFGRAPH_MODE=module`、`--config` 和 `--socket`、宿主的变量名作为别名、用身份头代替 bearer token、项目包含性检查（404，从不返回 403）、没有项目接口、事件转发到宿主的 outbox 表、每个操作者都记录类型、`PUT /objects/{workspace_id}`、`make bundle`。

### M1.0a：范围与偏差

| 项 | M1.0a 的做法 | 以后补成什么 | 阶段 |
|---|---|---|---|
| 数据库 | 必须连；不连时所有业务接口返回 503 | 不变 | — |
| 权限（独立部署） | 项目创建者是唯一拥有者，其他人一律 404 | 角色：reader / editor / reviewer / owner | M1.1 |
| 权限（嵌入模式） | M1.0a 模块化已完成：宿主的代理把请求限定在一个项目里，MFGraph 检查资源是否属于这个项目（否则 404，从不返回 403） | 身份头签名（HMAC）、由 MFGraph 判定 403、按团队授权 | 以后 |
| CLI | REST 的薄客户端 | 完整 CLI | M1.1 |
| 上传 | 整文件，单请求，上限 1 GiB；也可以走上传路径 `/objects/` | 分片、断点续传、预签名、S3，放在 `/uploads/` 下 | M1.0b |
| 分支 | 只有 `main` | 按名字创建分支；数据库层面的大小写唯一约束 | M1.0b / M1.1 |
| 事件 | 写入 `outbox_events`；模块模式转发到宿主的 outbox 表（M1.0a 模块化），同一分支内按 generation 的顺序、不同工作区之间按事务完成的先后；独立部署不发布 | 独立部署的发布器；积压很多时用的 `(status, created_at_us, event_id)` 索引和已投递行的保留期清理；"从快照 S 建分支"和"S 上的提交"之间的时间下界；两个模块进程同时转发的测试 | M1.0b |
| 模块的就绪检查 | `/readyz`（数据库、表结构版本、存储、宿主 outbox 表、投递状态） | 宿主升级门需要真实请求时，提供可选的只读 `GET {prefix}/_smoke` | M1.0b |
| `mfgraph.snapshot_created` | 登记但不发：快照只来自提交和恢复 | 显式快照（checkpoint、恢复前快照）时发 | M1.0b |
| 用量和配额 | 不维护（只建行） | 配额 | M1.0b |
| 垃圾回收 | 不做，孤立对象和暂存上传会累积 | GC | M1.0b |
| 长路径查询 | `GET` 加 `path` 查询参数（最长 4096 字节） | 需要时新增 `POST` 形式的接口 | 以后 |
| 存储前缀嵌套 | 默认前缀段数固定，不会嵌套 | 宿主平台自选前缀时检查 | M2 |
| 存储对象巡检 | 每次写入和每次下载前按大小检查已有对象 | 后台全量 hash 巡检 | M4 |
| 大目录 | 新建名字的提交或恢复为做大小写检查会读入整个目录；不限制单个请求缓存的节点数，也不限制并发请求数 | 单请求节点缓存上限和并发限制 | M1.0b |
| 慢速上传 | 只限制大小；没有最低速率、总时长和连接数限制 | 最低速率、总超时和连接数限制 | M1.0b |
| 崩溃一致性 | 可靠写入（fsync 加 rename，先写对象再写数据库） | G1 门中的 kill -9 一致性测试 | M1.0b |
| 过期的幂等记录 | 同一 key 再次使用时替换，否则保留 | 由垃圾回收删除 | M1.0b |

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
