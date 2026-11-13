# MFGraph content format, version 1

[English](content-format.md) | [简体中文](zh-CN/content-format.md)

This document specifies the byte-level formats that determine content and
tree hashes in MFGraph: content identity, the path grammar, manifest tree
nodes and storage keys. It is written so that another implementation can
produce and verify identical hashes. Test vectors are in
[`testdata/vectors`](../../testdata/vectors/README.md).

The key words MUST, MUST NOT, SHOULD and MAY are used as in RFC 2119.

Changing anything in this document that changes a hash requires a new format
version (`"format": 2`); data written with format 1 stays valid.

## 1. Content identity

1. The identity of a file is the SHA-256 of its exact bytes. Nothing is
   normalized: two files that differ only in line endings or Unicode
   normalization have different identities.
2. The identity does not depend on how the file is stored or transferred:
   chunk boundaries, multipart part sizes, storage backends and upload paths
   never influence it.
3. Textual form: `sha256:` followed by the 64-digit **lowercase** hex digest,
   for example `sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824`
   for `hello`. Parsers MUST reject every other spelling (uppercase digits,
   uppercase prefix, other algorithm names, missing prefix, surrounding
   whitespace, wrong length), so that every hash has one representation.
4. Database form: the 64-digit lowercase hex digest without prefix, plus a
   separate algorithm column whose value is `sha256`.

## 2. Paths

A path names a file relative to the workspace root. It is a sequence of
segments separated by `/`. Directories exist only as prefixes of file paths
(there are no empty directories), and symbolic links are not part of the data
model.

### 2.1 Normalization

The server normalizes every path with these steps, in this order. The first
step that fails determines the error kind.

1. The input MUST be valid UTF-8 (`invalid_utf8`) and non-empty (`empty`).
2. It MUST NOT start with `/` (`absolute`), and MUST NOT contain `\`
   (`backslash`) or a control character U+0000–U+001F or U+007F
   (`control_char`).
3. It is converted to Unicode Normalization Form C (NFC).
4. It is split on `/`. No segment may be empty (`a//b`, a trailing `/`:
   `empty_segment`) or equal to `.` or `..` (`dot_segment`).
5. Each segment MUST be at most 255 bytes (`segment_too_long`) and the whole
   path at most 4096 bytes (`path_too_long`), counted in UTF-8 bytes of the
   NFC form.

The result of step 3 is the canonical path. No other rule applies: for
example C1 control characters (U+0080–U+009F), bidirectional or zero-width
characters, the fullwidth solidus U+FF0F, names that are reserved on some
operating systems (`CON`, names ending in a dot or a space) are all allowed.

### 2.2 Siblings

Within one directory:

* the same name MUST NOT be used for a file and a directory (`file_dir_conflict`);
* the same path MUST NOT be given twice — including two inputs that only
  differ in Unicode normalization (`duplicate`);
* two different names MUST NOT have the same **fold key** (`case_conflict`).

The fold key of a name is the Unicode full case folding (CaseFolding.txt,
statuses C and F, without the Turkic mappings) of its NFC form. Examples:
`Plan.dwg` and `plan.dwg` conflict; `straße` and `STRASSE` conflict (both fold
to `strasse`); `σ` and `ς` conflict; `İ` (U+0130) and `i` do not.

### 2.3 Branch names

A branch name follows the rules of a single segment (steps 1–5 applied to
one segment) and therefore never contains `/`; a `/` gives
`contains_slash`. The NFC form is stored.

### 2.4 Unicode version

NFC and case folding depend on the Unicode character database. The
reference implementation uses Unicode 17.0.0. Results for characters that
are unassigned in an older Unicode version can differ between
implementations; the server's normalization is authoritative.

## 3. Tree nodes

A snapshot's content is a tree of immutable nodes. Each node is addressed by
the SHA-256 of its canonical bytes; the root node's hash is the snapshot's
`root_tree_hash`.

### 3.1 Canonical encoding

Nodes are JSON serialized according to RFC 8785 (JSON Canonicalization
Scheme), restricted to objects, arrays, strings and integers:

* object keys are sorted (all keys are ASCII, so plain byte order applies);
  there are no duplicate keys;
* no whitespace between tokens; UTF-8 without a byte order mark; no
  trailing newline;
* strings escape exactly: `"` as `\"`, `\` as `\\`, U+0008 as `\b`, U+0009 as
  `\t`, U+000A as `\n`, U+000C as `\f`, U+000D as `\r`, other code points below
  U+0020 as `\u00xx` with lowercase hex digits; every other character,
  including `/`, `<`, `>`, `&`, U+007F and all non-ASCII characters, is
  written as raw UTF-8;
* integers are written in decimal without sign, leading zeros, fraction or
  exponent, and lie in the range 0 … 2^53−1 (so that any RFC 8785
  implementation reproduces them).

Array order is not defined by RFC 8785; it is defined below.

### 3.2 Node kinds

Every node has `"format": 1` and one of three kinds. The keys appear in the
order shown (which is the RFC 8785 order).

| kind | shape |
|---|---|
| `dir` | `{"entries":[<entry>,…],"format":1,"kind":"dir"}` — a whole directory |
| `dir_page` | `{"entries":[<entry>,…],"format":1,"kind":"dir_page"}` — one page of a large directory (never empty) |
| `dir_index` | `{"format":1,"kind":"dir_index","pages":[<ref>,…]}` — references to pages or lower-level indexes (never empty) |

A node has at most 1024 entries or references and its canonical encoding is
at most 1,048,576 bytes.

### 3.3 Entries

| type | shape |
|---|---|
| file | `{"hash":"sha256:<hex>","mode":420,"name":"a.txt","size":1234,"type":"file"}` |
| directory | `{"hash":"sha256:<hex>","name":"docs","type":"dir"}` |

* `name` is one path segment in canonical (NFC) form.
* A file's `hash` is its content identity (section 1), `size` its length in
  bytes, `mode` either `420` (0644, regular) or `493` (0755, executable).
  Implementations map local permissions to `493` when any execute bit is set
  and to `420` otherwise. No other metadata is stored: no modification
  time, owner or other permission bits.
* A directory's `hash` is the hash of the directory's top node: a `dir` node
  or a top-level `dir_index` node, never a `dir_page`.
* Entries are sorted by the UTF-8 bytes of `name`, strictly ascending (this
  is not UTF-16 order: `｡` U+FF61 sorts before `𝄞` U+1D11E). Siblings obey
  section 2.2.

### 3.4 Index references

A reference in a `dir_index` is `{"count":N,"first":"<name>","hash":"sha256:<hex>"}`:

* `hash` is the hash of a `dir_page` or of a lower-level `dir_index`;
* `count` is the number of directory entries below the referenced node (for
  a lower-level index, the sum of its counts);
* `first` is the name of the first directory entry below the referenced node.

References are sorted by `first`, strictly ascending in UTF-8 byte order.

### 3.5 Splitting large directories

The thresholds are fixed for format 1; they cannot be configured.

```text
layout(entries):                      # entries sorted as in 3.3
  if len(entries) <= 1024 and size(dir node of entries) <= 1048576:
      return node("dir", entries)
  pages = greedy(entries, "dir_page")
  refs  = [ref(count=len(p), first=p[0].name, node("dir_page", p)) for p in pages]
  while len(refs) > 1024 or size(dir_index node of refs) > 1048576:
      groups = greedy(refs, "dir_index")
      refs   = [ref(count=sum(r.count for r in g), first=g[0].first, node("dir_index", g))
                for g in groups]
  return node("dir_index", refs)

greedy(items, kind):                  # keep the order, fill each node in turn
  start a new node; for each item: if adding it would exceed 1024 items or
  make the encoded node larger than 1048576 bytes, close the node and start
  a new one; then add the item.
```

The size of a node with items `i1…in` is the length of its encoding, that is
the size of the empty node of that kind plus the item sizes plus `n−1`
commas. The empty encodings are 38 bytes (`dir`), 43 bytes (`dir_page`) and
42 bytes (`dir_index`).

Consequences: 1024 entries give one `dir` node; 1025 entries give a
`dir_index` over pages of 1024 and 1 entries; more than 1024 × 1024 entries
add a second index level. Inserting a name near the start of a large
directory rewrites the following pages (accepted in format 1).

Informative: with the path limits of section 2 a single entry encodes to at
most 651 bytes, so 1024 entries stay below about 667 KB and the byte limit
does not trigger for valid names; it remains part of the format.

### 3.6 The root and empty directories

The root is a `dir` or a `dir_index` node. Only the root may be empty, in
which case it is `{"entries":[],"format":1,"kind":"dir"}` (hash
`sha256:e515bab4a24abb9c4929143865d247855e153af697b270afb65a0dcb1b62d9c4`).
A directory entry that points to an empty node is invalid.

### 3.7 Node hash

The hash of a node is the SHA-256 of its **uncompressed** canonical bytes, in
the textual form of section 1. How nodes are compressed or stored does not
affect it.

### 3.8 Strict decoding

A reader MUST reject a node unless all of the following hold:

1. it is at most 1,048,576 bytes, valid UTF-8, a single JSON object with
   exactly the keys of its kind, `format` = 1 and a known `kind`;
2. entries and references have exactly the keys of their type; names are
   valid segments already in NFC; hashes are in canonical form; modes are
   420 or 493; integers are in range; there is no symlink or other type;
3. names (and `first` values) are strictly ascending and siblings in the node
   obey section 2.2; a `dir_page` or `dir_index` is not empty;
4. re-encoding the decoded node with section 3.1 gives exactly the input
   bytes, and the SHA-256 of the bytes equals the expected hash.

A reader that walks a tree MUST additionally check that no directory other
than the root is empty, that `count` and `first` of every reference match
the referenced node, that entries are ascending and conflict-free across all
pages of a directory, and that every paged directory is split exactly as
section 3.5 prescribes. Together these rules give every set of files exactly
one valid tree.

### 3.9 Example

A tree with the single file `a.txt` containing `hello\n` (6 bytes):

```json
{"entries":[{"hash":"sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03","mode":420,"name":"a.txt","size":6,"type":"file"}],"format":1,"kind":"dir"}
```

Its hash, and therefore the root hash, is
`sha256:c054d695dcf341ac6d5570a0a7e0c1cbcf14379854ea857a1643c9bddf34695a`.

## 4. Storage keys

Objects are stored under keys derived from hashes. `<hex>` is the 64-digit
lowercase SHA-256 and `<hh>` its first two characters.

| object | relative key |
|---|---|
| file content | `objects/<hh>/<hex>` |
| tree node | `tree/<hh>/<hex>.json.zst` — the zstd-compressed canonical bytes; `<hex>` is the hash of the uncompressed bytes |

* The full object key is `<storage_prefix>/<relative key>`. Databases store
  only relative keys.
* `storage_prefix` is chosen when a workspace is created and never changes.
  It is non-empty, has no leading or trailing `/`, each of its segments
  satisfies section 2, it is in NFC, and it is at most 1024 bytes.
* The default prefix of a project-level workspace is
  `tenants/<tenant_id>/projects/<project_id>/workspaces/<workspace_id>`.
  A host platform may choose another prefix.
* Content objects are never shared between workspaces.
* The compression of tree nodes is a storage detail and may change; readers
  decompress and verify the hash of the canonical bytes.

## 5. Checking an implementation

* `testdata/vectors/contenthash.json`, `pathx.json` and `tree.json` contain
  inputs and expected results; see the
  [vectors README](../../testdata/vectors/README.md) for the generator rules.
* `mfgraph debug tree <dir> --out <out>` prints every node of the tree of a
  local directory and writes each node's canonical bytes to
  `<out>/<hex>.json`; `shasum -a 256` over those files must reproduce the
  file names.
