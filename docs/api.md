# API

[English](api.md) | [简体中文](zh-CN/api.md)

REST/JSON, versioned under `/api/v1`. MCP and CLI are thin adapters over the same API.

The contract of the first core slice is [`contracts/openapi.yaml`](../contracts/openapi.yaml) (OpenAPI 3.0.3): health checks, projects, project-level workspaces, branches, whole-file upload into staging (`PUT /api/v1/workspaces/{id}/objects`), commits, timeline, tree listing, file download, diff and restore. The lists below show the planned v1 surface; later slices add endpoints without changing existing ones.

## Workspace / project tree

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

`workstream` is a version lane only — **MFGraph does not expose `assign` / `dispatch`** (task assignment and task status belong to the external task/workflow system). Branches are addressed by immutable `branch_id`; fetch by ref with `?ref=`:

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

## Upload

```text
POST   /api/v1/workspaces/{id}/uploads
GET    /api/v1/uploads/{upload_id}
POST   /api/v1/uploads/{upload_id}/parts
POST   /api/v1/uploads/{upload_id}/complete
DELETE /api/v1/uploads/{upload_id}
```

Large files use presigned multipart URLs. The API returns metadata and URLs, not file bytes.

## Lock

```text
POST   /api/v1/workspaces/{id}/locks
POST   /api/v1/locks/{lock_id}/renew
DELETE /api/v1/locks/{lock_id}
POST   /api/v1/locks/{lock_id}/break-glass
```

## Merge (optional module)

```text
POST   /api/v1/workspaces/{id}/merges
GET    /api/v1/merges/{id}
GET    /api/v1/merges/{id}/conflicts
POST   /api/v1/merges/{id}/resolve
POST   /api/v1/merges/{id}/complete
POST   /api/v1/merges/{id}/abort
```

## Headers

```text
Authorization: Bearer <token>
Idempotency-Key: <ulid>
If-Match: <generation>
X-Request-Id: <ulid>
X-Workflow-Id: <optional>
X-Task-Id: <optional>
X-Correlation-Id: <optional>
```

## Errors

Every error has one flat body: `{"code": "...", "message": "...", "request_id": "...", "details": {...}}`. Codes: `invalid_request` (400), `unauthenticated` (401), `forbidden` (403), `not_found` (404), `conflict` and `idempotency_key_reuse` (409), `upload_expired` and `blob_missing` (410), `precondition_failed` (412), `quota_exceeded` and `file_too_large` (413), `invalid_path` and `unsupported_format` (422), `rate_limited` (429), `internal` (500), `not_implemented` (501), `storage_unavailable` and `db_unavailable` (503).
