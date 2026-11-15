# End-to-end acceptance tests

Black-box tests for the first core slice. `TestMain` builds `mfgraph-server`,
and each test starts it as a separate process with its own configuration,
talks to it over HTTP only, and stops it with SIGTERM.

```bash
make test-e2e                                    # no database
MFGRAPH_E2E_DB_DSN='root:mfgraph@tcp(127.0.0.1:3306)/' make test-e2e   # with MySQL (make dev-db-up)
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

Environment passed to the server: `MFGRAPH_HTTP_ADDR` (a free local port),
`MFGRAPH_AUTH_TOKENS` (two principals, one with a plain-name actor reference and one with a ULID), `MFGRAPH_STORAGE_ROOT` (a temporary
directory for the POSIX blob store of the first core slice), and, when
`MFGRAPH_E2E_DB_DSN` is set, `MFGRAPH_DB_DSN` pointing at a fresh database plus
`MFGRAPH_MIGRATE_ON_START=true`. Set `MFGRAPH_E2E_SERVER_BIN` to test a
prebuilt binary.

**Status:** the business endpoints are not implemented yet, so every test
fails at its first business request with `501 not_implemented`. That is the
expected state until the first core slice lands.
