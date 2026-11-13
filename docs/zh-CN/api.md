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
