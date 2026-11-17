# Module mode

MFGraph is one binary with two deployment forms, chosen by `MFGRAPH_MODE`:

| | `standalone` (default) | `module` |
|---|---|---|
| Runs as | the whole service | a module process of a host platform, behind the host's proxy |
| Listens on | TCP or a Unix domain socket | a Unix domain socket only (a TCP address stops the start) |
| Authentication | static bearer tokens (`MFGRAPH_AUTH_TOKENS`) | identity headers set by the host's proxy, authenticated by an internal token |
| Authorization | the owner of a project (its creator) | the project the host admitted the request to |
| Projects | `/projects` endpoints, kernel tables | owned by the host: `/projects` answers 404, MFGraph never reads a projects table |
| Kernel migrations | run | never run (`MFGRAPH_KERNEL_MIGRATIONS=true` stops the start) |
| Events | written to the local outbox (`outbox_events`) | the same, then forwarded to the host's outbox table |
| Business path prefix | `/api/v1` | `/internal/v1/mfgraph` |

Everything else, including the API, the data and the [events](events.md),
is the same. The packaging of one host platform (manifest, static settings,
acceptance script) lives in `packaging/host-module/`; this document stays
generic: header names below use the default prefix `X-Trusted-`, which a host
replaces with its own.

## Starting a module process

```bash
mfgraph-server serve --config etc/module.env --socket /run/modules/mfgraph.sock
mfgraph-server --config etc/module.env migrate
```

- `--config FILE` and `--socket PATH` may come before or after the command;
  each may be given once. A relative `--config` path is relative to the
  working directory: a host starts the process in the root of the bundle.
- The configuration file has one `KEY=VALUE` per line. Blank lines and lines
  that start with `#` are comments; nothing else is interpreted (no quotes,
  no escapes, no `${...}` expansion; a line with `${` stops the start, so
  that it is never taken literally). Only the variables below are allowed,
  each once.
- Precedence: command-line options, then the environment, then the
  configuration file, then the defaults. An environment variable set to the
  empty string counts as unset.
- `migrate` applies the module's own migrations (embedded in the binary, with
  their versions and checksums recorded in `schema_migrations`) and exits;
  running it again changes nothing.

## Settings

| Variable | Meaning | Module mode |
|---|---|---|
| `MFGRAPH_MODE` | `standalone` or `module` | `module` |
| `MODULE_ID` | the module's ID | optional; when set it must be `mfgraph` |
| `MODULE_SOCKET` | the socket path; the same as `MFGRAPH_HTTP_ADDR=unix:<path>`; `--socket` wins | a socket is required |
| `MODULE_INTERNAL_TOKEN` | the secret the host's proxy sends in the internal token header | required; any non-empty value without surrounding white space or control characters; shorter than 16 characters gives a warning |
| `MFGRAPH_DSN` = `MFGRAPH_DB_DSN` | the database of the module | required |
| `MFGRAPH_OPS_DSN` | the connection of the event forwarder | optional; without it the forwarder uses `MFGRAPH_DSN` |
| `MFGRAPH_DATA_DIR` = `MFGRAPH_STORAGE_ROOT` | the root directory of the object store | required |
| `MFGRAPH_TENANT_ID` | the fixed tenant of the deployment (a ULID) | required, and not the all-zero default |
| `MFGRAPH_TRUSTED_HEADER_PREFIX` | the prefix of the identity headers | default `X-Trusted-` |
| `MFGRAPH_INTERNAL_TOKEN_HEADER` | the header of the internal token | default `<prefix>Internal-Token` |
| `MFGRAPH_ROUTE_PREFIX` = `MFGRAPH_API_PREFIX` | the path prefix of the business endpoints | default `/internal/v1/mfgraph` |
| `MFGRAPH_OUTBOX_TABLE` | the host's outbox table, `schema.table` (`[A-Za-z0-9_]+.[A-Za-z0-9_]+`) | default `ops.module_outbox` |
| `MFGRAPH_KERNEL_MIGRATIONS` | run the kernel migrations | default `false`; `true` stops the start |

The two names of one setting (`MFGRAPH_DSN` and `MFGRAPH_DB_DSN`,
`MFGRAPH_DATA_DIR` and `MFGRAPH_STORAGE_ROOT`, `MFGRAPH_ROUTE_PREFIX` and
`MFGRAPH_API_PREFIX`, `MODULE_SOCKET` and `MFGRAPH_HTTP_ADDR`) may both be
set only to the same value; otherwise the process does not start. The
other settings (upload and body limits, logging, timeouts) are those of
[deployment](deployment.md); `MFGRAPH_MAX_UPLOAD_BYTES` defaults to 1 GiB.
`MFGRAPH_AUTH_TOKENS` is ignored in module mode, with a warning.

A module process refuses to start, with a message that names the setting
but never a token or a DSN, when a required setting is missing, the socket
is a TCP address, the tenant is the all-zero ULID, `MODULE_ID` is not
`mfgraph`, two names of a setting disagree, `MFGRAPH_KERNEL_MIGRATIONS=true`,
or a value is malformed.

## Identity headers

The host's proxy authenticates the caller and passes the result in headers
named `<prefix><claim>` (for example `X-Trusted-Actor`). Every business
request needs:

| Header | Rule |
|---|---|
| the internal token header | equal to `MODULE_INTERNAL_TOKEN` (compared in constant time) |
| `<prefix>Actor` | an [actor reference](api.md#actor-references): valid UTF-8, at most 255 bytes, no control characters |
| `<prefix>Actor-Type` | `[a-z0-9_-]{1,16}`, for example `human` or `worker` |
| `<prefix>Project` | a ULID: the project the host admitted the request to |

Optional: `<prefix>On-Behalf-Of` (an actor reference) and
`<prefix>On-Behalf-Of-Type` (an actor type; it needs `On-Behalf-Of`, which
alone has no type), recorded with the snapshots the request creates;
`<prefix>Team`, `<prefix>Token-Jti`, `<prefix>Issued-At` and
`<prefix>Audience`, which are only logged. A header given twice, a missing or
empty required header, or a header that breaks its rule gives `401
unauthenticated` (without a `WWW-Authenticate` challenge); `Authorization`
is ignored. A wrong token is logged at info level by the access log only; a
refused identity header after a valid token is also logged as a warning that
names the header, never its value.

`<prefix>Request-Id`, when it is 1-128 visible ASCII characters, becomes the
request ID: logged, returned in `X-Request-Id` and in error bodies.
`<prefix>Trace-Id` is logged as `trace_id`. Actor references are logged only
as pseudonyms at info level, as in standalone mode.

## Authorization

A request sees exactly the resources of its project: a workspace, branch,
snapshot or file of another project gives `404 not_found`, the same response
as for an ID that does not exist. A workspace is created in the project of
the request: `project_id` of `POST /workspaces` must equal `<prefix>Project`
(otherwise 404); MFGraph does not check that the project exists, which is
the host's business. The module never answers `403`: whether a caller may
read but not write a project is decided by the host before the request
arrives. `/projects` and `/projects/{project_id}` answer 404 for every
method, as unknown paths do.

The actor type is stored next to every actor reference MFGraph records
(`author_type`, `on_behalf_of_type` of snapshots, `owner_type` of
workspaces). An `Idempotency-Key` is replayed only to the actor that used it,
with the same type: the same reference with another type gets
`409 idempotency_key_reuse`.

## Uploads

`PUT {prefix}/objects/{workspace_id}` is the upload of
`PUT {prefix}/workspaces/{workspace_id}/objects` under a path that starts
with `/objects/`, so that a proxy can give uploads larger body and time
limits by path prefix (for example 1 GiB and 600 s for `{prefix}/objects/`
and `{prefix}/uploads/`, and its default elsewhere). Uploads in parts will be
added below `{prefix}/uploads/`.

## Events

A change writes its [events](events.md) to the local outbox in its own
transaction. In module mode a forwarder in the server process copies them to
the host's outbox table (`MFGRAPH_OUTBOX_TABLE`) through `MFGRAPH_OPS_DSN`:

```sql
INSERT INTO <table> (event_id, module, event_kind, scope_kind, scope_id, actor_type, actor_id,
  payload_json, status, attempts, available_at, created_at, updated_at)
VALUES (?, 'mfgraph', ?, 'project', ?, ?, ?, ?, 'pending', 0, ?, ?, ?)
```

- `event_id` is the envelope's ID; `event_kind` the kind; `scope_id` the
  project; `actor_type` and `actor_id` the actor, complete; `payload_json`
  the envelope; `created_at` the time of the change and `available_at`,
  `updated_at` the time of the insert, all UTC `DATETIME(6)`.
- Order: events go out one by one in outbox order (time, then row ID), which
  is the order in which they were emitted. The forwarder stops at the first
  event it cannot deliver and starts with it again next time; it never skips
  one.
- At least once: an `event_id` that the table already has (its unique key)
  counts as delivered, so an event that was written but not marked as
  delivered is simply written again.
- Latency: the forwarder looks every 500 ms and at once after a commit.
  After a failure the whole forwarder waits 1 s, then twice as long after
  every further failure, up to 60 s, and records the retry on the event.
- Stop and restart: on SIGTERM the server stops accepting requests, finishes
  the running ones and delivers one more batch, all within
  `MFGRAPH_SHUTDOWN_TIMEOUT` (8 s); events that are left stay pending in the
  local outbox and are delivered after the next start.
- Accounts: the account of `MFGRAPH_OPS_DSN` needs INSERT on the outbox
  table and nothing else; the account of `MFGRAPH_DSN` needs all privileges
  on the module's schema. The two schemas must be on one database instance
  only when `MFGRAPH_OPS_DSN` is empty.

Standalone deployments have no forwarder: their events stay pending.

## Health

`/healthz` and `/readyz` are at the root of the socket, without
authentication. In module mode `/readyz` also checks the host's outbox table
(check `outbox`): the forwarder's connection works and the table exists.
When the account may only insert, the table is looked up in
`information_schema.tables`.

## Bundles

`make bundle` builds, for linux/amd64 and linux/arm64, a reproducible
`tar.zst` with the server, a manifest, the static settings, the migrations
(for audit), the acceptance script and [events.md](events.md), plus
`sha256sums.txt`; `make bundle-check` builds them again from an empty build
cache and compares. See `packaging/host-module/README.md`.
