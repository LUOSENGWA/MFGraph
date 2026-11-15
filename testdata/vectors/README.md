# Test vectors

Language-neutral vectors for the frozen content formats. Any implementation
of the format can check itself against these files; the Go tests and an
independent Python implementation (`scripts/vectors.py`, standard library
only) both do.

**Status: reviewed** by expldfsh on 2026-11-14 (`review_status`,
`reviewed_by` and `reviewed_on` in each file) — see [REVIEW.md](REVIEW.md)
for the human-readable summary of every vector and how the expected values
were obtained. A change to an expected value needs a new review: set
`review_status` back to `pending-human-review` and remove the two fields.

| file | contents |
|---|---|
| `contenthash.json` | content hash inputs (UTF-8 text, hex bytes, or the `sha256-counter` generator) with expected size and `sha256:<hex>`; strict-parsing accept/reject lists |
| `pathx.json` | write-time path normalization (expected NFC output or error kind), case-fold keys, sibling conflicts, branch names, `stored_names`: the read-time rules for names inside stored nodes, and `lookup`: the rules for a path that finds an existing entry (expected NFC output or error kind) |
| `tree.json` | tree cases (input files → root hash, node count, nodes in walk order with canonical bytes; `walk_order` where given), inputs that `Build` must reject with the expected error kind, node bytes (hex) a strict decoder must reject (`decode_invalid`), node bytes a decoder must accept although the write path never produces them (`decode_valid_build_rejects`), and whole trees (nodes + root hash) a reader must accept (`walk_valid`) or reject with the category `invalid_node` or `invalid_tree` (`walk_invalid`) |
| `REVIEW.md` | generated summary for reviewers |

## Generators

Large inputs are described by rules instead of being stored:

* `contenthash.json`, generator `sha256-counter`: concatenate
  `SHA-256(seed_utf8 || uint32_big_endian(i))` for `i = 0, 1, 2, …` and truncate
  to `length` bytes.
* `pathx.json`, `input_gen`/`output_gen`: segments joined by `/`; each segment is
  `unit` repeated `repeat` times, emitted `times` times (default 1).
* `tree.json`, `generate`: for `i` in `[start, start+count)`, the path is
  `dir + "/" + prefix×prefix_repeat + i` zero-padded to `digits` digits (no
  `dir/` when `dir` is empty); the file content is the UTF-8 bytes of the path
  and the mode is 420. Explicit `files` give `path`, UTF-8 `content` and an
  optional `mode` (default 420); a file's hash is the SHA-256 of its content.
* `tree.json`, `node_gen` (nodes too large to list as hex): a node of the given
  `kind` with `count` items named `prefix` + the index zero-padded to `digits`
  digits, for the index in `[start, start+count)`: file entries with the hash
  of the empty content, mode 420 and size 0 (`dir`, `dir_page`), or references
  with count 1, `first` = the name and the hash of the empty content
  (`dir_index`). The bytes are the canonical encoding; the names are not
  checked, so a generator can describe an invalid node. `bytes_sha256` (or the
  node's `hash`) is the SHA-256 of the expansion, to check it.

Cases marked `"heavy": true` (over a million entries) are skipped by default;
run them with `MFGRAPH_HEAVY_VECTORS=1 go test ./internal/tree` and
`python3 scripts/vectors.py verify --heavy`.

## Write-time and read-time rules

Paths that are uploaded or committed go through the **write-time rules**
(`pathx.Normalize`, `tree.Build`): NFC, case-fold conflicts, and rejection of
control characters, noncharacters, a fixed list of bidi and invisible
characters, and code points unassigned in Unicode 15.0.0. Names read back
from stored nodes go through the **read-time rules**
(`pathx.ValidateStoredName`, `tree.Decode`, `tree.Walk`), which are structural
(valid UTF-8, no `/` or `\`, no control characters or noncharacters, not `.`
or `..`, at most 255 bytes) and never depend on Unicode tables, so stored
trees stay readable when the write-time rules change.
See [the specification](../../docs/spec/content-format.md).

## Error kinds

An error kind is normative only when the input breaks a single rule. Cases
marked `"multiple_violations": true` break several rules; their kind is the
one the reference implementation reports (the first failing step, and the
first offending character from left to right), which the Go tests and
`scripts/vectors.py` check, but another implementation may report any of the
violated kinds.

## Unicode version

Cases tagged `"ucd_version": "15.0.0"` depend on the Unicode version: they
hold for V = 15.0.0 (the version fixed by the specification) only. They pin
V: U+1F6DC (assigned in Unicode 15.0) is accepted, and U+2EBF0 (assigned in
15.1), U+1C89 and U+1F6D8 (assigned in 16.0) are rejected as unassigned. The
Go tests always check them (they fail if the Unicode tables are not V);
`scripts/vectors.py` checks them only when Python's `unicodedata` has
version 15.0.0 (Python 3.12) and otherwise skips them and prints the number
skipped. `generate` keeps their stored expected values in that case.

## Workflow

```bash
python3 scripts/vectors.py verify [--heavy]   # Python reference vs. the files
python3 scripts/vectors.py crosscheck         # Go CLI on materialized inputs + shasum
python3 scripts/vectors.py generate           # recompute expected values in tree.json
python3 scripts/vectors.py format             # rewrite the files as ASCII (\u escapes)
python3 scripts/vectors.py review             # regenerate REVIEW.md
```

The JSON files are pure ASCII: every non-ASCII character (and DEL) is written
as a `\u` escape, so that an editor cannot silently normalize an NFD input or
rewrite a combining mark, a compatibility character or an invisible
character. The Go tests and `verify` reject raw non-ASCII bytes; `format`
rewrites a hand-edited file. `REVIEW.md` shows the characters.

Expected values must never be produced by the Go code under test. Inputs of
`contenthash.json` and `pathx.json` and all expected values of `pathx.json` are
written by hand; expected hashes of `contenthash.json` come from `shasum -a 256`
and Python `hashlib`; expected values of `tree.json` come from the Python
reference implementation.
