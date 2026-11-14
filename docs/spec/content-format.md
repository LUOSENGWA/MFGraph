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

Sections 2.1–2.3 are the **write-time rules**: a server applies them to every
path that is uploaded or committed, and a tree writer applies them before
encoding. Names read back from stored tree nodes are checked only with the
**read-time rules** of section 2.5, which never depend on Unicode tables.

### 2.1 Normalization

The server normalizes every path with these steps, in this order. The first
step that fails determines the error kind.

1. The input MUST be valid UTF-8 (`invalid_utf8`) and non-empty (`empty`).
2. It MUST NOT start with `/` (`absolute`). Then its characters are scanned
   from left to right, and the first offending character determines the
   error kind:
   * `\` (`backslash`);
   * a control character U+0000–U+001F or U+007F–U+009F, that is C0, DEL
     and C1 (`control_char`);
   * a noncharacter: U+FDD0–U+FDEF and the last two code points of every
     plane, U+xxFFFE and U+xxFFFF — 66 code points in all (`noncharacter`);
   * a character of the fixed disallowed list (`disallowed_char`): the
     bidirectional formatting characters U+061C, U+200E, U+200F,
     U+202A–U+202E, U+2066–U+2069, and the invisible or layout-breaking
     characters U+00AD (soft hyphen), U+180E (Mongolian vowel separator),
     U+200B (zero width space), U+2028 and U+2029 (line and paragraph
     separators), U+2060 (word joiner), U+2061–U+2064 (invisible
     mathematical operators), U+FEFF (zero width no-break space) and
     U+FFF9–U+FFFB (interlinear annotation controls);
   * a code point that is unassigned (general category Cn) in Unicode version
     V (`unassigned_char`; see 2.4). The noncharacters are Cn too; the
     previous rule reports them as `noncharacter`.
3. It is converted to Unicode Normalization Form C (NFC).
4. It is split on `/`. No segment may be empty (`a//b`, a trailing `/`:
   `empty_segment`) or equal to `.` or `..` (`dot_segment`).
5. Each segment MUST be at most 255 bytes (`segment_too_long`) and the whole
   path at most 4096 bytes (`path_too_long`), counted in UTF-8 bytes of the
   NFC form. 255 bytes is the file name limit of Linux ext4 (and of most
   POSIX file systems), and 85 CJK characters (3 bytes each in UTF-8) are
   enough for a file name. The limit is also a read-time rule (2.5), so
   format 1 can never raise it.

All character checks of step 2 run on the input before NFC and before the
segment rules; for example `../a` followed by U+200B gives `disallowed_char`,
not `dot_segment`. The result of step 3 is the canonical path.

Why: control characters and the bidirectional formatting characters can make
a name display differently from what it is (spoofing, "Trojan Source");
invisible characters make different names look identical, and line or
paragraph separators break listings; unassigned code points have no defined
normalization or case folding yet (see 2.4); noncharacters are forbidden by
I-JSON (RFC 7493), on which RFC 8785 builds. The disallowed list is fixed:
other format characters (category Cf) are not rejected as a class. It is a
write-time rule only (2.5): it may be extended later, and stored names that
contain such characters stay readable.

No other rule applies. In particular these are **allowed**:

* ZWNJ U+200C and ZWJ U+200D — emoji sequences (for example
  U+1F468 U+200D U+1F469 U+200D U+1F467) and Persian, Indic and other
  scripts need them;
* the variation selectors U+FE00–U+FE0F — emoji presentation needs them (for
  example U+2764 U+FE0F);
* the tag characters U+E0020–U+E007F — subdivision flags need them (the flag
  of Scotland is U+1F3F4 followed by the tags for `gbsct` and U+E007F);
* U+034F COMBINING GRAPHEME JOINER;
* the fullwidth solidus U+FF0F — it is not a separator;
* private-use characters — they are assigned (category Co);
* names that are reserved or awkward on some operating systems (`CON`, names
  ending in a dot or a space) — mapping them is left to export tools, not to
  the format.

### 2.2 Siblings

Within one directory:

* the same name MUST NOT be used for a file and a directory (`file_dir_conflict`);
* the same path MUST NOT be given twice — including two inputs that only
  differ in Unicode normalization (`duplicate`);
* two different names MUST NOT have the same **fold key** (`case_conflict`).

The fold key of a name is its canonical caseless form as defined by the
Unicode Standard (section 3.13, definition D145):

```text
fold_key(name) = NFD(casefold(NFD(name)))
```

where casefold is the Unicode full case folding (CaseFolding.txt, statuses C
and F, without the Turkic mappings T). Two names conflict when their fold
keys are equal. The fold key is only compared, never stored. Examples:

* `Plan.dwg` and `plan.dwg` conflict; `straße` and `STRASSE` conflict (both
  fold to `strasse`); `σ` and `ς` conflict;
* Cherokee folds to the uppercase letters: U+13A0 and U+AB70 conflict, and so
  do U+13F0 and U+13F8;
* Georgian Mtavruli folds to Mkhedruli: U+1C90 and U+10D0 conflict;
* the outer NFD makes the key closed under canonical equivalence: U+1E9E
  U+0301 and `s` U+015B conflict (both give `s s` U+0301), and so do U+FB01
  U+0301 and `f` U+00ED;
* `İ` (U+0130) and `i` do not conflict, and neither do `ı` (U+0131) and `I`.

### 2.3 Branch names

A branch name follows the rules of a single segment (steps 1–5 applied to
one segment) and therefore never contains `/`; a `/` gives
`contains_slash`. The NFC form is stored.

### 2.4 Unicode version V

NFC, case folding and the `unassigned_char` rule depend on the Unicode
Character Database. Format 1 fixes the version of these tables:
**V = 15.0.0**. The write-time rules of every implementation MUST use the
tables of Unicode 15.0.0, whatever its platform provides. (The reference
implementation takes the general categories from the Go standard library and
NFC and case folding from golang.org/x/text, both at Unicode 15.0.0 with Go
1.26; its tests fail if a toolchain upgrade changes either.)

* V is raised only by recording the new version number in this document.
  Unicode's stability policies keep assigned code points assigned and keep
  NFC and case folding of assigned characters unchanged, so raising V only
  assigns more code points: the set of allowed characters only grows, paths
  that were valid stay valid with the same canonical form, and existing data
  is not affected.
* Because every unassigned code point is rejected at write time, stored
  names never contain characters whose normalization a later version could
  define differently.
* The read-time rules (2.5) never use Unicode tables, so a different V — in
  another implementation, or after V is raised — never makes stored data
  unreadable.
* Test vectors whose result depends on V carry `"ucd_version": "15.0.0"`:
  U+1F6DC (assigned in Unicode 15.0) is accepted, and U+2EBF0 (assigned in
  15.1), U+1C89 and U+1F6D8 (assigned in 16.0) are `unassigned_char`.

### 2.5 Write-time and read-time rules

| rule | write time (upload, commit, `Build`) | read time (decoding stored nodes, walking trees) |
|---|---|---|
| valid UTF-8, non-empty | ✓ | ✓ |
| no `/` in a name; no `\` | ✓ | ✓ |
| no control characters (C0, DEL, C1) | ✓ | ✓ |
| no noncharacters (U+FDD0–U+FDEF, U+xxFFFE, U+xxFFFF) | ✓ | ✓ |
| no character of the fixed disallowed list (2.1) | ✓ | — |
| not `.` or `..` | ✓ | ✓ |
| at most 255 bytes per name | ✓ (after NFC) | ✓ (as stored) |
| at most 4096 bytes per path | ✓ | — |
| no unassigned code points (in V) | ✓ | — |
| NFC | applied (the canonical form is stored) | not checked |
| no case-fold conflicts between siblings | ✓ | not checked |
| no duplicate names, no name used as both file and directory | ✓ | ✓ (implied by strictly ascending names, 3.8) |

The read-time rules are a subset of the write-time rules, so everything a
conforming writer produces is readable. They are structural, never use
Unicode tables and are fixed for format 1. A reader MUST NOT reject a stored
name because it is not in NFC, because two names have the same fold key,
because it contains a code point unassigned in the reader's Unicode version,
or because it contains a character of the fixed disallowed list. The
write-time rules can therefore change (a new V, a longer disallowed list)
without making stored data unreadable. The test vectors list such nodes in
`decode_valid_build_rejects`.

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
  exponent, and lie in the range 0 … 2^53−1. Reason: RFC 8785 serializes
  numbers as IEEE-754 double-precision values (the ECMAScript Number type),
  which represent every integer exactly only up to 2^53−1; larger values
  could be printed differently by conforming implementations. As a file
  size, 2^53−1 bytes (about 9 PB) is far above any supported size.

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

* `name` is one path segment. Writers store the canonical (NFC) form that
  section 2 produces; readers check names with the read-time rules (2.5)
  only.
* A file's `hash` is its content identity (section 1), `size` its length in
  bytes, `mode` either `420` (0644, regular) or `493` (0755, executable).
  Implementations map local permissions to `493` when any execute bit is set
  and to `420` otherwise. No other metadata is stored: no modification
  time, owner or other permission bits.
* A directory's `hash` is the hash of the directory's top node: a `dir` node
  or a top-level `dir_index` node, never a `dir_page`.
* Entries are sorted by the UTF-8 bytes of `name`, strictly ascending (this
  is not UTF-16 order: `｡` U+FF61 sorts before `𝄞` U+1D11E). Writers ensure
  that siblings obey section 2.2.

### 3.4 Index references

A reference in a `dir_index` is `{"count":N,"first":"<name>","hash":"sha256:<hex>"}`:

* `hash` is the hash of a `dir_page` or of a lower-level `dir_index`;
* `count` is the number of **directory entries** (the leaves: files and
  subdirectories) below the referenced node — for a `dir_page` the number of
  its entries, for a lower-level `dir_index` the sum of the `count` values of
  its references. The `count` values of a top-level index therefore add up to
  the number of entries of the directory;
* `first` is the name of the first directory entry (leaf) below the
  referenced node — for a lower-level index, the `first` of its first
  reference.

References are sorted by `first`, strictly ascending in UTF-8 byte order.

Example (test vector `two-level-index-1048577-entries`): a directory with the
1,048,577 files `f0000000` … `f1048576` is split into 1025 pages (1024 pages
of 1024 entries and one page with 1 entry). The 1025 page references exceed
1024, so they are packed into two level-1 indexes (with 1024 and 1
references), and the top-level index is:

```json
{"format":1,"kind":"dir_index","pages":[{"count":1048576,"first":"f0000000","hash":"sha256:a3d74c9e09aab22419a4f5d1324793ca517abd5df41ffea1e690fda9ee224095"},{"count":1,"first":"f1048576","hash":"sha256:25568880f10538654d433d8b9a01bd94b6bb770e5464b90a77ba0b4f3c094509"}]}
```

Its first reference covers 1024 × 1024 = 1,048,576 entries starting with
`f0000000`; the second covers the last entry. The second level-1 index is
`{"format":1,"kind":"dir_index","pages":[{"count":1,"first":"f1048576","hash":"sha256:88a61f4436ce70dac4a0b057cf7b092b9e473d970cd61e4404f6d240dea79db6"}]}`,
and the directory's hash (the hash of the top-level index) is
`sha256:c63ef4eaae5650368cdb6fac7d9dd85a4d895db54ee8e3bdd17a478bfbd9767c`.

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

The 1,048,576-byte limit stays in format 1 as a defensive upper bound, but
it cannot be reached under the path rules of section 2: a name has at most
255 bytes and only `"` needs escaping (2 bytes), so a file entry encodes to
at most 651 bytes, 1024 entries stay below about 667 KB, and 1024 index
references below about 644 KB. Splitting therefore always happens at 1024
items in format 1. No test vector can exercise the byte limit; implementations
should test it with reduced thresholds (the reference implementation does).

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
2. entries and references have exactly the keys of their type; names and
   `first` values satisfy the read-time rules (2.5); hashes are in canonical
   form; modes are 420 or 493; integers are in range; there is no symlink or
   other type;
3. names (and `first` values) are strictly ascending in UTF-8 byte order —
   which excludes duplicates and a name used as both file and directory; a
   `dir_page` or `dir_index` is not empty;
4. re-encoding the decoded node with section 3.1 gives exactly the input
   bytes, and the SHA-256 of the bytes equals the expected hash.

A reader that walks a tree MUST additionally check that no directory other
than the root is empty, that `count` and `first` of every reference match
the referenced node, that entries are strictly ascending across all pages of
a directory, and that every paged directory is split exactly as section 3.5
prescribes. Readers do not check NFC, case-fold conflicts, unassigned code
points or the fixed disallowed list (2.5). Together these rules give every directory listing exactly one
valid encoding, and the write-time rules ensure that equivalent names never
coexist.

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
