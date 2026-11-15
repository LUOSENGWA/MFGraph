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

For `invalid_path`, `details.kind` names the violated rule; the values are listed by the `PathErrorKind` enum of the [contract](../contracts/openapi.yaml). The kind is guaranteed only when a path breaks a single rule.

## File paths

- **Written paths** (uploads, commits) follow the write-time rules of the [content format](spec/content-format.md#2-paths) and are stored in NFC. A commit checks only the paths it adds or changes; entries carried over from the base snapshot keep their stored bytes.
- **Looked-up paths** (`path` of the tree and file endpoints and of a restore) are checked with the read-time rules only, converted to NFC and compared byte for byte with the stored names.
- **Responses** always contain the stored, normalized path, which can differ from what the client sent (NFC turns `e` + U+0301 into `é` and the CJK compatibility ideograph U+FA10 into U+585A).
- **Lists** of paths are in path order: segment by segment, each segment by its UTF-8 bytes (`a`, `a/x`, `a-b`, `a.txt`).
- **Empty files** are valid: an upload with an empty body stores the empty file.

## Actor references

Every operation is performed by an **actor**: the authenticated caller, and optionally the actor it acts on behalf of. MFGraph records actors as **actor references**, not as IDs of its own. An actor reference is a string that

- is non-empty, valid UTF-8 and at most **255 bytes**;
- contains no control characters (U+0000–U+001F, U+007F–U+009F).

Nothing else is required: actor references are stored exactly as given and compared byte for byte, with no Unicode normalization, no case folding and no check that they are ULIDs (`a` and `a ` are two actors). A host platform passes its own identity strings, for example a worker name, a user name such as `manager`, or an ID such as `@alice:example.com`. In a standalone deployment the actor reference of a principal is the string form of its `principal_uid` (a ULID), and the static tokens of `MFGRAPH_AUTH_TOKENS` map to actor references (`token=alice` and `token=01J...` are both valid).

Responses carry the actor reference in `*_ref` fields (`ActorRef` in the [contract](../contracts/openapi.yaml)), with an optional `*_principal_uid` that stays `null` until a host platform provides ULIDs for its actors. The Go implementation is `internal/actorref`.
