# 存储

[English](../storage.md) | **简体中文**

## BlobStore 接口

```text
Put / PutIfAbsent / Get / GetRange / Stat / Delete / List / Copy
MultipartInit / MultipartPutPart / MultipartComplete / MultipartAbort
PresignPut / PresignGet
Capabilities
```

第一个核心切片实现了 `PutIfAbsent`、`PutStream`、`Get`（支持范围读取）、`Stat` 和 `Delete`（`internal/ports`），有测试用的内存实现和 POSIX 文件系统实现（`internal/blob/posix`）：

- 每个对象是文件 `<root>/<存储前缀>/<key>`；含空段、`.`、`..`、反斜杠或 NUL 字节的 key 一律拒绝；
- 写入先在同一个文件系统上建临时文件（`.tmp-*.part`，用 `O_EXCL` 创建），fsync 之后 rename 到最终位置，再 fsync 所在目录（新建目录时也 fsync 其上级），然后才返回；
- `PutStream` 边写边计算上传内容的 hash，key 由 hash 决定，所以内容只写一次、最后 rename，内存占用不随文件大小增长；超过大小上限或校验和不符时什么都不保存；
- 对象按内容寻址：key 上已有大小相符的对象就保留；大小不符或无法检查的对象当作损坏，原子替换为新内容，并记录一条警告日志（全量 hash 的后台巡检以后再做）。

树节点通过 tree store（`internal/treestore`）存取：它把 zstd 压缩后的 canonical 字节存在同一个 BlobStore 的 `tree/<hh>/<hex>.json.zst` 下。

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
<storage_prefix>/                   # 默认：tenants/<tenant_id>/projects/<project_id>/workspaces/<workspace_id>
  objects/<hh>/<sha256>             # 整文件内容（已冻结）
  tree/<hh>/<sha256>.json.zst       # 树节点：规范字节的 zstd 压缩（已冻结）
  packs/<pack_id>.pack              # 后续切片
  uploads/<upload_id>/              # 后续切片
  derived/<source_hash>/<kind>/
  trash/<yyyy>/<mm>/
```

存储前缀在创建 workspace 时确定，之后不变；数据库只保存相对于它的 key。key 规则见[内容格式规范](../spec/zh-CN/content-format.md#4-存储-key)。

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
- 提交携带 `expected_generation`；基于过期分支 head 的提交返回 409 `conflict`——重试绝不静默覆盖。

## 垃圾回收

- v1 **不删除任何已提交快照**：所有已提交快照都是根，GC 只回收孤儿、暂存与 trash；
- 两段式：**mark（DB）→ sweep（对象存储）**，24h grace；创建后未过 grace 的对象一律不删；
- 默认启用 bucket versioning：sweep 删除**具体 version id**（普通 `DELETE` 只加删除标记）；grace 期同时是撤消窗口；非当前版本 30 天后过期；
- GC 不跨 workspace；`gc_state`（每 workspace 一行）提供水位与互斥；
- v1 pack 不压缩；Range 直接映射 `offset/length`。
