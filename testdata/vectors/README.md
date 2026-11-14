# Test vectors

Language-neutral vectors for the frozen content formats. Any implementation
of the format can check itself against these files; the Go tests and an
independent Python implementation (`scripts/vectors.py`, standard library
only) both do.

**Status: pending human review** — see [REVIEW.md](REVIEW.md) for the
human-readable summary of every vector and how the expected values were
obtained.

| file | contents |
|---|---|
| `contenthash.json` | content hash inputs (UTF-8 text, hex bytes, or the `sha256-counter` generator) with expected size and `sha256:<hex>`; strict-parsing accept/reject lists |
| `pathx.json` | write-time path normalization (expected NFC output or error kind), case-fold keys, sibling conflicts, branch names, and `stored_names`: the read-time rules for names inside stored nodes |
| `tree.json` | tree cases (input files → root hash, node count, nodes in walk order with canonical bytes), inputs that `Build` must reject with the expected error kind, node byte strings a strict decoder must reject (`decode_invalid`), and node byte strings a decoder must accept although the write path never produces them (`decode_valid_build_rejects`) |
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
