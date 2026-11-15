# API

[English](../api.md) | **简体中文**

REST/JSON，版本化在 `/api/v1` 下。MCP 与 CLI 都是同一 API 的薄适配器。

第一个核心切片的契约是 [`contracts/openapi.yaml`](../../contracts/openapi.yaml)（OpenAPI 3.0.3）：健康检查、项目、project 级 workspace、分支、整文件上传进暂存（`PUT /api/v1/workspaces/{id}/objects`）、提交、时间线、目录浏览、文件下载、diff 和恢复。下文列出的是规划中的 v1 接口；后续切片只新增接口，不改变已有接口。

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

## 文件路径

- **写入的路径**（上传、提交）按[内容格式](../spec/zh-CN/content-format.md#2-路径)的写入时规则检查，以 NFC 形式保存。提交只检查本次新增或修改的路径；从基准快照带过来的条目保留存储时的字节。
- **用于查找的路径**（目录、文件接口以及 restore 的 `path`）只做读取时检查，转为 NFC 后与存储的名字按字节比较。
- **响应**里的路径总是存储的规范形式，可能与客户端发送的不同（NFC 会把 `e` + U+0301 变成 `é`，把 CJK 兼容表意字 U+FA10 变成 U+585A）。
- **路径列表**按路径顺序排列：逐段比较，每段按 UTF-8 字节比较（`a`、`a/x`、`a-b`、`a.txt`）。
- **空文件合法**：请求体为空的上传保存一个空文件。

## 身份引用（actor reference）

每个操作都由一个**操作者（actor）**执行：认证后的调用者，另可附带它所代表的操作者。MFGraph 记录操作者时用**身份引用**，而不是自己的 ID。身份引用是满足以下条件的字符串：

- 非空、合法 UTF-8，至多 **255 字节**；
- 不含控制字符（U+0000–U+001F、U+007F–U+009F）。

除此之外没有其他要求：身份引用按原样保存、按字节比较，不做 Unicode 规范化、不做大小写折叠，也不检查是否为 ULID（`a` 与 `a ` 是两个操作者）。宿主平台传入它自己的身份字符串，例如 worker 名、`manager` 这样的用户名，或 `@alice:example.com` 这样的 ID。独立部署时，一个 principal 的身份引用就是其 `principal_uid`（ULID）的字符串形式；`MFGRAPH_AUTH_TOKENS` 的静态 token 映射到身份引用（`token=alice` 与 `token=01J...` 都合法）。

响应里用 `*_ref` 字段携带身份引用（[契约](../../contracts/openapi.yaml)中的 `ActorRef`），另有可选的 `*_principal_uid`，在宿主平台为其操作者提供 ULID 之前保持 `null`。Go 实现在 `internal/actorref`。
