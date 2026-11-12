# 路线图

[English](../roadmap.md) | **简体中文**

## M0 — 契约

- schema、BlobStore SPI、API/OpenAPI、事件 schema、FormatHandler SPI、身份/授权。
- 门：所有契约评审通过；冻结后不做破坏性变更。

## M1 — 核心

- workspace/workstream/branch/snapshot；
- chunk/pack/upload/lock；
- diff/restore/timeline/GC/quota；
- MySQL + S3 兼容/POSIX；
- 门：1 GiB 上传、无半成品 snapshot、并发 CAS、锁 fencing。

## M2 — 集成

- 可选 `mfgraph-merge`（文本三方合并，二进制仅锁）；
- 内置 HTTP 工作流适配器；
- 可选 webhook/治理适配器；
- 门：工作流 e2e；无引擎路径仍可用。

## M3 — 格式

- 文本、图片、PDF、音视频、数据集、归档、通用二进制；
- 预览/代理提取；
- CAD/BIM/DICOM/PSD 通过插件。

## M4 — 规模化

- pack compaction、GC、多租户配额、备份/恢复、HA；
- Azure Blob/GCS 适配器；
- PostgreSQL 适配器（后续，不是 v1 门）。

## v1 非目标

Git 协议、完整二进制合并、完整 DMS/ECM、PLM/BOM、工作流引擎、只服务 Agent 或只依赖 Matrix 的运行时。
