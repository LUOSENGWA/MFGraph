# API

[English](../api.md) | **简体中文**

REST/JSON，版本化在 `/api/v1` 下。MCP 与 CLI 都是同一 API 的薄适配器。

第一个核心切片的契约是 [`contracts/openapi.yaml`](../../contracts/openapi.yaml)（OpenAPI 3.0.3）：健康检查、项目、project 级 workspace、分支、整文件上传进暂存（`PUT /api/v1/workspaces/{id}/objects`）、提交、时间线、目录浏览、文件下载、diff 和恢复，已全部实现（M1.0a）。再往下列出的是规划中的 v1 接口；后续切片只新增接口，不改变已有接口。

## 第一个核心切片（M1.0a）

```text
GET    /healthz                                   存活检查（免鉴权）
GET    /readyz                                    就绪检查：db、storage；unicode_version、version
POST   /api/v1/projects                           创建项目（创建者是拥有者）
GET    /api/v1/projects/{project_id}
POST   /api/v1/workspaces                         创建工作区及其 main 分支
GET    /api/v1/workspaces/{workspace_id}          含 main_branch_id
GET    /api/v1/workspaces/{workspace_id}/branches
PUT    /api/v1/workspaces/{workspace_id}/objects  上传一个完整文件（暂存）
POST   /api/v1/workspaces/{workspace_id}/commits  唯一产生快照的接口
GET    /api/v1/workspaces/{workspace_id}/timeline?branch_id=
GET    /api/v1/snapshots/{snapshot_id}
GET    /api/v1/snapshots/{snapshot_id}/tree?path=
GET    /api/v1/snapshots/{snapshot_id}/file?path=
GET    /api/v1/snapshots/{snapshot_id}/diff?base=
POST   /api/v1/snapshots/{snapshot_id}/restore
```

- **必须连数据库。** 没有数据库（未设置 `MFGRAPH_DB_DSN` 或连不上）时服务照常启动，但 `/readyz` 返回 503，所有业务接口返回 `503 db_unavailable`；没有"无数据库模式"。
- **路径前缀。** 业务接口的默认前缀是 `/api/v1`；`MFGRAPH_API_PREFIX` 可以改到别处（例如 `/files/v1`，这时 `/files/v1/projects` 就是契约里的 `POST /api/v1/projects`），`/healthz` 和 `/readyz` 始终在根路径。其他路径一律返回 JSON 格式的 404。
- **谁能做什么（独立部署）。** 任何已认证的调用者都可以创建项目；创建者是这个项目及其下所有工作区的唯一拥有者，其他人访问一律 `404 not_found`。角色以后再做；嵌入宿主平台时由宿主平台的访问控制判定。
- **限制。** 单次上传最多 `MFGRAPH_MAX_UPLOAD_BYTES`（1 GiB；超过返回 `413 file_too_large`，带 `details.max_bytes`，读取请求体之前先按 `Content-Length` 检查）；其他请求的请求体最多 `MFGRAPH_MAX_JSON_BODY_BYTES`（16 MiB；超过返回 `413 request_too_large`，带 `details.max_bytes`，先按 `Content-Length` 检查，超出上限的部分不会被读取）；一次提交最多 `MFGRAPH_MAX_COMMIT_CHANGES` 条改动（10000）；列表每页最多 500 条（默认 50）。读取接口的文件路径放在 `path` 查询参数里（编码前最多 4096 字节；百分号编码后的中文路径可达约 12 KB，超过部分代理的上限）。
- **JSON 请求体。** 所有 POST 都要发送 `Content-Type: application/json`（参数只允许 `charset=utf-8`；否则返回 `415`，错误码 `invalid_request`，`details.reason` 为 `unsupported_content_type`），请求体必须是单个 JSON 值：值后面除空白外还有内容时返回 `400 invalid_request`。
- **命令行。** `mfgraph` 是这些接口的薄客户端：`project create`、`workspace create`、`upload`、`commit`、`log`、`ls`、`cat`、`diff`、`restore`（`mfgraph help`）。它读取 `MFGRAPH_URL`（`http://host:port` 或 `unix:<socket>`）、`MFGRAPH_TOKEN` 和 `MFGRAPH_API_PREFIX`，服务端返回的错误原样输出。

## Workspace / 项目树

```text
POST   /api/v1/workspaces
GET    /api/v1/workspaces/{id}
GET    /api/v1/projects/{project_id}/tree
GET    /api/v1/projects/{project_id}/timeline
```

## Workstream / branch

```text
POST   /api/v1/workspaces/{id}/workstreams
GET    /api/v1/workspaces/{id}/workstreams
GET    /api/v1/workstreams/{id}
POST   /api/v1/workstreams/{id}/branches
POST   /api/v1/workstreams/{id}/archive
GET    /api/v1/workspaces/{id}/branches
GET    /api/v1/workspaces/{id}/branches/{branch_id}
```

`workstream` 只是版本车道——**MFGraph 不提供 `assign`/`dispatch`**（任务指派与任务状态归外部任务/工作流系统）。分支用不可变 `branch_id` 寻址，按 ref 查询用 `?ref=`：

```text
main
workstreams/<workstream_id>/main
workstreams/<workstream_id>/release-x
```

## Snapshot / diff / restore

```text
POST   /api/v1/workspaces/{id}/snapshots
GET    /api/v1/snapshots/{id}
GET    /api/v1/snapshots/{id}/manifest
GET    /api/v1/workspaces/{id}/timeline
GET    /api/v1/snapshots/{id}/diff?base=
POST   /api/v1/snapshots/{id}/restore
POST   /api/v1/workspaces/{id}/promote
```

## 上传

```text
POST   /api/v1/workspaces/{id}/uploads
GET    /api/v1/uploads/{upload_id}
POST   /api/v1/uploads/{upload_id}/parts
POST   /api/v1/uploads/{upload_id}/complete
DELETE /api/v1/uploads/{upload_id}
```

大文件使用预签名 multipart URL。API 返回元数据与 URL，不返回文件字节。

## 锁

```text
POST   /api/v1/workspaces/{id}/locks
POST   /api/v1/locks/{lock_id}/renew
DELETE /api/v1/locks/{lock_id}
POST   /api/v1/locks/{lock_id}/break-glass
```

## Merge（可选模块）

```text
POST   /api/v1/workspaces/{id}/merges
GET    /api/v1/merges/{id}
GET    /api/v1/merges/{id}/conflicts
POST   /api/v1/merges/{id}/resolve
POST   /api/v1/merges/{id}/complete
POST   /api/v1/merges/{id}/abort
```

## 请求头

```text
Authorization: Bearer <token>
Idempotency-Key: <ulid>
If-Match: <generation>
X-Request-Id: <ulid>
X-Workflow-Id: <optional>
X-Task-Id: <optional>
X-Correlation-Id: <optional>
```

## 错误

所有错误都使用同一种扁平结构：`{"code": "...", "message": "...", "request_id": "...", "details": {...}}`。错误码：`invalid_request`（400）、`unauthenticated`（401）、`forbidden`（403）、`not_found`（404）、`conflict` 与 `idempotency_key_reuse`（409）、`upload_expired` 与 `blob_missing`（410）、`precondition_failed`（412）、`quota_exceeded` 与 `file_too_large`（413）、`invalid_path` 与 `unsupported_format`（422）、`rate_limited`（429）、`internal`（500）、`not_implemented`（501）、`storage_unavailable` 与 `db_unavailable`（503）。

`invalid_path` 错误的 `details.kind` 说明违反了哪条规则，取值见[契约](../../contracts/openapi.yaml)中的 `PathErrorKind` 枚举。只有在单一违规时，这个类别才是确定的。

第一个核心切片的其他 `details`：

| 错误码 | `details` |
|---|---|
| `invalid_request`（400） | `reason`：`checksum_mismatch`（上传）、`no_changes`（提交或恢复后树没有变化）、`too_many_changes`（带 `max_changes`）、`not_a_directory`（对文件列目录）、`not_a_file`（下载目录） |
| `conflict`（409） | `current_generation`（`expected_generation` 已过期），或 `reason`：`path_not_found`（删除的路径不是文件）、`in_progress`（相同 `Idempotency-Key` 的请求还在执行） |
| `blob_missing`（410） | `hashes`，以及 `reason`：`not_uploaded`（从未上传到这个工作区）或 `missing_in_storage`（上传过，但对象已丢失） |
| `file_too_large`（413） | `max_bytes` |
| `request_too_large`（413） | `max_bytes`（上传以外的请求的请求体） |
| `db_unavailable` / `storage_unavailable`（503） | 依赖未配置时 `reason` 为 `not_configured`；`/readyz` 另带 `check` |

提交新建的名字要和基准快照里该目录已有的名字做冲突检查，包括同一请求中另一条改动要删除的名字：只改名字的大小写，或者把文件改成目录，需要两次提交。

按 `path` 恢复时，恢复给 head 增加的名字视为新建的名字：head 的对应目录里没有按字节完全相同的名字时的恢复名字，以及它上面 head 中不存在的每个目录。它们不能与 head 中所在目录的其他名字有相同的大小写折叠键（`invalid_path`，`case_conflict`）：`README.md` 被删除、之后又新建了 `readme.md`，那么在删除 `readme.md` 之前，恢复 `README.md` 会失败。恢复出的目录里面的条目不检查。

## 文件路径

- **写入的路径**（提交）按[内容格式](../spec/zh-CN/content-format.md#2-路径)的写入时规则检查，以 NFC 形式保存。提交只对它新建的名字（路径中在基准快照里不存在的段）执行这些规则；替换或删除文件的变更像查找一样定位文件，并保留它存储时的名字；从基准快照带过来的条目保留存储时的字节。
- **用于查找的路径**（目录、文件接口和 restore 的 `path`，以及替换或删除文件的提交变更的 `path`）必须是合法 UTF-8，不含 `\`、控制字符和非字符；服务端把它转为 NFC，检查空段、`.` 段、`..` 段和 255/4096 字节上限，然后先按客户端发送的原样字节查找，再按 NFC 形式查找。不检查未分配码位、禁止清单和大小写。
- **响应**里的路径总是存储时的形式；凡是按写入时规则写入的名字都是 NFC，可能与客户端发送的不同（NFC 会把 `e` + U+0301 变成 `é`，把 CJK 兼容表意字 U+FA10 变成 U+585A）。
- **路径列表**按路径顺序排列：逐段比较，每段按 UTF-8 字节比较（`a`、`a/x`、`a-b`、`a.txt`）。
- **空文件合法**：请求体为空的上传保存一个空文件。

## 身份引用（actor reference）

每个操作都由一个**操作者（actor）**执行：认证后的调用者，另可附带它所代表的操作者。MFGraph 记录操作者时用**身份引用**，而不是自己的 ID。身份引用是满足以下条件的字符串：

- 非空、合法 UTF-8，至多 **255 字节**；
- 不含控制字符（U+0000–U+001F、U+007F–U+009F）。

除此之外没有其他要求：身份引用按原样保存、按字节比较，不做 Unicode 规范化、不做大小写折叠，也不检查是否为 ULID（`a` 与 `a ` 是两个操作者）。宿主平台传入它自己的身份字符串，例如 worker 名、`manager` 这样的用户名，或 `@alice:example.com` 这样的 ID。独立部署时，一个 principal 的身份引用就是其 `principal_uid`（ULID）的字符串形式；`MFGRAPH_AUTH_TOKENS` 的静态 token 映射到身份引用（`token=alice` 与 `token=01J...` 都合法）。

响应里用 `*_ref` 字段携带身份引用（[契约](../../contracts/openapi.yaml)中的 `ActorRef`），另有可选的 `*_principal_uid`，在宿主平台为其操作者提供 ULID 之前保持 `null`。Go 实现在 `internal/actorref`。
