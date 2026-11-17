# Deployment

[English](deployment.md) | [简体中文](zh-CN/deployment.md)

## Modes

| Mode | Components | Use |
|---|---|---|
| embedded | host + mfgraph module process (`MFGRAPH_MODE=module`, see [module mode](module-mode.md)) + DB + object store | platform integration |
| standalone | `mfgraph-server` + DB + object store | teams, self-hosted |
| HA | multiple `mfgraph-server` + MySQL HA + S3 | enterprise |
| dev | compose + MySQL + MinIO/POSIX | local/CI |

## Running mfgraph-server (M1.0a)

The server is configured with environment variables, optionally completed by a configuration file:
`mfgraph-server [--config FILE] [--socket PATH] [serve|migrate|version]`, where the options may also follow the
command. The file has `KEY=VALUE` lines with the variables below (`#` comments, no quotes, no `${...}`
expansion); command-line options win over the environment, which wins over the file. The same binary runs in
two modes (`MFGRAPH_MODE`): `standalone`, described here, and `module`, a module process of a host platform,
described in [module mode](module-mode.md) with its variables (`MODULE_ID`, `MODULE_SOCKET`,
`MODULE_INTERNAL_TOKEN`, `MFGRAPH_OPS_DSN`, `MFGRAPH_TRUSTED_HEADER_PREFIX`, `MFGRAPH_INTERNAL_TOKEN_HEADER`,
`MFGRAPH_OUTBOX_TABLE`).

| Variable | Default | Meaning |
|---|---|---|
| `MFGRAPH_MODE` | `standalone` | `standalone` or `module` |
| `MFGRAPH_HTTP_ADDR` | `127.0.0.1:8094` | `host:port`, or `unix:<path>` for a Unix domain socket (below); `--socket PATH` wins |
| `MFGRAPH_API_PREFIX` (alias `MFGRAPH_ROUTE_PREFIX`) | `/api/v1` | path prefix of the business endpoints; `/healthz` and `/readyz` stay at the root |
| `MFGRAPH_DB_DSN` (alias `MFGRAPH_DSN`) | — | MySQL DSN (go-sql-driver), for example `mfgraph:<password>@tcp(127.0.0.1:3306)/mfgraph`; **required** for the service to work |
| `MFGRAPH_MIGRATE_ON_START` | `false` | apply pending migrations before serving |
| `MFGRAPH_KERNEL_MIGRATIONS` | `true` | include the kernel tables (`projects`, `principals`); standalone deployments only |
| `MFGRAPH_STORAGE_ROOT` (alias `MFGRAPH_DATA_DIR`) | — | root directory of the POSIX blob store; required with a database (the server refuses to start without it) |
| `MFGRAPH_TENANT_ID` | `00000000000000000000000000` | tenant of a standalone deployment (a ULID) |
| `MFGRAPH_MAX_UPLOAD_BYTES` | `1073741824` (1 GiB) | largest upload (1 byte to 5 TiB) |
| `MFGRAPH_MAX_JSON_BODY_BYTES` | `16777216` (16 MiB) | largest body of any other request (1 KiB to 1 GiB); larger bodies get `413 request_too_large` and are not read beyond the limit. It also bounds a commit request |
| `MFGRAPH_MAX_COMMIT_CHANGES` | `10000` | most changes per commit |
| `MFGRAPH_AUTH_TOKENS` | — | static bearer tokens: `token=ACTOR_REF[,token=ACTOR_REF...]` (tokens of at least 16 characters) |
| `MFGRAPH_LOG_LEVEL` / `MFGRAPH_LOG_FORMAT` | `info` / `json` | log/slog settings (`text` for humans) |
| `MFGRAPH_LOG_PSEUDONYM_KEY` | random per start | key of the actor pseudonyms in the logs (at least 16 bytes; see [security](security.md)) |
| `MFGRAPH_SHUTDOWN_TIMEOUT` | `8s` | graceful shutdown after SIGTERM or SIGINT; module mode allows at most `8s` and finishes 1 s earlier |
| `MFGRAPH_IDEMPOTENCY_LEASE` | `60s` | how long a request holds its `Idempotency-Key` (1s to 24h); a repeat after that runs again, and the first request, if it still runs, fails with `409 conflict` without making its change. Keep it above the longest request |

- **A database is required.** Without `MFGRAPH_DB_DSN` the server starts, but `/readyz` answers 503 and every business endpoint `503 db_unavailable`; an unreachable database gives the same. Apply the migrations with `mfgraph-server migrate` (or `MFGRAPH_MIGRATE_ON_START=true`); it needs only `MFGRAPH_DB_DSN`.
- **Aliases.** The two names of one setting (`MFGRAPH_DB_DSN` and `MFGRAPH_DSN`, `MFGRAPH_STORAGE_ROOT` and `MFGRAPH_DATA_DIR`, `MFGRAPH_API_PREFIX` and `MFGRAPH_ROUTE_PREFIX`, and `MFGRAPH_HTTP_ADDR=unix:<path>` and `MODULE_SOCKET=<path>`) may both be set only to the same value; otherwise the server does not start.
- **Readiness.** `/readyz` checks the database (required) and the storage root (in module mode also the host's outbox table), and reports `unicode_version` (the Unicode version V of the path rules) and `version` (the build).
- **Unix domain socket.** `MFGRAPH_HTTP_ADDR=unix:/run/mfgraph/api.sock` listens on a socket that only the server's user can use (mode 0600, created with umask 0177). A socket left behind by a stopped server is removed at start; a regular file, a directory, or the socket of a server that still answers stops the start. The socket is removed on shutdown.
- **API prefix.** With `MFGRAPH_API_PREFIX=/files/v1` the contract's `/api/v1/...` endpoints are served at `/files/v1/...` (and `/api/v1/...` answers 404); a reverse proxy can forward a module path unchanged. The prefix starts with `/`, has no trailing `/`, at most 128 bytes, and its segments use `A–Z a–z 0–9 . _ ~ -`.
- **Storage.** The POSIX blob store keeps every object in a file `<MFGRAPH_STORAGE_ROOT>/<storage prefix>/<key>` (see [storage](storage.md)). The root must exist and be writable; temporary files (`.tmp-*.part`) are created next to the objects and renamed into place.
- **Shutdown.** After SIGTERM the server stops accepting connections and waits up to `MFGRAPH_SHUTDOWN_TIMEOUT` (8 s, below the 10 s after which supervisors commonly kill a process) for running requests; in module mode it also forwards one more batch of events within the same time. Module mode refuses a timeout above 8 s (the host kills a module 8 s after SIGTERM) and keeps 1 s of it for the exit, so it is done after 7 s even when a database does not answer; a request still running then is cut off (exit code 1).
- **Events.** Every change writes its [events](events.md) to the outbox in its own transaction. A standalone deployment keeps them there (no publisher yet); module mode forwards them to the host's outbox table.
- **Bundles.** `make bundle` builds the reproducible bundles of the module form (one `tar.zst` per platform and `sha256sums.txt` in `dist/`); `make bundle-check` builds them again from an empty build cache and compares. See [module mode](module-mode.md#bundles).

```bash
make dev-db-up
export MFGRAPH_DB_DSN='root:mfgraph@tcp(127.0.0.1:3306)/mfgraph'
./bin/mfgraph-server migrate
MFGRAPH_STORAGE_ROOT=/var/lib/mfgraph MFGRAPH_AUTH_TOKENS='<token>=alice' ./bin/mfgraph-server
```

## Database

The supported SQL dialect is **MySQL**. CI applies the migrations on two servers in every run: **MySQL 8.0** and **seekdb** (a single-node database compatible with a subset of MySQL 5.7, which host platforms use for single-machine deployments). Both build the same schema. To stay inside the common subset, ID and hash columns use `utf8mb4_bin` rather than the `ascii` character set, which seekdb does not provide; the migration lint enforces this.

On MySQL the tables must use InnoDB's **DYNAMIC** row format, the default since MySQL 5.7.9 (`innodb_default_row_format = DYNAMIC`). COMPACT and REDUNDANT limit an index key to 767 bytes, but the primary key of `idempotency_keys` (actor reference, route and key) has 1403 bytes; DYNAMIC allows 3072. With another default row format the migrations fail.

For development, `deploy/docker-compose.dev.yml` runs both on `127.0.0.1`: MySQL 8.0 on port 3306 and seekdb on port 2881 (user `root` without a password; the image is pinned to an exact tag, is published for `linux/amd64` and `linux/arm64`, and is meant for tests only; it needs at least 2 GB of memory).

```bash
make ci-mysql     # start MySQL 8.0 and run the migration tests on it
make ci-seekdb    # start seekdb and run the same tests on it
```

## Startup checks

The path rules depend on Unicode tables, and the [content format](spec/content-format.md#24-unicode-version-v) fixes their version V. Before `mfgraph-server serve` or `migrate` touches any data, the server compares the tables compiled into the binary with V: the Go standard library's tables (which decide whether a code point is assigned) must be exactly V, and those of `golang.org/x/text` (NFC, case folding) V or later. Otherwise it logs the actual versions and the required V and exits with a non-zero status. There is no switch to skip the check; raising V means changing the code and the specification together. `mfgraph debug` only prints a warning.

## Minimal compose (conceptual)

The S3 settings belong to a later slice; M1.0a stores objects on a POSIX file system (`MFGRAPH_STORAGE_ROOT`).

```yaml
services:
  mysql:
    image: mysql:8.0
    environment:
      MYSQL_DATABASE: mfgraph
      MYSQL_USER: mfgraph
      MYSQL_PASSWORD_FILE: /run/secrets/mysql_password
    volumes: [mysql-data:/var/lib/mysql]

  rustfs:
    image: rustfs/rustfs:latest
    command: server /data --console-address ":9001"
    volumes: [rustfs-data:/data]
    ports: ["9000:9000", "9001:9001"]

  mfgraph:
    image: mfgraph/mfgraph-server:0.1.0
    environment:
      MFGRAPH_DB_DSN: mfgraph:${MYSQL_PASSWORD}@tcp(mysql:3306)/mfgraph
      MFGRAPH_BLOB_BACKEND: s3
      MFGRAPH_S3_ENDPOINT: http://rustfs:9000
      MFGRAPH_S3_BUCKET: mfgraph
      MFGRAPH_PUBLIC_URL: https://mfgraph.example.com
    ports: ["8094:8094"]
    depends_on: [mysql, rustfs]
```

Production adds TLS, secret manager, backups, monitoring.

## K8s

- Deployment `mfgraph-server` (>=2 replicas; single-writer lease);
- external MySQL/OceanBase;
- external S3/RustFS;
- NetworkPolicy: only gateway/DB/object store/workflow adapter;
- non-root, read-only rootfs, drop capabilities;
- PDB, readiness/liveness/startup probes;
- backup CronJob + restore drill.

## Upgrade

```text
backup DB + object manifest
drain traffic
expand migration
start new version
health/smoke
ramp traffic
contract migration in next release
```

N-1 API compatibility; prefer old binary + new schema over restore.

Migrations only add tables, nullable columns and indexes, so the previous binary keeps working on the new schema.

- **Upgrade: migrate first.** Run `mfgraph-server migrate` with the new binary before starting it (or start it with
  `MFGRAPH_MIGRATE_ON_START=true`): the M1.0a module release adds `0009_actor_types` (nullable actor type columns),
  which the new binary reads. A binary whose embedded migrations are not all applied starts, logs an ERROR, and
  `/readyz` answers 503 `db_unavailable` with `details.reason` `schema_behind` and the missing migrations in
  `details.missing`, because its requests could fail.
- **Rollback: no automatic migration.** The previous binary runs on the new schema only with
  `MFGRAPH_MIGRATE_ON_START=false` (the default): its migration runner refuses a schema that records a migration it
  does not know (`applied migration missing from source`), so with `true` it does not start.

## Backup

- DB dump + binlog/PITR;
- object bucket/prefix manifest;
- verify manifest/pack sha256;
- restore drill monthly;
- target RPO <=15 min, RTO <=1 h (design target).

## Ports

| Port | Use |
|---|---|
| 8094 | mfgraph API (default `MFGRAPH_HTTP_ADDR` 127.0.0.1:8094) |
| 3306/2881 | MySQL/OceanBase |
| 9000/9001 | S3/RustFS |
| 443 | TLS ingress |
