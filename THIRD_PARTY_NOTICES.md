# Third-party notices

MFGraph is licensed under Apache-2.0. It uses the Go modules below. Their
license texts are shipped in each module (`LICENSE` in the module root) and
must be preserved when redistributing binaries. `python3 scripts/check_deps.py`
verifies that every module compiled into a binary or a test is on this list
and that its license file matches.

Policy: dependencies must be under Apache-2.0, MIT, BSD or ISC. Copyleft
licenses (GPL, LGPL, AGPL, SSPL, BUSL, Commons Clause) are not accepted. The
MySQL driver is the single MPL-2.0 exception: it is used as an unmodified
dependency, and none of its source code is copied or changed.

## Linked into the binaries

| module | version | license | purpose |
|---|---|---|---|
| golang.org/x/text | v0.44.0 | BSD-3-Clause | Unicode NFC normalization and case folding of paths |
| github.com/klauspost/compress | v1.20.4 | BSD-3-Clause (zstd) | zstd compression of stored tree nodes |
| github.com/oklog/ulid/v2 | v2.1.2 | Apache-2.0 | ULID generation |
| github.com/go-sql-driver/mysql | v1.10.1 | MPL-2.0 (exception, unmodified) | MySQL driver |
| filippo.io/edwards25519 | v1.2.0 | BSD-3-Clause | dependency of the MySQL driver |
| github.com/oapi-codegen/runtime | v1.9.0 | Apache-2.0 | support code of the generated HTTP server |
| github.com/apapsch/go-jsonmerge/v2 | v2.0.0 | MIT | dependency of oapi-codegen/runtime |
| github.com/google/uuid | v1.6.0 | BSD-3-Clause | dependency of oapi-codegen/runtime |

## Tests only

| module | version | license | purpose |
|---|---|---|---|
| github.com/getkin/kin-openapi | v0.153.0 | MIT | validation of `contracts/openapi.yaml` |
| github.com/go-openapi/jsonpointer | v0.22.5 | Apache-2.0 | dependency of kin-openapi |
| github.com/go-openapi/swag/jsonname | v0.25.5 | Apache-2.0 | dependency of kin-openapi |
| github.com/santhosh-tekuri/jsonschema/v6 | v6.0.3 | Apache-2.0 | dependency of kin-openapi |
| go.yaml.in/yaml/v4 | v4.0.0 | Apache-2.0 (parts MIT) | dependency of kin-openapi |

## Development tools (not linked)

| tool | version | license | purpose |
|---|---|---|---|
| github.com/oapi-codegen/oapi-codegen/v2 | v2.8.2 | Apache-2.0 | generates `internal/api/gen` (`go generate`) |
| mysql:8.0 container image | 8.0 | GPL-2.0 (server) | development and CI database only; not distributed |
