# End-to-end acceptance tests

Black-box tests for the first core slice. `TestMain` builds `mfgraph-server`,
and each test starts it as a separate process with its own configuration,
talks to it over HTTP only, and stops it with SIGTERM.

The service needs a database, so the tests need one too: they run against
MySQL 8.0 or seekdb and are skipped, with that reason, when
`MFGRAPH_E2E_DB_DSN` is not set. CI runs them on both servers with
`MFGRAPH_E2E_REQUIRE_DB=1`, which makes a missing DSN fail the run instead
of skipping every test (`make test-e2e-mysql` and `make test-e2e-seekdb` set
it too).

```bash
make dev-db-up && make test-e2e-mysql        # MySQL 8.0: root:mfgraph@tcp(127.0.0.1:3306)/
make dev-seekdb-up && make test-e2e-seekdb   # seekdb:    root@tcp(127.0.0.1:2881)/
MFGRAPH_E2E_DB_DSN='root:mfgraph@tcp(127.0.0.1:3306)/' make test-e2e   # any DSN of a user that may create databases
```

| test | acceptance criterion |
|---|---|
| `TestUploadCommitBrowseDownload` | create project → workspace → upload 2 files → commit → browse the tree → download identical content (plus a Range request) |
| `TestDiffShowsOnlyTheChangedFile` | change 1 file and commit → the diff lists exactly that file |
| `TestRestoreCreatesNewSnapshot` | restore the first snapshot → a new snapshot whose tree equals the first one |
| `TestDataSurvivesRestart` | all of the above is still there after a server restart |
| `TestConcurrentCommitsOneWins` | two commits with the same `expected_generation` → one 201, one 409 |
| `TestRestoreAndCommitRace` | a restore and a commit with the same `expected_generation` → one 201, one 409; the branch moves once |
| `TestIdempotentCommitRetry` | retry with the same `Idempotency-Key` → same snapshot; different body → 409 `idempotency_key_reuse` |
| `TestInvalidPathRejected` | commit an invalid path → 422 `invalid_path` |
| `TestCrossWorkspaceIsNotFound` | the token of workspace A reading a snapshot of workspace B → 404 |
| `TestModeKeptWhenOmitted` | replacing a 493 file without `mode` keeps 493; new files get 420; an explicit `mode` wins |

A further test, `TestCLISmoke`, runs every subcommand of the `mfgraph`
command-line client against the server.

The `TestModuleMode*` tests run the same binary in [module mode](../../docs/module-mode.md):
each prepares a schema for the module and an ops schema with a fixture of
the host's outbox table, runs `mfgraph-server --config FILE migrate`, starts
`mfgraph-server serve --config FILE --socket PATH` on a Unix domain socket
and sends requests with identity headers (a generic header prefix; the
host's own settings are checked by the acceptance script of
`packaging/host-module`).

| test | acceptance criterion |
|---|---|
| `TestModuleModeMigrate` | `migrate` twice, with `--config` before or after the command; only the module's versions are recorded; no kernel tables |
| `TestModuleModeMigrateOnlyDSN` | `mfgraph-server --config packaging/host-module/module.env migrate` with `MFGRAPH_DSN` as the only variable of the environment (as the host runs it): it succeeds twice without a warning, records the module's 7 versions and creates no kernel tables |
| `TestModuleModeStartup` | `/readyz` ready with the checks db, storage and outbox; socket mode 0600, a stale socket replaced; no TCP listener (lsof or /proc); not ready while the host table is gone |
| `TestModuleModePrefixAndIdentity` | `/internal/v1/mfgraph` works, `/api/v1` is 404; uploads through `/objects/{workspace_id}`; the actor, its type and the actor acted for are recorded; the request ID of the proxy is used; 401 without the token, with a wrong one, without actor, type or project, with an actor that is not UTF-8 or has 256 bytes, or with a bearer token only |
| `TestModuleModeACL` | every endpoint with another project gives the 404 of an unknown ID; a workspace cannot be created in another project; `/projects` is 404 |
| `TestModuleModeEvents` | the host table holds the events in order with their columns, envelopes, derived IDs and reasons and a 255-byte actor; failed commits and a replay add nothing; redelivery is absorbed; events wait while the table is gone and arrive after; every kind is in `docs/events.md` and exactly five kinds are emitted |
| `TestModuleModeSIGTERM` | SIGTERM ends the process within 8 s and removes the socket; after a restart the data is there and the events that could not be delivered arrive |
| `TestModuleModeSIGTERMUnresponsive` | SIGTERM ends the process within 8 s (measured: 7.0 s) when the forwarder's database accepts connections and never answers, and when the module's database stops answering while a request waits for it |
| `TestModuleModeUnicodeActor` | an actor with Chinese characters and an emoji arrives unchanged in the host table (the forwarder's connection is utf8mb4) |
| `TestModuleModeStalledForwarder` | a host table too narrow for the actor stalls delivery: after 5 failures `/readyz` is still 200 with `checks.outbox` `stalled` and the field `outbox` (backlog, error), the log has an ERROR line; after widening the column the events arrive with an INFO line |
| `TestModuleModeRedeliveryAnyKeyName` | the host's unique key on `event_id` has another name; after a SIGKILL with the local rows set back to pending the events are delivered again and recognized as delivered; a table without the unique index gives an ERROR line and the module runs |
| `TestModuleModeConfig` | a TCP address, a missing socket, token, DSN, data directory or tenant, the all-zero tenant, a wrong `MODULE_ID`, disagreeing aliases, `MFGRAPH_KERNEL_MIGRATIONS=true`, `${...}` in the file, a malformed DSN and a forwarder DSN with another character set (`utf8`, `latin1`, a list) stop the start with exit code 2, without leaking secrets; a short token only warns |

`TestUpgradeFromMain` upgrades the schema and data of the last release
before migration 0009: it builds the server of commit `dd5359e` (main) from
the git history (`git archive`, then `go build` without network access),
lets it create a project, a workspace, commits, an idempotent commit and a
restore, and then checks this build: `/readyz` is 503 `schema_behind`
before `migrate`, `migrate` applies only `0009_actor_types`, the old data is
served and the old idempotent commit replayed, the schema equals that of a
fresh installation, and the old server runs on the new schema only without
`MFGRAPH_MIGRATE_ON_START`. Without the git history (a shallow clone) the
test is skipped; CI checks out the full history and sets
`MFGRAPH_E2E_REQUIRE_UPGRADE=1`, which makes that a failure.
`MFGRAPH_E2E_UPGRADE_FROM` names another commit.

Environment passed to the server: `MFGRAPH_HTTP_ADDR` (a free local port),
`MFGRAPH_AUTH_TOKENS` (two principals, one with a plain-name actor reference
and one with a ULID), `MFGRAPH_STORAGE_ROOT` (a temporary directory for the
POSIX blob store), `MFGRAPH_DB_DSN` pointing at a fresh database of the
server of `MFGRAPH_E2E_DB_DSN` plus `MFGRAPH_MIGRATE_ON_START=true`, and a
log pseudonym key. With `MFGRAPH_E2E_DB_WAIT` (a duration, used for seekdb)
the tests first wait that long for the database. Set
`MFGRAPH_E2E_SERVER_BIN` to test a prebuilt binary.
