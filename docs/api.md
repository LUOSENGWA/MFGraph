# API

[English](api.md) | [简体中文](zh-CN/api.md)

REST/JSON, versioned under `/api/v1`. MCP and CLI are thin adapters over the same API.

The contract of the first core slice is [`contracts/openapi.yaml`](../contracts/openapi.yaml) (OpenAPI 3.0.3): health checks, projects, project-level workspaces, branches, whole-file upload into staging (`PUT /api/v1/workspaces/{id}/objects`), commits, timeline, tree listing, file download, diff and restore. All of it is implemented (M1.0a). The lists further below show the planned v1 surface; later slices add endpoints without changing existing ones.

## The first core slice (M1.0a)

```text
GET    /healthz                                   liveness (no authentication)
GET    /readyz                                    readiness: db, storage; unicode_version, version
POST   /api/v1/projects                           create a project (its creator owns it)
GET    /api/v1/projects/{project_id}
POST   /api/v1/workspaces                         create a workspace and its main branch
GET    /api/v1/workspaces/{workspace_id}          includes main_branch_id
GET    /api/v1/workspaces/{workspace_id}/branches
PUT    /api/v1/workspaces/{workspace_id}/objects  upload one whole file (staged)
POST   /api/v1/workspaces/{workspace_id}/commits  the only way to create a snapshot
GET    /api/v1/workspaces/{workspace_id}/timeline?branch_id=
GET    /api/v1/snapshots/{snapshot_id}
GET    /api/v1/snapshots/{snapshot_id}/tree?path=
GET    /api/v1/snapshots/{snapshot_id}/file?path=
GET    /api/v1/snapshots/{snapshot_id}/diff?base=
POST   /api/v1/snapshots/{snapshot_id}/restore
```

- **A database is required.** Without one (`MFGRAPH_DB_DSN` unset or unreachable) the server still starts, `/readyz` answers 503, and every business endpoint answers `503 db_unavailable`; there is no database-less mode.
- **Prefix.** `/api/v1` is the default prefix of the business endpoints; `MFGRAPH_API_PREFIX` moves them (for example to `/files/v1`, so that `/files/v1/projects` is `POST /api/v1/projects` of the contract), while `/healthz` and `/readyz` stay at the root. Requests to any other path get a JSON 404.
- **Who may do what (standalone).** Any authenticated caller may create a project; the creator is the only owner of the project and of all its workspaces, and everyone else gets `404 not_found` for them. Roles come later; embedded in a host platform, its access control decides.
- **Limits.** One upload is at most `MFGRAPH_MAX_UPLOAD_BYTES` (1 GiB; `413 file_too_large` with `details.max_bytes`, checked against `Content-Length` before the body is read); the body of any other request is at most `MFGRAPH_MAX_JSON_BODY_BYTES` (16 MiB; `413 request_too_large` with `details.max_bytes`, checked against `Content-Length` first and never read beyond the limit); one commit has at most `MFGRAPH_MAX_COMMIT_CHANGES` changes (10,000); lists return at most 500 items per page (default 50). File paths of reads travel in the `path` query parameter (at most 4096 bytes before encoding; a percent-encoded CJK path can reach about 12 KB, more than some proxies accept).
- **Names.** A project name and a workspace display name are free text of at most 255 bytes (UTF-8) without control characters, noncharacters, bidi controls or invisible characters (the disallowed list of the path rules); a project name must not be empty or only white space. Otherwise `400 invalid_request`.
- **JSON bodies.** Every POST sends `Content-Type: application/json` (no parameter but `charset=utf-8`; otherwise `415`, code `invalid_request`, `details.reason` = `unsupported_content_type`), and its body is a single JSON value: anything but white space after it gives `400 invalid_request`.
- **Command line.** `mfgraph` is a thin client of these endpoints: `project create`, `workspace create`, `upload`, `commit`, `log`, `ls`, `cat`, `diff`, `restore` (`mfgraph help`). It reads `MFGRAPH_URL` (`http://host:port` or `unix:<socket>`), `MFGRAPH_TOKEN` and `MFGRAPH_API_PREFIX` and prints the server's error responses as received.

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

Every error has one flat body: `{"code": "...", "message": "...", "request_id": "...", "details": {...}}`. Codes: `invalid_request` (400), `unauthenticated` (401), `forbidden` (403), `not_found` (404), `conflict` and `idempotency_key_reuse` (409), `upload_expired` and `blob_missing` (410), `precondition_failed` (412), `quota_exceeded`, `file_too_large` and `request_too_large` (413), `invalid_path` and `unsupported_format` (422), `rate_limited` (429), `internal` (500), `not_implemented` (501), `storage_unavailable` and `db_unavailable` (503).

For `invalid_path`, `details.kind` names the violated rule; the values are listed by the `PathErrorKind` enum of the [contract](../contracts/openapi.yaml). The kind is guaranteed only when a path breaks a single rule.

Other details of the first core slice:

| Code | `details` |
|---|---|
| `invalid_request` (400) | `reason`: `checksum_mismatch` (upload), `no_changes` (a commit or restore that leaves the tree as it is), `too_many_changes` (with `max_changes`), `not_a_directory` (tree listing of a file), `not_a_file` (download of a directory) |
| `conflict` (409) | `current_generation` (a stale `expected_generation`), or `reason`: `path_not_found` (a delete of a path that is not a file), `in_progress` (a request with the same `Idempotency-Key` is running) |
| `blob_missing` (410) | `hashes`, and `reason`: `not_uploaded` (never uploaded to this workspace) or `missing_in_storage` (uploaded, but the object is gone) |
| `file_too_large` (413) | `max_bytes` |
| `request_too_large` (413) | `max_bytes` (the body of a request other than an upload) |
| `db_unavailable` / `storage_unavailable` (503) | `reason`: `not_configured` when the dependency is not configured; `/readyz` adds `check` |

A commit checks the names it creates against the names of the base snapshot's directory, including names that another change of the same request deletes: changing only the case of a name, or turning a file into a directory, takes two commits.

A restore of one `path` treats the names it adds to the head as created names: the restored name, when the head's directory does not have it byte for byte, and every directory above it that the head does not have. They must not have the case-fold key of another name in their directory of the head (`invalid_path`, `case_conflict`): after `README.md` was deleted and `readme.md` created, restoring `README.md` fails until `readme.md` is deleted. The entries inside a restored directory are not checked.

## File paths

- **Written paths** (commits) follow the write-time rules of the [content format](spec/content-format.md#2-paths) and are stored in NFC. A commit applies them only to the names it creates (the segments of a path that do not exist in the base snapshot); a change that replaces or deletes a file finds it like a lookup and keeps its stored name, and entries carried over from the base snapshot keep their stored bytes.
- **Looked-up paths** (`path` of the tree and file endpoints, of a restore, and of a commit change that replaces or deletes a file) must be valid UTF-8 without `\`, control characters or noncharacters; the server converts them to NFC, checks for empty, `.` and `..` segments and the 255/4096-byte limits, and then looks for the path as sent, byte for byte, and after that for its NFC form. Unassigned code points, the disallowed list and case are not checked.
- **Responses** always contain the stored path, which is NFC for every name written under the write-time rules and can differ from what the client sent (NFC turns `e` + U+0301 into `é` and the CJK compatibility ideograph U+FA10 into U+585A).
- **Lists** of paths are in path order: segment by segment, each segment by its UTF-8 bytes (`a`, `a/x`, `a-b`, `a.txt`).
- **Empty files** are valid: an upload with an empty body stores the empty file.

## Actor references

Every operation is performed by an **actor**: the authenticated caller, and optionally the actor it acts on behalf of. MFGraph records actors as **actor references**, not as IDs of its own. An actor reference is a string that

- is non-empty, valid UTF-8 and at most **255 bytes**;
- contains no control characters (U+0000–U+001F, U+007F–U+009F).

Nothing else is required: actor references are stored exactly as given and compared byte for byte, with no Unicode normalization, no case folding and no check that they are ULIDs (`a` and `a ` are two actors). A host platform passes its own identity strings, for example a worker name, a user name such as `manager`, or an ID such as `@alice:example.com`. In a standalone deployment the actor reference of a principal is the string form of its `principal_uid` (a ULID), and the static tokens of `MFGRAPH_AUTH_TOKENS` map to actor references (`token=alice` and `token=01J...` are both valid).

Responses carry the actor reference in `*_ref` fields (`ActorRef` in the [contract](../contracts/openapi.yaml)), with an optional `*_principal_uid` that stays `null` until a host platform provides ULIDs for its actors. The Go implementation is `internal/actorref`.
