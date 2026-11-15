// Package migrate applies forward-only, checksum-pinned SQL migrations.
//
// Rules:
//
//   - Bookkeeping lives in one table, schema_migrations(component, version,
//     checksum, applied_at_us), shared by all components ("kernel" for the
//     standalone kernel tables, "mfgraph" for the module schema).
//   - Files run in file name order (NNNN_description.sql). Numbers may have
//     gaps, and a gap may be filled later.
//   - checksum = SHA-256 of the file bytes. An applied file whose checksum
//     changed, or that disappeared, stops the run before anything executes:
//     migration files are immutable.
//   - MySQL DDL commits implicitly, so statements run one by one outside a
//     transaction and the version is recorded after the last statement
//     succeeded. Every statement is CREATE TABLE IF NOT EXISTS, so a run that
//     stopped half-way is simply repeated.
//   - One runner at a time. There is no locking between concurrent runners;
//     deployments must run migrations from a single process.
package migrate
