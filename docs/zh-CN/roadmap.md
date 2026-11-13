# 路线图

[English](../roadmap.md) | **简体中文**

## M0 — 契约（G0）

- **只冻结 M1.0a 面**：schema 迁移 0000–0004/0006/0008、BlobStore SPI、API/OpenAPI、事件 schema、FormatHandler SPI、身份/授权；
- fixture：upload→commit→restore、manifest tree（纯内容 + 确定性编码）、路径文法正反例；
- 锁/GC/project commit/restore/fencing 的 fixture **移到 M1.0b 门**；
- 门：所有契约评审通过；冻结后不做破坏性变更。seekdb 5.7 仅作实验档可选校验。
- 现状：内容格式（[规范](../spec/zh-CN/content-format.md)）及交叉核对过的测试向量、迁移 0000–0004/0006/0008 与按 checksum 校验的执行器、OpenAPI 契约与生成的服务端桩代码、核心 ports 与内存实现、端到端验收测试（M1.0a 之前以 501 失败）以及 CI 已就位。FormatHandler SPI 与 MCP 工具 schema 随后续切片提供。

## M1 — 核心（G1）

- **M1.0a**（1–2 周）：单 workspace / 单 branch / POSIX 本地盘 / whole-file；`upload→commit→diff→restore→重启`；REST + CLI。无锁子系统、无 GC、无 project commit。
- **M1.0b**（其余）：chunk/pack（不压缩）、断点续传、锁/fencing、GC、project commit、MCP；之后 M1.1 硬化。
- 门：1 GiB 上传服务端不缓冲；kill -9 后无半成品 snapshot；两并发 commit 只有一个 CAS 成功；锁 fencing；GC 不删已提交快照；POSIX + S3 contract test 全绿。

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
