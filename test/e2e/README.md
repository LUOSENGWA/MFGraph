# End-to-end acceptance tests

Black-box tests for the first core slice. `TestMain` builds `mfgraph-server`,
and each test starts it as a separate process with its own configuration,
talks to it over HTTP only, and stops it with SIGTERM.

The service needs a database, so the tests need one too: they run against
MySQL 8.0 or seekdb and are skipped, with that reason, when
`MFGRAPH_E2E_DB_DSN` is not set. CI runs them on both servers.

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
| `TestIdempotentCommitRetry` | retry with the same `Idempotency-Key` → same snapshot; different body → 409 `idempotency_key_reuse` |
| `TestInvalidPathRejected` | commit an invalid path → 422 `invalid_path` |
| `TestCrossWorkspaceIsNotFound` | the token of workspace A reading a snapshot of workspace B → 404 |
| `TestModeKeptWhenOmitted` | replacing a 493 file without `mode` keeps 493; new files get 420; an explicit `mode` wins |

A further test, `TestCLISmoke`, runs every subcommand of the `mfgraph`
command-line client against the server.

Environment passed to the server: `MFGRAPH_HTTP_ADDR` (a free local port),
`MFGRAPH_AUTH_TOKENS` (two principals, one with a plain-name actor reference
and one with a ULID), `MFGRAPH_STORAGE_ROOT` (a temporary directory for the
POSIX blob store), `MFGRAPH_DB_DSN` pointing at a fresh database of the
server of `MFGRAPH_E2E_DB_DSN` plus `MFGRAPH_MIGRATE_ON_START=true`, and a
log pseudonym key. With `MFGRAPH_E2E_DB_WAIT` (a duration, used for seekdb)
the tests first wait that long for the database. Set
`MFGRAPH_E2E_SERVER_BIN` to test a prebuilt binary.
