# Changelog

All notable changes will be documented here.

## [Unreleased]

### M0: contracts and code skeleton

- Content format version 1 specified in `docs/spec/content-format.md` (English and Chinese): whole-file SHA-256 content identity, path grammar, canonical tree nodes (`dir`, `dir_page`, `dir_index`) with fixed split thresholds, storage keys.
- Reference implementation of the formats (`internal/contenthash`, `internal/pathx`, `internal/tree`, `internal/storagekey`) with test vectors in `testdata/vectors`, checked against an independent Python implementation and `shasum`; `mfgraph debug hash|tree` for inspecting trees.
- MySQL migrations for the first core slice and a checksum-pinned migration runner (`mfgraph-server migrate`).
- OpenAPI 3.0.3 contract of the first core slice (`contracts/openapi.yaml`), generated server code, flat error envelope, request IDs and static bearer tokens; business endpoints answer `501 not_implemented`.
- Core ports with in-memory implementations, end-to-end acceptance tests (expected to fail until the first core slice), CI workflow and a dependency license check.

### Earlier

- Renamed the project from **MFGit** to **MFGraph** (module/binary/env prefixes: `mfgraph` / `MFGRAPH_`).
- Initial design/spec repository.
- Architecture, data model, storage, API, workflow, security, deployment, FAQ.
