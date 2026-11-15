#!/usr/bin/env python3
"""Independent reference implementation for the MFGraph test vectors.

This script re-implements the frozen content formats from the written
specification (docs/spec/content-format.md) using only the Python standard
library (hashlib, unicodedata, json). It shares no code with the Go
implementation, so agreement between the two is a cross-check.

Usage:
  python3 scripts/vectors.py verify [--heavy]      # check all vector files
  python3 scripts/vectors.py generate              # recompute expected values in tree.json
  python3 scripts/vectors.py format                # rewrite the vector files as ASCII (\\u escapes)
  python3 scripts/vectors.py review                # regenerate testdata/vectors/REVIEW.md
  python3 scripts/vectors.py materialize CASE DIR  # write a tree case's files into DIR
  python3 scripts/vectors.py crosscheck [--heavy]  # Go CLI + shasum on every tree case

Exit code: 0 = all vectors agree, 1 = mismatch.
"""
import argparse
import hashlib
import json
import os
import sys
import unicodedata

ROOT = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
VECTORS = os.path.join(ROOT, "testdata", "vectors")

FAILURES = []

# V, the Unicode version of the write-time rules (spec section 2.4). Vectors
# whose result depends on it carry "ucd_version"; they are checked only when
# this Python's unicodedata has exactly that version and are counted as
# skipped otherwise.
V = "15.0.0"
UCD_SKIPPED = []


def ucd_skip(case, label):
    """True (and the case is recorded as skipped) if case needs a Unicode
    version other than this Python's."""
    want = case.get("ucd_version")
    if want is None:
        return False
    if want != V:
        check(False, "%s: ucd_version %s is not V = %s" % (label, want, V))
        return True
    if unicodedata.unidata_version != want:
        UCD_SKIPPED.append(label)
        return True
    return False


def check(cond, msg):
    if not cond:
        FAILURES.append(msg)


def load(name):
    with open(os.path.join(VECTORS, name), encoding="utf-8") as f:
        return json.load(f)


# ---------------------------------------------------------------- contenthash

def sha256_counter(seed, length):
    out = bytearray()
    i = 0
    while len(out) < length:
        out += hashlib.sha256(seed.encode("utf-8") + i.to_bytes(4, "big")).digest()
        i += 1
    return bytes(out[:length])


def contenthash_input(inp):
    if "utf8" in inp:
        return inp["utf8"].encode("utf-8")
    if "hex" in inp:
        return bytes.fromhex(inp["hex"])
    if inp.get("generator") == "sha256-counter":
        return sha256_counter(inp["seed"], inp["length"])
    raise ValueError("unknown input %r" % (inp,))


def content_hash(b):
    return "sha256:" + hashlib.sha256(b).hexdigest()


HEX = set("0123456789abcdef")


def parse_hash_ok(s):
    return len(s) == 71 and s.startswith("sha256:") and all(c in HEX for c in s[7:])


def verify_contenthash():
    v = load("contenthash.json")
    for vec in v["vectors"]:
        data = contenthash_input(vec["input"])
        check(len(data) == vec["size"], "contenthash %s: size %d != %d" % (vec["name"], len(data), vec["size"]))
        check(content_hash(data) == vec["hash"], "contenthash %s: hash mismatch" % vec["name"])
    for s in v["parse"]["valid"]:
        check(parse_hash_ok(s), "contenthash parse: %r should be valid" % s)
    for case in v["parse"]["invalid"]:
        check(not parse_hash_ok(case["input"]), "contenthash parse: %r should be invalid" % case["input"])
    return len(v["vectors"])


# ---------------------------------------------------------------------- pathx

MAX_SEGMENT = 255
MAX_PATH = 4096


class PathError(Exception):
    def __init__(self, kind):
        Exception.__init__(self, kind)
        self.kind = kind


def _decode(raw):
    if isinstance(raw, str):
        try:
            raw = raw.encode("utf-8")
        except UnicodeEncodeError:  # a lone surrogate, e.g. from the JSON escape \ud800
            raise PathError("invalid_utf8")
    if len(raw) == 0:
        raise PathError("empty")
    try:
        return raw.decode("utf-8", errors="strict")
    except UnicodeDecodeError:
        raise PathError("invalid_utf8")


# Fixed list (not the whole category Cf), checked at write time only:
# bidirectional formatting characters U+061C, U+200E, U+200F, U+202A-U+202E,
# U+2066-U+2069, and invisible or layout-breaking characters U+00AD, U+180E,
# U+200B, U+2028, U+2029, U+2060, U+2061-U+2064, U+FEFF, U+FFF9-U+FFFB.
# Allowed on purpose: ZWNJ U+200C, ZWJ U+200D, variation selectors
# U+FE00-U+FE0F, tag characters U+E0020-U+E007F, CGJ U+034F.
DISALLOWED = ({0x00AD, 0x061C, 0x180E, 0x200B, 0x200E, 0x200F, 0x2028, 0x2029, 0x2060, 0xFEFF}
              | set(range(0x202A, 0x202F)) | set(range(0x2061, 0x2065))
              | set(range(0x2066, 0x206A)) | set(range(0xFFF9, 0xFFFC)))


def is_noncharacter(o):
    """The 66 noncharacters: U+FDD0-U+FDEF and U+xxFFFE/U+xxFFFF."""
    return 0xFDD0 <= o <= 0xFDEF or (o & 0xFFFE) == 0xFFFE


def char_problem(ch, write):
    """Error kind for one code point, or None. Read time (write=False) checks
    only backslash, control characters and noncharacters; write time also
    rejects the fixed disallowed list and code points of general category Cn
    (unassigned)."""
    o = ord(ch)
    if ch == "\\":
        return "backslash"
    if o < 0x20 or 0x7F <= o <= 0x9F:
        return "control_char"
    if is_noncharacter(o):
        return "noncharacter"
    if write and o in DISALLOWED:
        return "disallowed_char"
    if write and unicodedata.category(ch) == "Cn":
        return "unassigned_char"
    return None


def _check_chars(s, write=True):
    # Left to right: the first offending character decides.
    for ch in s:
        kind = char_problem(ch, write)
        if kind:
            raise PathError(kind)


def _check_segments(n):
    # On the NFC form: empty and dot segments first, then the byte limits.
    segs = n.split("/")
    for seg in segs:
        if seg == "":
            raise PathError("empty_segment")
        if seg in (".", ".."):
            raise PathError("dot_segment")
    for seg in segs:
        if len(seg.encode("utf-8")) > MAX_SEGMENT:
            raise PathError("segment_too_long")
    if len(n.encode("utf-8")) > MAX_PATH:
        raise PathError("path_too_long")


def normalize_path(raw):
    s = _decode(raw)
    if s[0] == "/":
        raise PathError("absolute")
    _check_chars(s)
    n = unicodedata.normalize("NFC", s)
    _check_segments(n)
    return n


def normalize_lookup(raw):
    """Lookup rules (specification 2.6) for a path given to find an existing
    entry: on the input the read-time character rules (backslash, control
    characters, noncharacters) after UTF-8, non-empty and not absolute; then
    NFC; then the segment rules and byte limits on the NFC form. No
    unassigned, disallowed-list or case checks. Returns (nfc, raw): an entry
    is looked up by the input as given first, then by its NFC form."""
    s = _decode(raw)
    if s[0] == "/":
        raise PathError("absolute")
    _check_chars(s, write=False)
    n = unicodedata.normalize("NFC", s)
    _check_segments(n)
    return n, s


def normalize_segment(raw):
    s = _decode(raw)
    if "/" in s:
        raise PathError("contains_slash")
    _check_chars(s)
    n = unicodedata.normalize("NFC", s)
    if n in (".", ".."):
        raise PathError("dot_segment")
    if len(n.encode("utf-8")) > MAX_SEGMENT:
        raise PathError("segment_too_long")
    return n


def check_stored_name(raw):
    """Read-time rules for a name in a stored node: structural only, no Unicode
    tables involved (no NFC, case folding, unassigned or disallowed-list
    check)."""
    s = _decode(raw)
    if "/" in s:
        raise PathError("contains_slash")
    _check_chars(s, write=False)
    if s in (".", ".."):
        raise PathError("dot_segment")
    if len(s.encode("utf-8")) > MAX_SEGMENT:
        raise PathError("segment_too_long")
    return s


def fold_key(name):
    """Canonical caseless match (Unicode 3.13, D145): NFD(casefold(NFD(x))).
    str.casefold is the full case folding of CaseFolding.txt (C+F)."""
    return unicodedata.normalize("NFD", unicodedata.normalize("NFD", name).casefold())


def check_siblings(entries):
    """entries: list of (name, is_dir). Raises PathError on conflict."""
    s = sorted(entries, key=lambda e: e[0].encode("utf-8"))
    for a, b in zip(s, s[1:]):
        if a[0] == b[0]:
            raise PathError("file_dir_conflict" if a[1] != b[1] else "duplicate")
    seen = {}
    for name, _ in s:
        k = fold_key(name)
        if k in seen:
            raise PathError("case_conflict")
        seen[k] = name


def gen_path(gen):
    segs = []
    for part in gen["segments"]:
        segs.extend([part["unit"] * part["repeat"]] * part.get("times", 1))
    return "/".join(segs)


def vector_input(case):
    if "input_hex" in case:
        return bytes.fromhex(case["input_hex"])
    if "input_gen" in case:
        return gen_path(case["input_gen"])
    return case["input"]


def vector_output(case):
    if "output_gen" in case:
        return gen_path(case["output_gen"])
    return case.get("output")


def _run(fn, raw):
    try:
        return fn(raw), None
    except PathError as e:
        return None, e.kind


def verify_pathx():
    v = load("pathx.json")
    n = 0
    for case in v["normalize"]:
        label = "pathx normalize %s" % case["name"]
        if ucd_skip(case, label):
            continue
        got, kind = _run(normalize_path, vector_input(case))
        if "error" in case:
            check(kind == case["error"], "%s: got %r/%r, want error %s" % (label, got, kind, case["error"]))
        else:
            check(kind is None and got == vector_output(case), "%s: got %r/%r" % (label, got, kind))
        n += 1
    for case in v["fold"]:
        check(fold_key(case["input"]) == case["fold"], "pathx fold %r" % case["input"])
        n += 1
    for case in v["siblings"]:
        entries = [(e["name"], e["dir"]) for e in case["entries"]]
        _, kind = _run(check_siblings, entries)
        check(kind == case.get("error"), "pathx siblings %s: got %r" % (case["name"], kind))
        n += 1
    for case in v["branch_names"]:
        label = "pathx branch %r" % (case.get("input", case.get("input_hex", "<gen>")),)
        if ucd_skip(case, label):
            continue
        got, kind = _run(normalize_segment, vector_input(case))
        if "error" in case:
            check(kind == case["error"], "%s: got %r/%r" % (label, got, kind))
        else:
            check(kind is None and got == vector_output(case), "%s: got %r/%r" % (label, got, kind))
        n += 1
    for case in v["stored_names"]:
        _, kind = _run(check_stored_name, vector_input(case))
        check(kind == case.get("error"), "pathx stored name %s: got %r, want %r" % (case["name"], kind, case.get("error")))
        n += 1
    for case in v["lookup"]:
        label = "pathx lookup %s" % case["name"]
        if ucd_skip(case, label):
            continue
        raw = vector_input(case)
        got, kind = _run(normalize_lookup, raw)
        if "error" in case:
            check(kind == case["error"], "%s: got %r/%r, want error %s" % (label, got, kind, case["error"]))
        else:
            check(kind is None and got == (vector_output(case), raw), "%s: got %r/%r" % (label, got, kind))
        n += 1
    return n


# ----------------------------------------------------------------------- tree
#
# Written from the format specification, not from the Go code:
#   * nodes are RFC 8785 (JCS) JSON: keys sorted, no whitespace, strings escape
#     only '"', '\\' and U+0000-U+001F (\b \t \n \f \r short forms, others
#     \u00xx lowercase), everything else raw UTF-8; integers in plain decimal;
#   * "entries" sorted by the UTF-8 bytes of "name";
#   * a directory is one "dir" node if it has <= 1024 entries and encodes to
#     <= 1 MiB; otherwise entries are packed greedily into "dir_page" nodes
#     (a page is closed when one more entry would exceed 1024 entries or
#     1 MiB), pages are referenced by a "dir_index" node, and while there are
#     more than 1024 references (or the index would exceed 1 MiB) the
#     references are packed into "dir_index" nodes by the same greedy rule;
#   * a reference is {"count","first","hash"}: count = number of directory
#     entries under it, first = name of its first entry;
#   * node hash = sha256 of the uncompressed canonical bytes.

MAX_ENTRIES = 1024
MAX_NODE_BYTES = 1 << 20
MAX_SAFE_INTEGER = 2 ** 53 - 1
SHORT_ESCAPES = {0x08: "\\b", 0x09: "\\t", 0x0A: "\\n", 0x0C: "\\f", 0x0D: "\\r"}


def jcs_string(s):
    out = ['"']
    for ch in s:
        o = ord(ch)
        if ch == '"':
            out.append('\\"')
        elif ch == "\\":
            out.append("\\\\")
        elif o < 0x20:
            out.append(SHORT_ESCAPES.get(o, "\\u%04x" % o))
        else:
            out.append(ch)
    out.append('"')
    return "".join(out)


def jcs(obj):
    """Minimal JCS serializer for the value types used by tree nodes."""
    if isinstance(obj, dict):
        # JCS sorts keys by UTF-16 code units; all keys here are ASCII.
        return "{" + ",".join(jcs_string(k) + ":" + jcs(obj[k]) for k in sorted(obj)) + "}"
    if isinstance(obj, list):
        return "[" + ",".join(jcs(x) for x in obj) + "]"
    if isinstance(obj, bool) or obj is None:
        raise TypeError("unexpected value in a tree node")
    if isinstance(obj, int):
        if obj < 0 or obj > MAX_SAFE_INTEGER:
            raise ValueError("integer out of range")
        return str(obj)
    if isinstance(obj, str):
        return jcs_string(obj)
    raise TypeError(type(obj))


def canonical(obj):
    text = jcs(obj)
    # Second, independent serializer: the stdlib json module produces the same
    # bytes for this restricted value set.
    other = json.dumps(obj, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
    if text != other:
        raise AssertionError("jcs/json.dumps disagree:\n%s\n%s" % (text, other))
    return text.encode("utf-8")


def entry_obj(e):
    if e["type"] == "file":
        return {"hash": e["hash"], "mode": e["mode"], "name": e["name"], "size": e["size"], "type": "file"}
    return {"hash": e["hash"], "name": e["name"], "type": "dir"}


def node_obj(kind, items):
    if kind == "dir_index":
        return {"format": 1, "kind": kind, "pages": items}
    return {"entries": items, "format": 1, "kind": kind}


def greedy(sizes, overhead, max_items, max_bytes):
    """Split item sizes into [start, end) groups by the greedy rule."""
    groups, start, size = [], 0, overhead
    for i, n in enumerate(sizes):
        add = n + (1 if i > start else 0)
        if i > start and (i - start + 1 > max_items or size + add > max_bytes):
            groups.append((start, i))
            start, size, add = i, overhead, n
        if size + add > max_bytes:
            raise ValueError("single item exceeds node limit")
        size += add
    groups.append((start, len(sizes)))
    return groups


def node_size(overhead, sizes):
    return overhead + sum(sizes) + max(0, len(sizes) - 1)


def layout(entries, store, max_items=MAX_ENTRIES, max_bytes=MAX_NODE_BYTES):
    """Store the nodes of one directory; return the directory's reference hash."""
    def put(obj):
        b = canonical(obj)
        h = content_hash(b)
        store[h] = b
        return h

    objs = [entry_obj(e) for e in entries]
    sizes = [len(canonical(o)) for o in objs]
    if len(objs) <= max_items and node_size(len(canonical(node_obj("dir", []))), sizes) <= max_bytes:
        return put(node_obj("dir", objs))
    refs = []
    for a, b in greedy(sizes, len(canonical(node_obj("dir_page", []))), max_items, max_bytes):
        h = put(node_obj("dir_page", objs[a:b]))
        refs.append({"count": b - a, "first": entries[a]["name"], "hash": h})
    index_overhead = len(canonical(node_obj("dir_index", [])))
    while True:
        rsizes = [len(canonical(r)) for r in refs]
        if len(refs) <= max_items and node_size(index_overhead, rsizes) <= max_bytes:
            return put(node_obj("dir_index", refs))
        nxt = []
        for a, b in greedy(rsizes, index_overhead, max_items, max_bytes):
            h = put(node_obj("dir_index", refs[a:b]))
            nxt.append({"count": sum(r["count"] for r in refs[a:b]), "first": refs[a]["first"], "hash": h})
        refs = nxt


def utf8key(s):
    return s.encode("utf-8")


def build_tree(files, max_items=MAX_ENTRIES, max_bytes=MAX_NODE_BYTES):
    """files: list of dicts {path, hash, size, mode}. Returns (root, store).
    Raises PathError for invalid or conflicting paths."""
    norm = sorted(((normalize_path(f["path"]), f) for f in files), key=lambda t: utf8key(t[0]))
    root = {}
    for path, f in norm:
        segs = path.split("/")
        d = root
        for s in segs[:-1]:
            if s in d and isinstance(d[s], tuple):
                raise PathError("file_dir_conflict")
            d = d.setdefault(s, {})
        last = segs[-1]
        if last in d:
            raise PathError("file_dir_conflict" if isinstance(d[last], dict) else "duplicate")
        if f["mode"] not in (420, 493) or not (0 <= f["size"] <= MAX_SAFE_INTEGER):
            raise ValueError("invalid file entry")
        d[last] = ("file", f["hash"], f["size"], f["mode"])  # files are tuples, dirs are dicts

    def check(d):
        check_siblings([(name, isinstance(v, dict)) for name, v in d.items()])
        for v in d.values():
            if isinstance(v, dict):
                check(v)

    check(root)
    store = {}

    def encode(d):
        entries = []
        for name in sorted(d, key=utf8key):
            v = d[name]
            if isinstance(v, tuple):
                entries.append({"type": "file", "name": name, "hash": v[1], "size": v[2], "mode": v[3]})
            else:
                entries.append({"type": "dir", "name": name, "hash": encode(v)})
        return layout(entries, store, max_items, max_bytes)

    return encode(root), store


# A strict decoder, used to (a) list nodes in walk order and (b) check that the
# decode_invalid vectors are rejected.

class DecodeError(Exception):
    pass


def _pairs(pairs):
    keys = [k for k, _ in pairs]
    if len(set(keys)) != len(keys):
        raise DecodeError("duplicate key")
    return dict(pairs)


def _int(v, lo):
    if not isinstance(v, int) or isinstance(v, bool) or v < lo or v > MAX_SAFE_INTEGER:
        raise DecodeError("bad integer")
    return v


def _name(v):
    if not isinstance(v, str):
        raise DecodeError("name not a string")
    try:
        check_stored_name(v)
    except PathError as e:
        raise DecodeError("bad name: " + e.kind)
    return v


def decode_node(b):
    if len(b) > MAX_NODE_BYTES:
        raise DecodeError("node too large")
    try:
        text = b.decode("utf-8", errors="strict")
        obj = json.loads(text, object_pairs_hook=_pairs, parse_float=lambda s: float("nan"),
                         parse_constant=lambda s: None)
    except (UnicodeDecodeError, ValueError) as e:
        raise DecodeError("not JSON: %s" % e)
    if not isinstance(obj, dict):
        raise DecodeError("not an object")
    kind = obj.get("kind")
    if obj.get("format") != 1 or isinstance(obj.get("format"), bool):
        raise DecodeError("bad format")
    if kind in ("dir", "dir_page"):
        if set(obj) != {"entries", "format", "kind"} or not isinstance(obj["entries"], list):
            raise DecodeError("bad keys")
        ents = obj["entries"]
        if len(ents) > MAX_ENTRIES or (kind == "dir_page" and not ents):
            raise DecodeError("bad entry count")
        for e in ents:
            if not isinstance(e, dict):
                raise DecodeError("entry not an object")
            if e.get("type") == "file":
                if set(e) != {"hash", "mode", "name", "size", "type"} or e["mode"] not in (420, 493) \
                        or isinstance(e["mode"], bool):
                    raise DecodeError("bad file entry")
                _int(e["size"], 0)
            elif e.get("type") == "dir":
                if set(e) != {"hash", "name", "type"}:
                    raise DecodeError("bad dir entry")
            else:
                raise DecodeError("bad entry type")
            _name(e["name"])
            if not (isinstance(e["hash"], str) and parse_hash_ok(e["hash"])):
                raise DecodeError("bad hash")
        names = [e["name"] for e in ents]
        if any(utf8key(a) >= utf8key(b) for a, b in zip(names, names[1:])):
            raise DecodeError("entries not strictly sorted")
        # Read time: no NFC or case-fold checks (write-time rules only).
    elif kind == "dir_index":
        if set(obj) != {"format", "kind", "pages"} or not isinstance(obj["pages"], list):
            raise DecodeError("bad keys")
        pages = obj["pages"]
        if not pages or len(pages) > MAX_ENTRIES:
            raise DecodeError("bad page count")
        for p in pages:
            if not isinstance(p, dict) or set(p) != {"count", "first", "hash"}:
                raise DecodeError("bad page ref")
            _int(p["count"], 1)
            _name(p["first"])
            if not (isinstance(p["hash"], str) and parse_hash_ok(p["hash"])):
                raise DecodeError("bad hash")
        firsts = [p["first"] for p in pages]
        if any(utf8key(a) >= utf8key(b) for a, b in zip(firsts, firsts[1:])):
            raise DecodeError("pages not strictly sorted")
    else:
        raise DecodeError("bad kind")
    if canonical(obj) != b:
        raise DecodeError("not canonical")
    return obj


EMPTY_HASH = "sha256:" + hashlib.sha256(b"").hexdigest()


def gen_names(g):
    return [g["prefix"] + str(i).zfill(g["digits"]) for i in range(g["start"], g["start"] + g["count"])]


def gen_node(g):
    """Expand a node generator (testdata/vectors/README.md): a node of kind
    g["kind"] with g["count"] items named prefix + zero-padded index (digits
    wide) for index in [start, start+count): file entries (mode 420, size 0)
    for dir/dir_page, references with count 1 for dir_index, every hash the
    hash of the empty content. Returns the canonical bytes; the names are not
    checked, so a generator can describe an invalid node."""
    names = gen_names(g)
    if g["kind"] == "dir_index":
        items = [{"count": 1, "first": n, "hash": EMPTY_HASH} for n in names]
    else:
        items = [{"hash": EMPTY_HASH, "mode": 420, "name": n, "size": 0, "type": "file"} for n in names]
    return canonical(node_obj(g["kind"], items))


def node_case_bytes(case, label):
    """The node bytes of a decode_* case: bytes_hex, or node_gen whose
    expansion must hash to bytes_sha256."""
    if "bytes_hex" in case:
        return bytes.fromhex(case["bytes_hex"])
    b = gen_node(case["node_gen"])
    check(content_hash(b) == case["bytes_sha256"], "%s: generated node hashes to %s" % (label, content_hash(b)))
    return b


class TreeInvalid(Exception):
    """The nodes decode, but the tree violates a whole-tree rule (spec 3.6,
    3.8 second paragraph): vector category invalid_tree. A DecodeError is
    category invalid_node."""


class MissingNode(Exception):
    pass


def check_tree(store, root):
    """Verify a whole tree from its root, written from the specification:
    every node decodes strictly (3.8); a directory's top node is a dir or a
    dir_index, never a dir_page; only the root may be empty; an index
    references only dir_page or dir_index nodes; each reference's count and
    first match the leaves below it; entries are strictly ascending across all
    pages; and a paged directory is split exactly as 3.5 prescribes."""
    def fetch(h):
        if h not in store:
            raise MissingNode(h)
        b = store[h]
        if content_hash(b) != h:
            raise TreeInvalid("node bytes do not hash to the reference")
        return decode_node(b)

    def leaves(index):
        out = []
        for p in index["pages"]:
            child = fetch(p["hash"])
            if child["kind"] == "dir_page":
                sub = child["entries"]
            elif child["kind"] == "dir_index":
                sub = leaves(child)
            else:
                raise TreeInvalid("an index references a dir node")
            if len(sub) != p["count"] or sub[0]["name"] != p["first"]:
                raise TreeInvalid("reference count/first does not match the referenced node")
            out += sub
        return out

    def directory(h, is_root):
        top = fetch(h)
        if top["kind"] == "dir_page":
            raise TreeInvalid("a directory points to a dir_page")
        if top["kind"] == "dir":
            ents = top["entries"]
            if not ents and not is_root:
                raise TreeInvalid("empty directory other than the root")
        else:
            ents = leaves(top)
            names = [utf8key(e["name"]) for e in ents]
            if any(a >= b for a, b in zip(names, names[1:])):
                raise TreeInvalid("entries not strictly ascending across pages")
            if layout(ents, {}) != h:
                raise TreeInvalid("directory not split canonically")
        for e in ents:
            if e["type"] == "dir":
                directory(e["hash"], False)

    directory(root, True)


def walk_store(case, label):
    """The node store of a walk_* case; every listed hash must match."""
    store = {}
    for n in case["nodes"]:
        b = bytes.fromhex(n["hex"]) if "hex" in n else gen_node(n["node_gen"])
        h = content_hash(b)
        check(h == n["hash"], "%s: a node hashes to %s, listed as %s" % (label, h, n["hash"]))
        store[h] = b
    return store


def walk_category(store, root):
    try:
        check_tree(store, root)
    except DecodeError:
        return "invalid_node"
    except TreeInvalid:
        return "invalid_tree"
    return None


def dir_entries(store, h):
    obj = decode_node(store[h])
    if obj["kind"] != "dir_index":
        return obj["entries"]
    out = []

    def expand(index):
        for p in index["pages"]:
            child = decode_node(store[p["hash"]])
            if child["kind"] == "dir_index":
                expand(child)
            else:
                out.extend(child["entries"])
    expand(obj)
    return out


def walk_paths(store, root):
    """Full paths in walk order: pre-order, entries of a directory in byte
    order of their names (the path order of spec 3.3)."""
    out = []

    def visit(prefix, h):
        for e in dir_entries(store, h):
            out.append(prefix + e["name"])
            if e["type"] == "dir":
                visit(prefix + e["name"] + "/", e["hash"])
    visit("", root)
    return out


def walk_nodes(store, root):
    """Yield (dir, kind, hash, bytes) in pre-order: a directory's own nodes
    (index first, then its pages in order), then its subdirectories by name."""
    out = []

    def fetch(h):
        b = store[h]
        if content_hash(b) != h:
            raise DecodeError("hash mismatch")
        return decode_node(b), b

    def expand(path, h, top):
        obj, b = fetch(h)
        if top and obj["kind"] == "dir_page":
            raise DecodeError("directory points to a dir_page")
        if not top and obj["kind"] == "dir":
            raise DecodeError("index points to a dir node")
        out.append((path, obj["kind"], h, b))
        if obj["kind"] == "dir_index":
            ents = []
            for p in obj["pages"]:
                sub = expand(path, p["hash"], False)
                if len(sub) != p["count"] or sub[0]["name"] != p["first"]:
                    raise DecodeError("page ref count/first mismatch")
                ents += sub
            return ents
        return obj["entries"]

    def visit(path, h, is_root):
        ents = expand(path, h, True)
        if not ents and not is_root:
            raise DecodeError("empty non-root directory")
        for e in ents:
            if e["type"] == "dir":
                visit(e["name"] if not path else path + "/" + e["name"], e["hash"], False)

    visit("", root, True)
    return out


def tree_files(case):
    files = []
    for f in case.get("files", []):
        content = f["content"].encode("utf-8")
        files.append({"path": f["path"], "hash": content_hash(content), "size": len(content),
                      "mode": f.get("mode", 420)})
    for g in case.get("generate", []):
        prefix = g["prefix"] * g.get("prefix_repeat", 1)
        for i in range(g["start"], g["start"] + g["count"]):
            name = prefix + str(i).zfill(g["digits"])
            path = g["dir"] + "/" + name if g.get("dir") else name
            content = path.encode("utf-8")
            files.append({"path": path, "hash": content_hash(content), "size": len(content), "mode": 420})
    return files


def node_names(obj):
    if obj["kind"] == "dir_index":
        return [p["first"] for p in obj["pages"]]
    return [e["name"] for e in obj["entries"]]


def write_rule_violated(obj, rule):
    """True if the write path would never produce this (decodable) node."""
    names = node_names(obj)
    if rule == "nfc":
        return any(_run(normalize_segment, n)[1] is None and normalize_segment(n) != n for n in names)
    if rule == "case_conflict":
        entries = [(e["name"], e["type"] == "dir") for e in obj.get("entries", [])]
        return _run(check_siblings, entries)[1] == "case_conflict"
    if rule in ("unassigned_char", "disallowed_char"):
        return any(_run(normalize_segment, n)[1] == rule for n in names)
    raise ValueError(rule)


CANONICAL_INLINE_LIMIT = 4096  # list canonical bytes only for nodes up to this size


def expected_tree(case):
    """Compute the expected section of a tree case."""
    try:
        root, store = build_tree(tree_files(case))
    except PathError as e:
        return {"error": e.kind}
    listing = walk_nodes(store, root)
    nodes = []
    for path, kind, h, b in listing:
        n = {"dir": path, "kind": kind, "hash": h, "size": len(b)}
        if len(b) <= CANONICAL_INLINE_LIMIT:
            n["canonical"] = b.decode("utf-8")
        nodes.append(n)
    exp = {"root": root, "node_count": len(store)}
    if "walk_order" in case:
        exp["walk_order"] = walk_paths(store, root)
    if "list_nodes" in case:
        # Large cases list only selected positions of the walk order.
        exp["walk_length"] = len(nodes)
        nodes = [dict(nodes[i], position=i % len(nodes)) for i in case["list_nodes"]]
    exp["nodes"] = nodes
    return exp


EXPECTED_KEYS = ("root", "node_count", "walk_length", "walk_order", "nodes", "error")


def verify_tree(heavy, update=False):
    path = os.path.join(VECTORS, "tree.json")
    v = load("tree.json")
    n = 0
    for case in v["cases"]:
        if case.get("heavy") and not heavy:
            continue
        if ucd_skip(case, "tree %s" % case["name"]):
            continue  # with update, the stored expected values are kept
        exp = expected_tree(case)
        if update:
            for k in EXPECTED_KEYS:
                case.pop(k, None)
            case.update(exp)
        else:
            for k in EXPECTED_KEYS:
                check(case.get(k) == exp.get(k), "tree %s: %s differs" % (case["name"], k))
        n += 1
    for case in v["decode_invalid"]:
        label = "tree decode_invalid %s" % case["name"]
        try:
            decode_node(node_case_bytes(case, label))
            check(False, "%s: accepted" % label)
        except DecodeError:
            pass
        if "node_gen" in case and case["node_gen"]["count"] > MAX_ENTRIES:
            # Control: one item fewer is a valid node, so the count is the reason.
            ok = dict(case["node_gen"], count=MAX_ENTRIES)
            try:
                decode_node(gen_node(ok))
            except DecodeError as e:
                check(False, "%s: the %d-item control node is rejected: %s" % (label, MAX_ENTRIES, e))
        n += 1
    for case in v["decode_valid_build_rejects"]:
        label = "tree decode_valid_build_rejects %s" % case["name"]
        if ucd_skip(case, label):
            continue
        try:
            obj = decode_node(node_case_bytes(case, label))
        except DecodeError as e:
            check(False, "%s: rejected by the decoder: %s" % (label, e))
            n += 1
            continue
        check(write_rule_violated(obj, case["write_rule"]), "%s: write rule %s not violated" % (label, case["write_rule"]))
        n += 1
    for section, valid in (("walk_valid", True), ("walk_invalid", False)):
        for case in v[section]:
            label = "tree %s %s" % (section, case["name"])
            store = walk_store(case, label)
            try:
                got = walk_category(store, case["root"])
            except MissingNode as e:
                check(False, "%s: references a node that is not listed: %s" % (label, e))
                continue
            check(got == case.get("error"), "%s: got %r, want %r" % (label, got, case.get("error")))
            check(valid == ("error" not in case), "%s: misplaced case" % label)
            n += 1
    if update:
        with open(path, "w", encoding="ascii", newline="\n") as f:
            f.write(dump_vectors(v))
        print("updated", os.path.relpath(path, ROOT))
    return n


def needs_escape(ch):
    """Characters that REVIEW.md shows as code points (display only)."""
    cat = unicodedata.category(ch)
    invisible = cat[0] in "MC" or cat in ("Zl", "Zp") or (cat == "Zs" and ch != " ")
    lookalike = unicodedata.normalize("NFKC", ch) != ch or 0x1100 <= ord(ch) <= 0x11FF
    return (invisible or lookalike) and ord(ch) >= 0x80


def json_ascii(obj, **kw):
    """json.dumps with every non-ASCII character (and DEL) written as a \\u
    escape. Vector files are pure ASCII, so no editor can silently normalize
    an NFD input or rewrite a combining mark, a compatibility character or an
    invisible character; the decoded values are unchanged. The rule needs no
    Unicode tables, so every Python version writes the same bytes."""
    return json.dumps(obj, ensure_ascii=True, **kw).replace("\x7f", "\\u007f")


def dump_vectors(v):
    """Pretty-print tree.json (indent 1, ASCII only)."""
    return json_ascii(v, indent=1) + "\n"


def dump_lines(v):
    """Format pathx.json and contenthash.json (ASCII only): one top-level key
    per line; list items one per line as compact JSON; one level of nested
    objects."""
    out = ["{"]
    keys = list(v)
    for i, k in enumerate(keys):
        val = v[k]
        comma = "," if i < len(keys) - 1 else ""
        if isinstance(val, list):
            out.append("  %s: [" % json_ascii(k))
            out += ["    " + json_ascii(x) + ("," if n < len(val) - 1 else "") for n, x in enumerate(val)]
            out.append("  ]" + comma)
        elif isinstance(val, dict):
            out.append("  %s: {" % json_ascii(k))
            sub = list(val)
            for n, sk in enumerate(sub):
                sv = val[sk]
                c2 = "," if n < len(sub) - 1 else ""
                if isinstance(sv, list):
                    out.append("    %s: [" % json_ascii(sk))
                    out += ["      " + json_ascii(x) + ("," if m < len(sv) - 1 else "") for m, x in enumerate(sv)]
                    out.append("    ]" + c2)
                else:
                    out.append("    %s: %s%s" % (json_ascii(sk), json_ascii(sv), c2))
            out.append("  }" + comma)
        else:
            out.append("  %s: %s%s" % (json_ascii(k), json_ascii(val), comma))
    out.append("}")
    return "\n".join(out) + "\n"


VECTOR_FILES = {"contenthash.json": dump_lines, "pathx.json": dump_lines, "tree.json": dump_vectors}


def format_vectors():
    """Rewrite every vector file in its canonical ASCII-only layout."""
    for name, dump in VECTOR_FILES.items():
        path = os.path.join(VECTORS, name)
        v = load(name)
        text = dump(v)
        if json.loads(text) != v:
            raise AssertionError("formatting changed the values of " + name)
        with open(path, "w", encoding="ascii", newline="\n") as f:
            f.write(text)
        print("formatted", os.path.relpath(path, ROOT))


def verify_review_fields():
    """The review fields of the vector files: a known status, the same in all
    files; "reviewed" comes with reviewed_by and a reviewed_on date
    (YYYY-MM-DD); "pending-human-review" with neither."""
    import datetime
    for name in VECTOR_FILES:
        v = load(name)
        status, by, on = v.get("review_status"), v.get("reviewed_by"), v.get("reviewed_on")
        check(status in REVIEW_STATUSES, "%s: review_status %r" % (name, status))
        if status == "reviewed":
            check(isinstance(by, str) and by.strip() != "", "%s: reviewed without reviewed_by" % name)
            try:
                ok = datetime.date.fromisoformat(on).isoformat() == on
            except (TypeError, ValueError):
                ok = False
            check(ok, "%s: reviewed_on %r is not a YYYY-MM-DD date" % (name, on))
        else:
            check(by is None and on is None, "%s: reviewed_by/reviewed_on without the reviewed status" % name)
    check(review_state() is not None, "the vector files disagree about their review fields")
    return len(VECTOR_FILES)


def verify_ascii():
    """Every vector file must be pure ASCII (non-ASCII characters escaped)."""
    for name in VECTOR_FILES:
        with open(os.path.join(VECTORS, name), "rb") as f:
            raw = f.read()
        bad = [i for i, b in enumerate(raw) if b >= 0x80 or b == 0x7F]
        check(not bad, "%s: raw non-ASCII byte at offset %s; run: python3 scripts/vectors.py format"
              % (name, bad[:1]))
    return len(VECTOR_FILES)


# --------------------------------------------------------------------- review

def show(s):
    """Render a string for a Markdown table: marks, controls, format and
    non-ASCII spaces are written as \\uXXXX so that NFC/NFD differences and
    invisible characters are visible."""
    if isinstance(s, bytes):
        return "bytes `" + s.hex() + "`"
    out = []
    for ch in s:
        cat = unicodedata.category(ch)
        invisible = cat[0] in "MC" or cat in ("Zl", "Zp") or (cat == "Zs" and ch != " ")
        # Compatibility/canonical singletons (e.g. U+212B, U+FB01, U+FF0F) and
        # conjoining jamo look like other characters: show their code points.
        lookalike = unicodedata.normalize("NFKC", ch) != ch or 0x1100 <= ord(ch) <= 0x11FF
        if invisible or lookalike:
            out.append("\\u%04x" % ord(ch) if ord(ch) <= 0xFFFF else "\\U%08x" % ord(ch))
        elif ch == "|":
            out.append("\\|")
        elif ch == "`":
            out.append("\\`")
        else:
            out.append(ch)
    return "`" + "".join(out) + "`" if out else "(empty string)"


def show_gen(gen):
    parts = []
    for p in gen["segments"]:
        seg = "%s×%d" % (show(p["unit"]) if p["unit"] else "(empty)", p["repeat"])
        if p.get("times", 1) != 1:
            seg = "(%s)×%d segments" % (seg, p["times"])
        parts.append(seg)
    return " / ".join(parts)


def show_case_input(case):
    if "input_gen" in case:
        raw = gen_path(case["input_gen"])
        return "%s (%d bytes)" % (show_gen(case["input_gen"]), len(raw.encode("utf-8")))
    if "input_hex" in case:
        return "bytes `%s`" % case["input_hex"]
    return show(case["input"])


def ucd_tag(case):
    tag = " (Unicode %s only)" % case["ucd_version"] if "ucd_version" in case else ""
    if case.get("multiple_violations"):
        tag += " (several violations: reference order, not normative)"
    return tag


def show_case_result(case):
    if "error" in case:
        return "✗ `%s`%s" % (case["error"], ucd_tag(case))
    if "output_gen" in case:
        out = gen_path(case["output_gen"])
        return "✓ %s (%d bytes)%s" % (show_gen(case["output_gen"]), len(out.encode("utf-8")), ucd_tag(case))
    return "✓ " + show(case["output"]) + ucd_tag(case)


def show_node_bytes(b):
    """Render node bytes: the text with invisible characters as code points,
    or hex when they are not valid UTF-8."""
    try:
        text = b.decode("utf-8")
    except UnicodeDecodeError:
        return "bytes `%s` (not UTF-8)" % b.hex()
    if len(b) > 400:
        return "%d bytes" % len(b)
    return show(text)


def show_node_gen(g):
    return "generated `%s` with %d %s `%s`…`%s`" % (
        g["kind"], g["count"], "references" if g["kind"] == "dir_index" else "file entries",
        g["prefix"] + str(g["start"]).zfill(g["digits"]), g["prefix"] + str(g["start"] + g["count"] - 1).zfill(g["digits"]))


def review_contenthash(lines):
    v = load("contenthash.json")
    lines += ["## 1. contenthash", "",
              "Rule: `sha256:` + 64 lowercase hex of SHA-256 over the exact bytes. No normalization of content.", "",
              "| name | input | size | expected hash |", "|---|---|---:|---|"]
    for vec in v["vectors"]:
        inp = vec["input"]
        if "utf8" in inp:
            desc = "utf8 " + show(inp["utf8"])
        elif "hex" in inp:
            desc = "hex bytes 00..ff" if len(inp["hex"]) == 512 else "hex `%s`" % inp["hex"]
        else:
            desc = "generator `%s` seed `%s` length %d" % (inp["generator"], inp["seed"], inp["length"])
        lines.append("| %s | %s | %d | `%s` |" % (vec["name"], desc, vec["size"], vec["hash"]))
    lines += ["", "Parse rejects (must all fail): " + ", ".join(
        "%s" % c["reason"] for c in v["parse"]["invalid"]) + ".", ""]


def review_pathx(lines):
    v = load("pathx.json")
    lines += ["## 2. pathx", "",
              "Rule order: UTF-8 + non-empty → absolute → first offending character from left to right "
              "(backslash; control_char = U+0000–U+001F, U+007F–U+009F; noncharacter = U+FDD0–U+FDEF and "
              "U+xxFFFE/U+xxFFFF (66 code points); disallowed_char = the fixed list: bidi "
              "controls U+061C, U+200E, U+200F, U+202A–U+202E, U+2066–U+2069 and invisible characters U+00AD, "
              "U+180E, U+200B, U+2028, U+2029, U+2060, U+2061–U+2064, U+FEFF, U+FFF9–U+FFFB; "
              "unassigned_char = general category Cn in Unicode 15.0.0) → NFC → "
              "empty / `.` / `..` segments → segment ≤ 255 B and path ≤ 4096 B (NFC bytes). First failure wins.", "",
              "### 2.1 Normalize", "", "| name | input | result |", "|---|---|---|"]
    for case in v["normalize"]:
        lines.append("| %s | %s | %s |" % (case["name"], show_case_input(case), show_case_result(case)))
    lines += ["", "### 2.2 FoldKey: NFD(casefold(NFD(name))), full case folding (C+F)", "",
              "| input | fold key |", "|---|---|"]
    for case in v["fold"]:
        lines.append("| %s | %s |" % (show(case["input"]), show(case["fold"])))
    lines += ["", "### 2.3 Sibling conflicts", "", "| name | entries | result |", "|---|---|---|"]
    for case in v["siblings"]:
        ents = ", ".join("%s%s" % (show(e["name"]), "/" if e["dir"] else "") for e in case["entries"])
        res = "✗ `%s`" % case["error"] if "error" in case else "✓ ok"
        lines.append("| %s | %s | %s |" % (case["name"], ents, res))
    lines += ["", "(a trailing `/` marks a directory)", "", "### 2.4 Branch names (single-segment rules, no `/`)", "",
              "| input | result |", "|---|---|"]
    for case in v["branch_names"]:
        lines.append("| %s | %s |" % (show_case_input(case), show_case_result(case)))
    lines += ["", "### 2.5 Stored names (read-time rules)", "",
              "Names inside stored tree nodes are checked only with structural rules that do not depend on "
              "Unicode tables: valid UTF-8, non-empty, no `/`, then the first offending character (backslash, "
              "control_char, noncharacter), not `.`/`..`, at most 255 bytes as stored. No NFC, case-fold, "
              "unassigned or disallowed-list checks: those are write-time rules, so stored trees stay readable "
              "when they change.", "",
              "| name | input | result |", "|---|---|---|"]
    for case in v["stored_names"]:
        res = "✗ `%s`" % case["error"] if "error" in case else "✓ accepted"
        lines.append("| %s | %s | %s |" % (case["name"], show_case_input(case), res))
    lines += ["", "### 2.6 Lookups (a path that finds an existing entry)", "",
              "Listing, download, diff and restore paths, and the path of a commit change that replaces or "
              "deletes a file (specification 2.6): on the input UTF-8 + non-empty → absolute → first offending "
              "character (backslash, control_char, noncharacter: the read-time character rules) → NFC → on the "
              "NFC form empty / `.` / `..` segments → segment ≤ 255 B and path ≤ 4096 B. No unassigned, "
              "disallowed-list or case checks. The result is the NFC form; an entry is looked up by the input as "
              "given first and by the NFC form second.", "",
              "| name | input | result |", "|---|---|---|"]
    for case in v["lookup"]:
        lines.append("| %s | %s | %s |" % (case["name"], show_case_input(case), show_case_result(case)))
    lines.append("")


def review_tree(lines):
    v = load("tree.json")
    lines += ["## 3. tree (format 1)", "",
              "Rules checked by these vectors:", "",
              "* node = RFC 8785 (JCS) JSON, keys sorted, no whitespace; strings escape only `\"`, `\\` and "
              "U+0000–U+001F; everything else is raw UTF-8; node hash = SHA-256 of the uncompressed bytes;",
              "* `entries` sorted by UTF-8 bytes of `name`; file entry `{hash,mode,name,size,type}`, "
              "directory entry `{hash,name,type}`; mode is 420 or 493; no mtime;",
              "* a directory is one `dir` node when it has ≤ 1024 entries and encodes to ≤ 1 MiB; otherwise "
              "greedy `dir_page` nodes + `dir_index` (levels added while > 1024 references); a reference is "
              "`{count,first,hash}` where count = number of directory entries below it;",
              "* only the root may be empty; directories are implicit.", "",
              "### 3.1 Cases", "",
              "| case | input | expected |", "|---|---|---|"]
    for c in v["cases"]:
        parts = []
        if c.get("files"):
            parts.append("%d file(s)" % len(c["files"]))
        for g in c.get("generate", []):
            pre = g["prefix"] * g.get("prefix_repeat", 1)
            pre_s = show(g["prefix"]) + ("×%d" % g["prefix_repeat"] if g.get("prefix_repeat", 1) != 1 else "")
            parts.append("generated %s%s + %d-digit index, %d…%d" % (
                (show(g["dir"]) + "/") if g.get("dir") else "", pre_s, g["digits"], g["start"], g["start"] + g["count"] - 1))
        if "error" in c:
            exp = "✗ `%s`" % c["error"]
        else:
            exp = "root `%s…`, %d node(s)" % (c["root"][:19], c["node_count"])
            if c.get("heavy"):
                exp += " (heavy, opt-in)"
        exp += ucd_tag(c)
        lines.append("| %s | %s | %s |" % (c["name"], "; ".join(parts) or "no files", exp))
    lines += ["", "### 3.2 Small cases in full", "",
              "Inputs and every node (directory, kind, hash, canonical bytes exactly as hashed)."
              " File hashes are SHA-256 of the given content.", ""]
    for c in v["cases"]:
        if "error" in c or c.get("generate"):
            continue
        lines.append("#### `%s`" % c["name"])
        lines.append("")
        if c["files"]:
            lines += ["| path | content | mode |", "|---|---|---:|"]
            for f in c["files"]:
                lines.append("| %s | %s | %d |" % (show(f["path"]), show(f["content"]), f.get("mode", 420)))
        else:
            lines.append("(no files)")
        lines += ["", "root: `%s`%s" % (c["root"], ucd_tag(c)), ""]
        if "walk_order" in c:
            lines += ["walk order (path order): " + ", ".join(show(p) for p in c["walk_order"]) +
                      " — sorting the full paths by bytes would give " +
                      ", ".join(show(p) for p in sorted(c["walk_order"], key=utf8key)) + ".", ""]
        for n in c["nodes"]:
            lines.append("* dir %s · `%s` · `%s` · %d bytes" % (show(n["dir"]) if n["dir"] else "`.` (root)",
                                                               n["kind"], n["hash"], n["size"]))
            lines += ["", "  ```json", "  " + n["canonical"], "  ```", ""]
            if any(needs_escape(ch) for ch in n["canonical"]):
                lines += ["  The same bytes with invisible characters shown as code points: " +
                          show(n["canonical"]), ""]
    lines += ["### 3.3 Large cases (root and selected nodes)", ""]
    for c in v["cases"]:
        if "error" in c or not c.get("generate"):
            continue
        lines += ["#### `%s`" % c["name"], "",
                  "root `%s`; %d distinct node(s)%s." % (c["root"], c["node_count"],
                   "; walk visits %d" % c["walk_length"] if "walk_length" in c else ""), ""]
        lines += ["| position | dir | kind | bytes | hash |", "|---:|---|---|---:|---|"]
        for i, n in enumerate(c["nodes"]):
            lines.append("| %d | %s | %s | %d | `%s` |" % (n.get("position", i), show(n["dir"]) if n["dir"] else ".",
                                                         n["kind"], n["size"], n["hash"]))
        small = [n for n in c["nodes"] if "canonical" in n]
        for n in small:
            lines += ["", "```json", n["canonical"], "```"]
        lines.append("")
    lines += ["### 3.4 Strict decoding: inputs that must be rejected (read-time rules)", "",
              "The vector file gives the exact bytes in hex (`bytes_hex`); large nodes are given by the node "
              "generator (`node_gen`, checked with `bytes_sha256`). For each generated node with more than 1024 "
              "items, the same node with 1024 items must decode (checked by both implementations).", "",
              "| name | node bytes |", "|---|---|"]
    for c in v["decode_invalid"]:
        desc = show_node_gen(c["node_gen"]) if "node_gen" in c else show_node_bytes(bytes.fromhex(c["bytes_hex"]))
        lines.append("| %s | %s |" % (c["name"], desc))
    lines += ["", "### 3.5 Accepted when reading, never produced when writing", "",
              "A decoder must accept these nodes (the read-time rules are structural and do not use Unicode "
              "tables); the write path never produces them: `tree.Build` rejects the equivalent input or "
              "normalizes the name.", "",
              "| name | violated write-time rule | node |", "|---|---|---|"]
    for c in v["decode_valid_build_rejects"]:
        lines.append("| %s | `%s`%s | %s |" % (c["name"], c["write_rule"], ucd_tag(c),
                                              show_node_bytes(bytes.fromhex(c["bytes_hex"]))))
    lines += ["", "### 3.6 Whole trees", "",
              "Each case is a set of nodes and a root hash. A reader that walks the tree must accept "
              "`walk_valid` and reject `walk_invalid` with the given category: `invalid_node` (a node violates "
              "the single-node rules, spec 3.8) or `invalid_tree` (every node decodes, but the tree violates the "
              "whole-tree rules, spec 3.6 and 3.8).", "",
              "| section | name | root | nodes | expected |", "|---|---|---|---|---|"]
    for section in ("walk_valid", "walk_invalid"):
        for c in v[section]:
            parts = []
            for n in c["nodes"]:
                if "node_gen" in n:
                    parts.append("%s `%s…`" % (show_node_gen(n["node_gen"]), n["hash"][7:19]))
                else:
                    b = bytes.fromhex(n["hex"])
                    parts.append("`%s…` %s" % (n["hash"][7:19], show_node_bytes(b)))
            exp = "✗ `%s`" % c["error"] if "error" in c else "✓ valid"
            lines.append("| %s | %s | `%s…` | %s | %s |" % (section, c["name"], c["root"][7:19],
                                                             "<br>".join(parts), exp))
    lines.append("")


REVIEW_COVERAGE = """## 4. Coverage checklist

| requirement | where |
|---|---|
| contenthash: empty, `hello`, `hello\\n`, non-ASCII, 1 MiB deterministic pseudo-random | `contenthash.json` (+ NFC/NFD content, bytes 00–ff) |
| contenthash: strict parsing | `contenthash.json` → `parse` |
| path: NFC and NFD `café`; `Plan.dwg` vs `plan.dwg`; `ß` vs `SS` | `pathx.json` → `cafe-*`, `case-conflict`, `sharp-s-vs-ss` |
| path: `..`, `a//b`, trailing `/`, absolute, backslash, control characters | `pathx.json` → `dot*`, `double-slash`, `trailing-slash`, `absolute`, `backslash*`, `nul`/`newline`/`tab`/`del` |
| path: 256-byte segment, over-long path, emoji, Chinese | `segment-256-bytes`, `path-4097-bytes` (+ 255/4096 boundaries), `emoji`, `chinese` |
| fold key NFD(casefold(NFD)): Cherokee, Georgian, `ı` vs `I`, canonical equivalence after folding | `pathx.json` → `fold`, `siblings` → `cherokee-*`, `georgian-*`, `dotless-i-vs-capital-i`, `capital-sharp-s-acute-vs-s-s-acute`, `fi-ligature-acute-vs-f-i-acute`; `tree.json` → `reject-cherokee-*`, `reject-georgian-*`, `reject-capital-sharp-s-acute-vs-s-s-acute`, `reject-fi-ligature-acute-vs-f-i-acute`, `allowed-dotless-i-and-capital-i` |
| V = 15.0.0 pinned | `ucd_version` cases: `assigned-in-15.0-u1f6dc-allowed`, `assigned-in-15.1-u2ebf0`, `assigned-in-16.0-u1c89`, `assigned-in-17.0-u1f6d8`, two branch names, `allowed-assigned-in-15.0-u1f6dc`, `reject-assigned-in-15.1-u2ebf0`, `name-assigned-in-17.0-u1f6d8` |
| tree: empty tree, single file, nested, Chinese/emoji/`<&>`/`"` names, executable | `empty-root`, `single-file`, `nested`, `deep-path`, `special-names`, `executable-mode` |
| tree: exactly 1024 entries (no split), 1025 entries (2 pages) | `dir-1024-entries-one-node`, `dir-1025-entries-two-pages`, `subdir-3000-entries-three-pages` |
| tree: page split caused by the 1 MiB limit | **no longer a required case**: it cannot be constructed under the format-1 path rules (spec §3.5); the byte rule is tested with reduced thresholds (`TestSplitByBytesGreedy`, `TestSplitByCountMultiLevel`), and `longest-names-1024-entries-still-one-node` shows the largest node real names produce |
| tree: walk order is path order, not full-path byte order | `walk-order-is-path-order-not-full-path-byte-order` (`walk_order`) |
| tree: conflicting input rejected | `reject-*` |
| tree: multi-level index | `two-level-index-1048577-entries` (heavy) and `TestSplitByCountMultiLevel` |
| tree: input order does not change the root; a change rewrites only the path to the root | `internal/tree/property_test.go` |
| tree: Build → Walk restores the input | every case in `TestTreeVectors` |
| tree: strict decoding (read-time rules), bytes in hex, invalid UTF-8, surrogate escapes, more than 1024 items | `decode_invalid`, `FuzzDecode` |
| tree: whole-tree rules (empty subdirectory, dir_page as a directory, index to a dir, count/first, order across pages, canonical split) | `walk_valid`, `walk_invalid` |
| path, write time: C1 controls, noncharacters, the fixed disallowed list, unassigned code points; allowed ZWJ/ZWNJ, variation selectors, tag characters, CGJ | `pathx.json` → `c1-control-*`, `noncharacter-*`, `bidi-*`, `arabic-letter-mark-u061c`, `zero-width-space-u200b`, `word-joiner-u2060`, `byte-order-mark-ufeff`, `soft-hyphen-u00ad`, `mongolian-vowel-separator-u180e`, `line-separator-u2028`, `paragraph-separator-u2029`, `function-application-u2061`, `invisible-*`, `interlinear-annotation-*`, `unassigned-*`, `assigned-in-*`, `*-allowed`, `order-*`; `tree.json` → `reject-c1-control`, `reject-noncharacter*`, `reject-bidi-override`, `reject-zero-width-space`, `reject-byte-order-mark-in-subdirectory`, `reject-unassigned-*`, `reject-soft-hyphen`, `reject-line-separator`, `reject-invisible-times`, `reject-interlinear-annotation`, `allowed-zwj-zwnj-fullwidth-reserved-names`, `allowed-variation-selectors-tags-cgj` |
| read time: structural rules only (noncharacters and controls rejected; NFD, case variants, unassigned code points and the disallowed list readable) | `pathx.json` → `stored_names`; `tree.json` → `decode_valid_build_rejects`, `decode_invalid` → `name-with-c1-*`, `*noncharacter*`, `index-first-with-c1-control` |
| lookups: read-time character rules on the input, then NFC, then segment rules and byte limits on the NFC form; NFD input over 255 bytes accepted, too long after NFC rejected; the disallowed list, unassigned code points and case not checked | `pathx.json` → `lookup` |
| vector files cannot be rewritten by an editor | all three JSON files are ASCII (`\\u` escapes); `verify` and the Go tests reject raw non-ASCII bytes |

## 5. Notes for the reviewer

1. **The 1 MiB rule cannot trigger with valid paths** (confirmed: the rule stays as a defensive cap). A file entry is at most 651 bytes
   (255-byte name made of `"` escapes to 510 bytes, plus a 16-digit size), so 1024 entries
   encode to at most ~667 KB, and 1024 index references to at most ~644 KB. The splitting
   therefore always happens at the 1024-entry limit: every page except the last has exactly
   1024 items. The byte rule stays in the format as a guard;
   `longest-names-1024-entries-still-one-node` shows the largest single node (649,253 bytes)
   that real names produce.
2. **`count` in a `dir_index` reference** (confirmed definition) is the number of directory
   entries (leaves) below the referenced node (for a lower-level index: the sum of its
   counts), at least 1, and `first` is the name of the first leaf below it. The specification
   states this with a two-level example taken from `two-level-index-1048577-entries` (spec §3.4).
3. **Integers are limited to 0…2^53−1** (confirmed) so that any RFC 8785 implementation
   reproduces the bytes: JCS serializes numbers as IEEE-754 doubles, which are exact for all
   integers only up to 2^53−1.
4. **Unicode version V = 15.0.0, fixed by the specification.** NFC, case folding and the
   unassigned check depend on Unicode tables. The Go standard library and `golang.org/x/text`
   both use Unicode 15.0.0 under Go 1.26, and `TestUnicodeVersionV` fails if either changes.
   Cases whose result depends on the version carry `ucd_version`; this Python check ran with
   Unicode %(pyuni)s%(skipnote)s. The other vectors use code points with the same status in
   Unicode 14.0 to 17.0.
5. **Allowed on purpose**: ZWNJ U+200C and ZWJ U+200D (emoji sequences such as
   `\U0001f468\\u200d\U0001f469\\u200d\U0001f467`, Persian and Indic text), variation selectors U+FE00–U+FE0F
   (emoji presentation), tag characters U+E0020–U+E007F (subdivision flags such as the flag
   of Scotland), U+034F COMBINING GRAPHEME JOINER, fullwidth solidus U+FF0F, private-use
   characters, names that are reserved on some platforms (`CON`, trailing dots or spaces).
   Platform-specific names are left to a future export layer.
6. **golang.org/x/text folds Cherokee the wrong way round** (it swaps the two cases instead of
   folding to the uppercase letters, as CaseFolding.txt does). The Go code corrects the 86
   lowercase letters after folding; `TestFoldKeyExhaustive` checks every assigned code point
   (idempotence and equal keys across each `unicode.SimpleFold` orbit).
"""


REVIEW_STATUSES = ("pending-human-review", "reviewed")


def review_state():
    """(status, reviewed_by, reviewed_on) shared by all vector files, or None
    if the files disagree."""
    states = {(load(n).get("review_status"), load(n).get("reviewed_by"), load(n).get("reviewed_on")) for n in VECTOR_FILES}
    return states.pop() if len(states) == 1 else None


def review_header():
    """The banner of REVIEW.md, from the review fields of the vector files."""
    state = review_state()
    if state and state[0] == "reviewed":
        status = ("> **Reviewed by %s on %s.** Every vector file in this directory carries\n"
                  "> `\"review_status\": \"reviewed\"` with `reviewed_by` and `reviewed_on`. A change to an\n"
                  "> expected value needs a new review (see README.md).") % (state[1], state[2])
    else:
        status = ("> **Status: pending human review.** The vector files carry\n"
                  "> `\"review_status\": \"pending-human-review\"`. Treat the expected values as\n"
                  "> provisional until a maintainer has checked this summary and flipped the status.")
    return ("# Test vector review summary\n\n" + status + "\n>\n"
            "> This file is generated by `python3 scripts/vectors.py review`; do not edit it by hand.\n\n")

REVIEW_METHOD = """## How the expected values were produced

* **contenthash** — inputs are written by hand; the expected hashes were
  computed with `shasum -a 256` and with `python3 -I` + `hashlib` (both agree),
  not with the Go code. The Go tests then check the implementation against them.
* **pathx** — expected results are written by hand from the rules, and checked by
  an independent Python implementation (`scripts/vectors.py`, stdlib
  `unicodedata`) as well as by the Go tests.
* **tree** — inputs are written by hand (`tree.json`); the expected roots, node
  lists and canonical bytes are computed by the independent Python
  implementation in `scripts/vectors.py` (written from the specification, two
  serializers — a hand-written JCS writer and `json.dumps` — must agree). The Go
  implementation is then checked against them, and `crosscheck` runs the Go CLI on
  materialized inputs and verifies every node file with `shasum -a 256`.

Check it yourself (three commands; needs Go, Python 3 and `shasum`):

```bash
d=$(mktemp -d) && python3 scripts/vectors.py materialize nested "$d/in"
go run ./cmd/mfgraph debug tree "$d/in" --out "$d/nodes"   # last line: root sha256:99f1d781…; compare with tree.json
(cd "$d/nodes" && for f in *.json; do echo "${f%.json}  $f"; done | shasum -a 256 -c -)
```

Re-run everything:

```bash
python3 scripts/vectors.py verify [--heavy]   # Python reference vs. vector files
python3 scripts/vectors.py crosscheck         # Go CLI + shasum on every small/large case
go test ./internal/contenthash ./internal/pathx ./internal/tree   # MFGRAPH_HEAVY_VECTORS=1 for the heavy case
```

"""


REVIEW_PATCH4_CHANGES = """## 冻结前补丁轮 4 变更 (patch round 4 before the format freeze)

No expected value of an existing case changed, and no root hash or node byte of a tree case.

* **New section `lookup` in `pathx.json`** (31 cases): the lookup rules for a path that finds
  an existing entry (specification 2.6), checked by `pathx.NormalizeLookup` and by the Python
  reference. Accepted with their NFC form: NFD input whose segment has more than 255 bytes as
  given (`nfd-hangul-270-bytes-is-90-after-nfc`, 30 Hangul syllables; a path of 4335 bytes that
  has 1455 after NFC), characters of the disallowed list (`disallowed-char-*-accepted`), a code
  point unassigned in V (`assigned-in-17.0-u1f6d8-accepted`) and case variants. Rejected:
  control characters, noncharacters, backslash, absolute paths, empty and dot segments, and
  segments too long after NFC (`nfd-hangul-774-bytes-is-258-after-nfc`,
  `segment-255-bytes-is-510-after-nfc-u0958`: 255 bytes as given, 510 after NFC). These cases
  were added after the review of 2026-11-14.
* **Cases renamed: U+1F6D8 is assigned in Unicode 17.0, not 16.0.** `pathx.json` `normalize`
  `assigned-in-16.0-u1f6d8` is now `assigned-in-17.0-u1f6d8`, and `tree.json`
  `decode_valid_build_rejects` `name-assigned-in-16.0-u1f6d8` is now
  `name-assigned-in-17.0-u1f6d8`. 用例改名，期望值没有变化 (only the names changed; the
  expected values did not). The other `assigned-in-*` names were checked against the Unicode
  tables of Python 3.11, 3.12, 3.14 and 3.15 (Unicode 14.0, 15.0, 16.0, 17.0): U+1F6DC (15.0)
  and U+1C89 (16.0) are right; U+2EBF0 is unassigned in 15.0 and assigned in 16.0, which agrees
  with 15.1 (no Python has the tables of 15.1).
"""


REVIEW_FREEZE_CHANGES = """## 冻结前修复轮变更 (changes before the format freeze)

No root hash or node byte of an existing positive tree case changed: all 18 positive cases of
the previous revision were compared field by field (root, node count, nodes with canonical
bytes).

* **All vector files are ASCII.** Every non-ASCII character is a `\\u` escape
  (`python3 scripts/vectors.py format`); `verify` and the Go tests reject raw non-ASCII bytes.
  The decoded values did not change.
* **Fold key = NFD(casefold(NFD(name)))**, the canonical caseless match of Unicode 3.13
  (D145). `fold`: `café`, `cafe\\u0301` and `CAFÉ` now fold to `cafe\\u0301` (the key
  is NFD); new fold cases for Cherokee (U+13A0/U+AB70, U+13F8/U+13F0), Georgian
  (U+1C90/U+10D0), `ı`/`I`, U+1E9E U+0301 / `sś`, U+FB01 U+0301 / `fí` and U+1FB3.
  New `siblings` cases `cherokee-*`, `georgian-mtavruli-vs-mkhedruli`, `dotless-i-vs-capital-i`
  (no conflict), `capital-sharp-s-acute-vs-s-s-acute`, `fi-ligature-acute-vs-f-i-acute`; new
  Build cases `reject-cherokee-case-conflict`, `reject-cherokee-ye-case-conflict-in-subdirectory`,
  `reject-georgian-mtavruli-vs-mkhedruli`, `reject-capital-sharp-s-acute-vs-s-s-acute`,
  `reject-fi-ligature-acute-vs-f-i-acute` (`case_conflict`) and the positive case
  `allowed-dotless-i-and-capital-i`.
* **Read-time rules are structural only; new error kind `noncharacter`.**
  * Error kind changed: `normalize` `noncharacter-uffff`, `noncharacter-ufdd0` and the Build
    case `reject-noncharacter` (`unassigned_char` → `noncharacter`); `stored_names`
    `noncharacter-accepted` is now `noncharacter-uffff` and rejected (`noncharacter`); the
    15 `stored_names` with a character of the disallowed list are now accepted and renamed
    `<name>-accepted`.
  * Moved: the 20 `decode_invalid` cases rejected only for a disallowed character
    (`name-with-bidi-*`, `name-with-arabic-letter-mark-u061c`, `name-with-zero-width-space-u200b`,
    `name-with-word-joiner-u2060`, `name-with-byte-order-mark-ufeff`, `dir-entry-name-with-bidi-u202b`,
    `index-first-with-bidi-u202e`, `name-with-soft-hyphen-u00ad`,
    `name-with-mongolian-vowel-separator-u180e`, `name-with-line-separator-u2028`,
    `name-with-paragraph-separator-u2029`, `name-with-function-application-u2061`,
    `name-with-invisible-*`, `name-with-interlinear-annotation-*`) to `decode_valid_build_rejects`
    with `write_rule: disallowed_char`; `name-noncharacter-uffff` from
    `decode_valid_build_rejects` to `decode_invalid`.
  * New: noncharacter cases in `normalize` (`noncharacter-ufffe`, `-ufdef`, `-plane1-u1ffff`,
    `-plane16-u10fffe`, three `order-*`), `branch_names`, `stored_names`, `decode_invalid`
    (`name-with-noncharacter-ufdd0`, `dir-entry-name-with-noncharacter-u10fffe`,
    `index-first-with-noncharacter-u1ffff`) and Build (`reject-noncharacter-plane16-in-subdirectory`).
* **V = 15.0.0 is pinned.** Cases tagged `"ucd_version": "15.0.0"`: U+1F6DC (assigned in
  15.0) is accepted (`assigned-in-15.0-u1f6dc-allowed`, a branch name,
  `allowed-assigned-in-15.0-u1f6dc`); U+2EBF0 (15.1), U+1C89 (16.0) and U+1F6D8 (17.0) are
  `unassigned_char` (`assigned-in-15.1-u2ebf0`, `assigned-in-16.0-u1c89`,
  `assigned-in-17.0-u1f6d8`, a branch name, `reject-assigned-in-15.1-u2ebf0`,
  `name-assigned-in-17.0-u1f6d8`). `stored_names` `assigned-after-15.0-accepted` shows that
  the read-time rules accept them. Python checks the tagged cases only with Unicode 15.0.0.
* **Node bytes in hex.** `decode_invalid` and `decode_valid_build_rejects` give `bytes_hex`.
  New `decode_invalid` cases: invalid UTF-8 in a name (`name-invalid-utf8-byte-ff`,
  `-truncated-sequence`, `-overlong-solidus`, `-encoded-surrogate`, `-above-u10ffff`), lone
  surrogate escapes (`name-lone-surrogate-escape-ud800`, `name-lone-low-surrogate-escape-udc00`),
  `name-surrogate-pair-escape-not-canonical`, `name-escaped-noncharacter-uffff`,
  `invalid-utf8-after-the-object`, and nodes with 1025 items (`dir-with-1025-entries`,
  `dir-page-with-1025-entries`, `dir-index-with-1025-references`, given by the node generator).
* **Whole-tree vectors**: `walk_valid` (`canonical-split-1025-entries`) and 13 `walk_invalid`
  cases with the category `invalid_node` or `invalid_tree`: `subdirectory-empty`,
  `root-is-a-dir-page`, `directory-entry-points-to-a-dir-page`, `index-references-a-dir-node`,
  `reference-count-too-large`, `reference-count-too-small`, `reference-first-mismatch`,
  `reference-count-zero` (`invalid_node`), `entries-out-of-order-across-pages`,
  `duplicate-name-across-pages`, `split-not-canonical-uneven-pages`,
  `split-not-canonical-index-over-one-small-page`, `subdirectory-split-not-canonical`.
* **Path order**: positive case `walk-order-is-path-order-not-full-path-byte-order` locks the
  walk order `a`, `a/x`, `a-b`, `a.txt` (full-path byte order would be `a`, `a-b`, `a.txt`, `a/x`).
"""


REVIEW_CHANGES = """## Earlier changes (收尾轮)

Path rules are now split into **write-time rules** (applied to paths that are uploaded or
committed, and by `tree.Build`) and **read-time rules** (applied to names inside stored tree
nodes; they never depend on Unicode tables, so stored trees stay readable when the tables
are upgraded). No root hash or node byte of an existing positive tree case changed.

`pathx.json`:

* changed: `c1-control-not-rejected` (`a\\u0085b` used to be accepted) is now
  `c1-control-u0085` → ✗ `control_char` (C1 controls are control characters);
* new write-time rejections: `c1-control-u009f` (`control_char`); `bidi-*` and
  `arabic-letter-mark-u061c`, `zero-width-space-u200b`, `word-joiner-u2060`,
  `byte-order-mark-ufeff` (`disallowed_char`); `unassigned-u0378`,
  `unassigned-plane4-u40000`, `noncharacter-uffff`, `noncharacter-ufdd0`
  (`unassigned_char`, Unicode 15.0.0);
* new cases that lock the check order: `order-*` (absolute first; then the first offending
  character from left to right; all character checks run before NFC and before the segment
  rules);
* new allowed cases: `zwj-emoji-sequence-allowed`, `zwnj-allowed`,
  `windows-reserved-name-allowed`, `trailing-dot-allowed`, `trailing-space-allowed`,
  `private-use-allowed`;
* branch names: `rel\\u0085`, `main\\u202e`, `\\u0378` rejected; a ZWJ emoji sequence
  accepted;
* new section `stored_names`: the read-time rules for names in stored nodes
  (NFD names, case variants and unassigned code points are accepted; C1 controls, bidi
  controls and zero-width characters are rejected).

`tree.json`:

* moved from `decode_invalid` to the new section `decode_valid_build_rejects`:
  `name-not-nfc`, `case-conflict-in-node`, `index-first-not-nfc` — decoders now accept
  them, the write path never produces them;
* also new in `decode_valid_build_rejects`: `sharp-s-conflict-in-node`,
  `file-and-dir-differ-in-case`, `name-unassigned-u0378`, `name-noncharacter-uffff`,
  `index-first-unassigned-plane4`;
* new in `decode_invalid` (read-time rejections): `name-with-c1-control-*`,
  `name-with-bidi-*`, `name-with-arabic-letter-mark-u061c`, `name-with-zero-width-space-u200b`,
  `name-with-word-joiner-u2060`, `name-with-byte-order-mark-ufeff`,
  `dir-entry-name-with-bidi-u202b`, `index-first-with-bidi-u202e`,
  `index-first-with-c1-control`;
* new Build rejections: `reject-c1-control` (`control_char`), `reject-bidi-override`,
  `reject-zero-width-space`, `reject-byte-order-mark-in-subdirectory` (`disallowed_char`),
  `reject-unassigned-u0378`, `reject-unassigned-plane4`, `reject-noncharacter`
  (`unassigned_char`);
* new positive case `allowed-zwj-zwnj-fullwidth-reserved-names`;
* all 16 existing positive cases keep their root hash and node bytes; the 9 existing
  rejections keep their error kinds.

Added later in this revision — a fixed list of further invisible characters (not the whole
category Cf) is rejected at write time and at read time with `disallowed_char`: U+00AD
SOFT HYPHEN, U+180E MONGOLIAN VOWEL SEPARATOR, U+2028 LINE SEPARATOR, U+2029 PARAGRAPH
SEPARATOR, U+2061–U+2064 invisible mathematical operators, U+FFF9–U+FFFB interlinear
annotation controls.

* `pathx.json`: one write-time rejection per character (`soft-hyphen-u00ad`,
  `mongolian-vowel-separator-u180e`, `line-separator-u2028`, `paragraph-separator-u2029`,
  `function-application-u2061`, `invisible-times-u2062`, `invisible-separator-u2063`,
  `invisible-plus-u2064`, `interlinear-annotation-*-ufff9/ufffa/ufffb`), the same eleven in
  `stored_names` (read time), and the branch name `release\\u00ad1`;
* still allowed, with new cases: variation selectors (`emoji-with-variation-selector-ufe0f-allowed`,
  `text-variation-selector-ufe0e-allowed`), tag characters (`tag-sequence-flag-of-scotland-allowed`,
  the flag of Scotland), U+034F (`combining-grapheme-joiner-u034f-allowed`), and the same in
  `stored_names`;
* `tree.json`: eleven `decode_invalid` cases `name-with-<character>`; Build rejections
  `reject-soft-hyphen`, `reject-line-separator`, `reject-invisible-times`,
  `reject-interlinear-annotation`; new positive case `allowed-variation-selectors-tags-cgj`;
* all 17 existing positive cases (including `allowed-zwj-zwnj-fullwidth-reserved-names`) keep
  their root hash and node bytes.
"""


def count_ucd_cases():
    n = 0
    for name, sections in (("pathx.json", ("normalize", "branch_names")),
                           ("tree.json", ("cases", "decode_valid_build_rejects"))):
        v = load(name)
        n += sum(1 for sec in sections for c in v[sec] if "ucd_version" in c)
    return n


def write_review():
    lines = [review_header(), REVIEW_PATCH4_CHANGES, REVIEW_FREEZE_CHANGES, REVIEW_CHANGES, REVIEW_METHOD]
    review_contenthash(lines)
    review_pathx(lines)
    review_tree(lines)
    skipnote = ("" if unicodedata.unidata_version == V else
                " and skipped the %d tagged case(s) (they need Unicode %s; the Go tests cover them)" % (
                    count_ucd_cases(), V))
    lines.append(REVIEW_COVERAGE % {"pyuni": unicodedata.unidata_version, "skipnote": skipnote})
    path = os.path.join(VECTORS, "REVIEW.md")
    with open(path, "w", encoding="utf-8", newline="\n") as f:
        f.write("\n".join(lines).rstrip("\n") + "\n")
    print("wrote", os.path.relpath(path, ROOT))


# ------------------------------------------------------- materialize / crosscheck

def materialize(case, dest):
    """Write the files of a tree case below dest (content and mode)."""
    files = tree_files(case)
    contents = {f["path"]: f["content"].encode("utf-8") for f in case.get("files", [])}
    for f in files:
        target = os.path.join(dest, *f["path"].split("/"))
        os.makedirs(os.path.dirname(target), exist_ok=True)
        with open(target, "wb") as fh:
            fh.write(contents.get(f["path"], f["path"].encode("utf-8")))
        os.chmod(target, 0o755 if f["mode"] == 493 else 0o644)
    os.makedirs(dest, exist_ok=True)


def find_case(name):
    for c in load("tree.json")["cases"]:
        if c["name"] == name:
            return c
    raise SystemExit("unknown case %r" % name)


def crosscheck(heavy):
    import subprocess
    import tempfile
    exe = os.path.join(tempfile.mkdtemp(prefix="mfgraph-xc-"), "mfgraph")
    subprocess.run(["go", "build", "-o", exe, "./cmd/mfgraph"], cwd=ROOT, check=True)
    checked = 0
    for c in load("tree.json")["cases"]:
        if "error" in c or (c.get("heavy") and not heavy):
            continue
        work = tempfile.mkdtemp(prefix="mfgraph-xc-")
        src, out = os.path.join(work, "in"), os.path.join(work, "nodes")
        materialize(c, src)
        res = subprocess.run([exe, "debug", "tree", src, "--out", out], capture_output=True, check=True)
        root = res.stdout.decode("utf-8").strip().splitlines()[-1].split()[-1]
        check(root == c["root"], "crosscheck %s: CLI root %s != %s" % (c["name"], root, c["root"]))
        names = sorted(os.listdir(out))
        check(len(names) == c["node_count"], "crosscheck %s: %d node files, want %d" % (c["name"], len(names), c["node_count"]))
        # Independent hash tool over every written node file.
        for i in range(0, len(names), 256):
            batch = names[i:i + 256]
            sums = subprocess.run(["shasum", "-a", "256"] + batch, cwd=out, capture_output=True, check=True)
            for line in sums.stdout.decode().splitlines():
                digest, fname = line.split(None, 1)
                check(fname.strip() == digest + ".json", "crosscheck %s: %s does not hash to its name" % (c["name"], fname))
        print("crosscheck %-45s root ok, %d node files ok (shasum)" % (c["name"], len(names)))
        checked += 1
    return checked


# ----------------------------------------------------------------------- main

def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    vp = sub.add_parser("verify", help="check every vector file")
    vp.add_argument("--heavy", action="store_true", help="include heavy cases")
    sub.add_parser("review", help="regenerate testdata/vectors/REVIEW.md")
    sub.add_parser("generate", help="(re)compute the expected values in tree.json from its inputs")
    sub.add_parser("format", help="rewrite the vector files in their ASCII-only layout")
    mp = sub.add_parser("materialize", help="write the files of a tree case into a directory")
    mp.add_argument("case")
    mp.add_argument("dir")
    cp = sub.add_parser("crosscheck", help="run the Go CLI on every tree case and check node files with shasum")
    cp.add_argument("--heavy", action="store_true")
    args = ap.parse_args()

    if args.cmd == "materialize":
        materialize(find_case(args.case), args.dir)
        return 0
    if args.cmd == "crosscheck":
        n = crosscheck(args.heavy)
        for msg in FAILURES:
            print("FAIL:", msg)
        print("crosschecked %d case(s); %s" % (n, "%d failure(s)" % len(FAILURES) if FAILURES else "all agree"))
        return 1 if FAILURES else 0

    if args.cmd == "generate":
        verify_tree(heavy=True, update=True)
        if UCD_SKIPPED:
            print("kept the stored expected values of %d case(s) that need Unicode %s (this Python has %s): %s"
                  % (len(UCD_SKIPPED), V, unicodedata.unidata_version, ", ".join(UCD_SKIPPED)))
        return 0
    if args.cmd == "format":
        format_vectors()
        return 0
    if args.cmd == "review":
        write_review()
        return 0
    if args.cmd == "verify":
        counts = {
            "ascii_files": verify_ascii(),
            "review_fields": verify_review_fields(),
            "contenthash": verify_contenthash(),
            "pathx": verify_pathx(),
            "tree": verify_tree(args.heavy),
        }
        for msg in FAILURES:
            print("FAIL:", msg)
        print("checked: " + ", ".join("%s=%d" % kv for kv in counts.items()))
        print("unicode data version (python):", unicodedata.unidata_version)
        if UCD_SKIPPED:
            print("skipped %d case(s) that need Unicode %s (this Python has %s; the Go tests cover them): %s"
                  % (len(UCD_SKIPPED), V, unicodedata.unidata_version, ", ".join(UCD_SKIPPED)))
        if FAILURES:
            print("%d failure(s)" % len(FAILURES))
            return 1
        print("all vectors agree")
        return 0
    return 2


if __name__ == "__main__":
    sys.exit(main())
