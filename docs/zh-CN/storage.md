# 存储

[English](../storage.md) | **简体中文**

## BlobStore 接口

```text
Put / PutIfAbsent / Get / GetRange / Stat / Delete / List / Copy
MultipartInit / MultipartPutPart / MultipartComplete / MultipartAbort
PresignPut / PresignGet
Capabilities
```

## 适配器分层

| 适配器 | v1 | 说明 |
|---|---|---|
| S3 兼容（AWS S3、RustFS、MinIO、Ceph RGW、R2、B2、OSS S3 模式、IBM COS、Oracle S3） | ✅ 一等 | multipart、presign、range；能力探测 |
| POSIX（本地/NFS） | ✅ 一等 | 临时文件 + 原子 rename；无 multipart；锁走 DB 租约 |
| Azure Blob | 计划中 | 原生 block blob/SAS；不强制走 S3 网关 |
| GCS | 计划中 | 原生 resumable upload/signed URL |
| WebDAV | 可选 | 无标准 multipart |
| HDFS / CIFS/SMB | 非一等 | 语义/锁差异大 |

## RustFS 与 POSIX 对比

| 维度 | RustFS（S3 兼容） | POSIX（本地/NFS） |
|---|---|---|
| API | S3 REST | 文件路径 |
| 大文件 | multipart、presign、range | 临时文件/合并、pread |
| 原子性 | 对象 PUT；无目录 rename | 同目录 rename |
| 目录 | 扁平 key 前缀 | 真实目录/inode |
| 锁 | 无文件锁；DB 租约 | 本地 flock；NFS 锁不可靠 |
| 规模 | 多节点/HA/云 | 单节点；HA 需共享文件系统 |
| 适用 | 共享/HA/K8s | 单机/隔离网/开发 |

## 内容布局

```text
tenants/<tenant_id>/
  projects/<project_id>/
    nodes/<scope_node_id>/          # 可选
      workspaces/<workspace_id>/
        packs/<pack_id>.pack
        manifests/<snapshot_id>.json.zst
        uploads/<upload_id>/
        derived/<source_hash>/<kind>/
        trash/<yyyy>/<mm>/
```

## 提交协议

1. 写内容对象（不可变；整文件 `sha256`）；
2. 写 pack 文件（可选）；
3. 写树节点（按 hash 寻址；根节点**就是** manifest）；
4. DB 事务：snapshot（`root_tree_hash`）+ parents + branch CAS + outbox —— 不批量写 `snapshot_entries`；
5. 孤儿对象由 GC 回收（见下方 GC）。

DB 与对象存储之间没有分布式事务。一次提交是**文件/版本关联点**；workflow 引用是观察值，不是事务参与者。

## 锁

- 二进制/大文件用 `exclusive`；只读输入用 `shared`；共享锁可并存、独占锁互斥（辅助表 `lock_exclusive_active`）；
- TTL + 心跳 + fencing token；持有凭据是 **lock_id**，不是 principal；
- 租约过期判定用 **DB 时间**（`NOW(6)`）；acquire/接管与提交共用 `workspace_fencing` 行锁（串行化点）；
- 过期写者的提交返回 409；break-glass 仅管理员可用，且必须审计；
- 提交校验 `base_head`：同一路径被并发修改返回 409 `path_conflict`——重试绝不静默覆盖。

## 垃圾回收

- v1 **不删除任何已提交快照**：所有已提交快照都是根，GC 只回收孤儿、暂存与 trash；
- 两段式：**mark（DB）→ sweep（对象存储）**，24h grace；创建后未过 grace 的对象一律不删；
- 默认启用 bucket versioning：sweep 删除**具体 version id**（普通 `DELETE` 只加删除标记）；grace 期同时是撤消窗口；非当前版本 30 天后过期；
- GC 不跨 workspace；`gc_state`（每 workspace 一行）提供水位与互斥；
- v1 pack 不压缩；Range 直接映射 `offset/length`。
