# Host module packaging (AgentTeams)

This directory packages MFGraph as a module of the AgentTeams platform
(agentteams-service). It is the only place in the repository that names
the host platform or its header prefix `X-Atf-`; everything else is
generic (see [Module mode](../../docs/module-mode.md)), and
`check_host_names.py` keeps it that way in CI.

| File | Purpose |
|---|---|
| `manifest.json.tmpl` | The module manifest; `make bundle` fills in `{{version}}`. The host's release pipeline fills in `sha256` and `signature` and signs. |
| `module.env` | The static settings, `etc/module.env` in the bundle. |
| `acceptance-m1.0a.sh` | The acceptance script of M1.0a, `scripts/acceptance-m1.0a.sh` in the bundle. |
| `acceptance-m1.0a-diff.md` | The difference table, filled in beforehand by MFGraph, `scripts/acceptance-m1.0a-diff.md` in the bundle. |
| `VERSION` | The module version: `0.1.0` is M1.0a; every milestone raises the minor version, `1.0.0` is the first stable version. |
| `check_host_names.py` | CI check: the host's name and header prefix appear nowhere else, in any of the spellings its documentation lists (also concatenated, split across lines, in UTF-16); a line with `host-name-check: allow` is exempt. |
| `test_check_host_names.py` | Tests of the check (CI and `make host-names-check` run them). |

## Bundles

`make bundle` (Go only; zstd comes from the Go module klauspost/compress)
writes to `dist/`:

```text
mfgraph-0.1.0-linux-amd64.tar.zst
mfgraph-0.1.0-linux-arm64.tar.zst
sha256sums.txt
BUILDINFO.txt
```

`BUILDINFO.txt` (also `BUILDINFO` in each bundle; not in the manifest)
records the build:

```text
module=mfgraph
version=0.1.0
commit=<the commit, 40 hex digits; "-dirty" when built with local changes>
go=go1.26.1
source_date_epoch=1794787200
```

Each archive has the bundle root at its top level (no enclosing directory):

```text
bin/mfgraph-server                 CGO_ENABLED=0, -trimpath, version 0.1.0
manifest.json
BUILDINFO                          version, commit and Go version of the build
etc/module.env
migrations/0001_workspaces.sql ... 0009_actor_types.sql   for audit only
scripts/acceptance-m1.0a.sh
scripts/acceptance-m1.0a-diff.md
docs/events.md                     the event vocabulary (drift check)
LICENSE  NOTICE  THIRD_PARTY_NOTICES.md
```

The builds are reproducible: entries are sorted, owned by 0:0 and dated
`SOURCE_DATE_EPOCH` (default 2026-11-16T00:00:00Z), zstd is single-threaded,
and the binary has no paths, VCS data or build ID. The Go version is pinned
too: `go.mod` names the toolchain (`toolchain go1.26.1`), the go command
switches to it by itself, and `make bundle` refuses to build with any other
version. Two builds of the same commit give the same `sha256sums.txt`;
`make bundle-check` builds again with an empty Go build cache and compares
the archives and `BUILDINFO.txt`. MFGraph's CI builds both architectures
with the pinned version, checks `go version`, and uploads them unsigned.

## Releases

A tag `module-v<version>` (with the version of `VERSION`) makes the
workflow `.github/workflows/release-module.yml` build the bundles with the
pinned Go version, check that a second build from an empty build cache is
identical, and publish them unsigned as the assets of the GitHub release of
that tag. A tag that does not match `VERSION` fails the workflow. The
assets have fixed addresses that need no login and do not expire:

```text
https://github.com/<owner>/<repo>/releases/download/module-v<version>/<file>
```

for example
`https://github.com/LUOSENGWA/MFGraph/releases/download/module-v0.1.0/mfgraph-0.1.0-linux-amd64.tar.zst`,
with the files `mfgraph-<version>-linux-amd64.tar.zst`,
`mfgraph-<version>-linux-arm64.tar.zst`, `sha256sums.txt` and
`BUILDINFO.txt`. The commit of a release is the one the tag points to, as
`BUILDINFO.txt` records.

`migrations/` holds the module's own migrations (never the kernel's) for
audit. The host does not execute them: `migrations.command` runs the
migrations embedded in the binary, which are the same files (a test checks
it), with their versions and checksums recorded in `schema_migrations`.

## Settings

The host starts the process in the bundle root, so the relative paths of
the manifest (`bin/mfgraph-server`, `etc/module.env`) resolve. Settings come
from, in this order of precedence: command-line options (`--socket`), the
environment, `--config etc/module.env`, the defaults.

| Set by | Variables |
|---|---|
| the host, per deployment (environment) | `MODULE_ID=mfgraph`, `MODULE_SOCKET`, `MODULE_INTERNAL_TOKEN`, `MFGRAPH_DSN`, `MFGRAPH_OPS_DSN`, `MFGRAPH_DATA_DIR`, `MFGRAPH_TENANT_ID` |
| the bundle (`etc/module.env`) | `MFGRAPH_MODE=module`, `MFGRAPH_TRUSTED_HEADER_PREFIX=X-Atf-`, `MFGRAPH_INTERNAL_TOKEN_HEADER=X-Atf-Internal-Token`, `MFGRAPH_ROUTE_PREFIX=/internal/v1/mfgraph`, `MFGRAPH_OUTBOX_TABLE=ops.module_outbox`, `MFGRAPH_KERNEL_MIGRATIONS=false`, `MFGRAPH_LOG_LEVEL=info` |

MFGraph's own names `MFGRAPH_DB_DSN`, `MFGRAPH_STORAGE_ROOT` and
`MFGRAPH_API_PREFIX` stay valid as aliases; two names of one setting with
different values stop the start. The process refuses to start, with a
message that names the setting (never a token or a DSN password), when: it
is given a TCP address, `MODULE_ID` is not `mfgraph`, `MODULE_INTERNAL_TOKEN`,
the DSN, the data directory or the tenant is missing, the tenant is the
all-zero ULID, `MFGRAPH_KERNEL_MIGRATIONS=true`, `MFGRAPH_SHUTDOWN_TIMEOUT`
is above 8 s, a DSN is malformed, or the forwarder's DSN (`MFGRAPH_OPS_DSN`,
or `MFGRAPH_DSN` without it) sets a character set other than utf8mb4 (a DSN
without `charset` gets `charset=utf8mb4`). An internal token shorter than
16 characters is accepted with a warning. The tenant the platform assigned
is `01M7MT9K23WZC95EECHGDBSYH7`; it stays inside the module (data and
storage paths) and never appears in an event.

The migration command (`migrations.command` of the manifest) needs
`MFGRAPH_DSN` and nothing else: the mode and the other static settings come
from `etc/module.env`; no socket, internal token, tenant, data directory or
`MFGRAPH_OPS_DSN`. It runs only MFGraph's own migrations (never the
kernel's), and running it again skips what is applied.

## What the host provides

- the socket directory, mode 0700 (the socket itself is created with mode
  0600; a stale one is removed);
- `ops.module_outbox` on the same database instance as the `mfgraph` schema
  (the host's migration `ops/0029-module-outbox.sql`, `actor_id
  VARCHAR(255)`), with a **unique index on `event_id` alone** (0029 has
  `UNIQUE KEY uk_module_outbox_event (event_id)`; the name does not
  matter). The forwarder delivers at least once: after a crash between
  writing an event and marking it, it writes the event again, and only the
  unique index turns that into a duplicate-key error, which the forwarder
  recognizes by the duplicate `event_id`. Without the index the event is
  written twice; the module logs an ERROR at the start and in `/readyz`
  (at most every 5 minutes) but keeps running;
- an ops account (`MFGRAPH_OPS_DSN`) with INSERT on `ops.module_outbox`
  (nothing else is needed; without `MFGRAPH_OPS_DSN` the forwarder uses
  `MFGRAPH_DSN`, whose account then needs that INSERT), and an mfgraph
  account with all privileges on the `mfgraph` schema (`MFGRAPH_DSN`, also
  used by `migrate`);
- the proxy limits: 1 GiB and 600 s for `/internal/v1/mfgraph/objects/` and
  `/internal/v1/mfgraph/uploads/`; the default 8 MiB elsewhere, including
  the older upload path `/internal/v1/mfgraph/workspaces/{id}/objects`;
- the identity headers on every forwarded request: `X-Atf-Internal-Token`,
  `X-Atf-Actor`, `X-Atf-Actor-Type`, `X-Atf-Project` (required),
  `X-Atf-On-Behalf-Of`, `X-Atf-On-Behalf-Of-Type`, `X-Atf-Team`,
  `X-Atf-Request-Id`, `X-Atf-Trace-Id`, `X-Atf-Token-Jti`, `X-Atf-Issued-At`,
  `X-Atf-Audience` (optional; see [Module mode](../../docs/module-mode.md)).

## Running the acceptance script

From the root of an unpacked bundle of the machine's platform:

```bash
mkdir bundle && go run ./tools/bundle extract dist/mfgraph-0.1.0-linux-amd64.tar.zst bundle   # or: tar --zstd -xf ... -C bundle
cd bundle
SOCK=/run/agentteams/modules/mfgraph.sock DATADIR=/data/mfgraph \
DSN='mfgraph:...@tcp(127.0.0.1:2881)/mfgraph' OPS_DSN='ops:...@tcp(127.0.0.1:2881)/ops' TOKEN=dev-token \
MYSQL='mysql -h127.0.0.1 -P2881 -uroot' scripts/acceptance-m1.0a.sh
# with BIG_UPLOAD=1 before the script: also the upload of 1 GiB straight to the module (row 5)
```

The variables are those of the host's template (`BIN`, `SOCK`, `BASE`,
`DATADIR`, `DSN`, `OPS_DSN`, `TOKEN`) plus `TENANT`, `PROJECT`,
`OTHER_PROJECT`, `ACTOR`, `ACTOR_TYPE`, `HDR`, `CONFIG` and `MYSQL` (a mysql
client command for the database checks); all have defaults and can be
overridden. `TENANT` defaults to the platform's tenant
`01M7MT9K23WZC95EECHGDBSYH7`; `PROJECT` and `OTHER_PROJECT` are new ULIDs
for every run (never the tenant). `PREPARE_DB=1` creates the two schemas
and the host table when they are missing (MFGraph's CI and local runs).
`BIG_UPLOAD=1` (off by default, and in MFGraph's CI) uploads 1 GiB of
random data straight to the module, which must answer within 600 s with the
right hash, and then one byte more, which must be refused with 413; it
needs about 2 GiB of free space. The migration runs with `MFGRAPH_DSN` as
the only variable (`env -i`), as the host runs it. The script needs bash,
curl with `--unix-socket`, python3 and the mysql client. It prints the
difference table in two parts, the module side with this run's results and
the host side for the host to fill in, with the states `PASS`,
`HOST-PENDING` and `FAIL`, and exits 1 if a row failed. Row 5 is
`HOST-PENDING` when it passes: the limits of the host's proxy are measured
there.

Differences from the host's template, because of the MFGraph API:

- every request carries `X-Atf-Actor` and `X-Atf-Actor-Type` (talking to
  the module directly, the script sets the headers that the kernel injects
  behind the host);
- JSON requests send `Content-Type: application/json` (otherwise 415);
- a workspace is created with `{"project_id": ..., "display_name": ...}`;
- a commit sends `branch_id`, `expected_generation` and `changes`;
- the tree is `GET /snapshots/{id}/tree` and a download
  `GET /snapshots/{id}/file?path=...`; uploads use the upload path
  `PUT /objects/{workspace_id}`;
- added checks: migrate twice and the recorded versions (row 6), a stale
  socket at start (row 1), 401 for a missing or wrong token, missing
  identity headers or an actor that is not UTF-8, 404 for another project's
  resources, `/api/v1` and `/projects` (rows 3, 4), a restore and the
  data after a restart (row 8);
- the outbox check (row 7) reads `ops.module_outbox` and checks the six
  registered kinds: five are emitted, `mfgraph.snapshot_created` is
  expected 0 times because M1.0a has no manual snapshot and no snapshot
  before a restore.

On a machine that is not Linux, build a bundle of its own platform first,
for example `make bundle BUNDLE_PLATFORMS=darwin/arm64`.

## Difference table

`acceptance-m1.0a-diff.md` is the host's table (appendix B) in two parts:
the module side, filled in beforehand from MFGraph's runs on MySQL 8.0 and
seekdb (with `BIG_UPLOAD=1`), and the host side, left empty for the host's
runs through its proxy. The states are `PASS`, `HOST-PENDING` and `FAIL`;
row 5 is `HOST-PENDING`: 1 GiB straight to the module is verified, 1 GiB
through the host's proxy is arranged separately. The script prints the same
two parts with the values of the run.
