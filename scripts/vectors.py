#!/usr/bin/env python3
"""Independent reference implementation for the MFGraph test vectors.

This script re-implements the frozen content formats from the written
specification (docs/spec/content-format.md) using only the Python standard
library (hashlib, unicodedata, json). It shares no code with the Go
implementation, so agreement between the two is a cross-check.

Usage:
  python3 scripts/vectors.py verify [--heavy]      # check all vector files
  python3 scripts/vectors.py generate              # recompute expected values in tree.json
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
        raw = raw.encode("utf-8")
    if len(raw) == 0:
        raise PathError("empty")
    try:
        return raw.decode("utf-8", errors="strict")
    except UnicodeDecodeError:
        raise PathError("invalid_utf8")


# Bidirectional formatting characters and invisible zero-width characters.
# ZWNJ U+200C and ZWJ U+200D are allowed (emoji sequences, several scripts).
DISALLOWED = {0x061C, 0x200E, 0x200F, 0x200B, 0x2060, 0xFEFF} | set(range(0x202A, 0x202F)) | set(range(0x2066, 0x206A))


def char_problem(ch, write):
    """Error kind for one code point, or None. Write-time rules (write=True)
    also reject code points of general category Cn (unassigned)."""
    o = ord(ch)
    if ch == "\\":
        return "backslash"
    if o < 0x20 or 0x7F <= o <= 0x9F:
        return "control_char"
    if o in DISALLOWED:
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


def normalize_path(raw):
    s = _decode(raw)
    if s[0] == "/":
        raise PathError("absolute")
    _check_chars(s)
    n = unicodedata.normalize("NFC", s)
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
    return n


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
    """Read-time rules for a name in a stored node: no Unicode tables involved
    (no NFC, no case folding, no unassigned check)."""
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
    return unicodedata.normalize("NFC", name).casefold()


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
        got, kind = _run(normalize_path, vector_input(case))
        label = "pathx normalize %s" % case["name"]
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
        got, kind = _run(normalize_segment, vector_input(case))
        label = "pathx branch %r" % (case.get("input", case.get("input_hex", "<gen>")),)
        if "error" in case:
            check(kind == case["error"], "%s: got %r/%r" % (label, got, kind))
        else:
            check(kind is None and got == vector_output(case), "%s: got %r/%r" % (label, got, kind))
        n += 1
    for case in v["stored_names"]:
        _, kind = _run(check_stored_name, vector_input(case))
        check(kind == case.get("error"), "pathx stored name %s: got %r, want %r" % (case["name"], kind, case.get("error")))
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
    if rule == "unassigned_char":
        return any(_run(normalize_segment, n)[1] == "unassigned_char" for n in names)
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
    if "list_nodes" in case:
        # Large cases list only selected positions of the walk order.
        exp["walk_length"] = len(nodes)
        nodes = [dict(nodes[i], position=i % len(nodes)) for i in case["list_nodes"]]
    exp["nodes"] = nodes
    return exp


EXPECTED_KEYS = ("root", "node_count", "walk_length", "nodes", "error")


def verify_tree(heavy, update=False):
    path = os.path.join(VECTORS, "tree.json")
    v = load("tree.json")
    n = 0
    for case in v["cases"]:
        if case.get("heavy") and not heavy:
            continue
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
        try:
            decode_node(case["bytes"].encode("utf-8"))
            check(False, "tree decode_invalid %s: accepted" % case["name"])
        except DecodeError:
            pass
        n += 1
    for case in v["decode_valid_build_rejects"]:
        label = "tree decode_valid_build_rejects %s" % case["name"]
        try:
            obj = decode_node(case["bytes"].encode("utf-8"))
        except DecodeError as e:
            check(False, "%s: rejected by the decoder: %s" % (label, e))
            n += 1
            continue
        check(write_rule_violated(obj, case["write_rule"]), "%s: write rule %s not violated" % (label, case["write_rule"]))
        n += 1
    if update:
        with open(path, "w", encoding="utf-8", newline="\n") as f:
            f.write(dump_vectors(v))
        print("updated", os.path.relpath(path, ROOT))
    return n


def needs_escape(ch):
    cat = unicodedata.category(ch)
    invisible = cat[0] in "MC" or cat in ("Zl", "Zp") or (cat == "Zs" and ch != " ")
    lookalike = unicodedata.normalize("NFKC", ch) != ch or 0x1100 <= ord(ch) <= 0x11FF
    return (invisible or lookalike) and ord(ch) >= 0x80


def json_escape_char(ch):
    o = ord(ch)
    if o <= 0xFFFF:
        return "\\u%04x" % o
    o -= 0x10000
    return "\\u%04x\\u%04x" % (0xD800 + (o >> 10), 0xDC00 + (o & 0x3FF))


def dump_vectors(v):
    """Pretty-print a vector file. Invisible and look-alike non-ASCII
    characters (combining marks, compatibility characters, ...) are written
    as \\u escapes so that NFC/NFD inputs stay readable; the decoded values
    are unchanged."""
    text = json.dumps(v, ensure_ascii=False, indent=1)
    return "".join(json_escape_char(c) if needs_escape(c) else c for c in text) + "\n"


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


def show_case_result(case):
    if "error" in case:
        return "✗ `%s`" % case["error"]
    if "output_gen" in case:
        out = gen_path(case["output_gen"])
        return "✓ %s (%d bytes)" % (show_gen(case["output_gen"]), len(out.encode("utf-8")))
    return "✓ " + show(case["output"])


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
              "(backslash; control_char = U+0000–U+001F, U+007F–U+009F; disallowed_char = bidi controls "
              "U+061C, U+200E, U+200F, U+202A–U+202E, U+2066–U+2069 and zero-width U+200B, U+2060, U+FEFF; "
              "unassigned_char = general category Cn in Unicode 15.0.0) → NFC → "
              "empty / `.` / `..` segments → segment ≤ 255 B and path ≤ 4096 B (NFC bytes). First failure wins.", "",
              "### 2.1 Normalize", "", "| name | input | result |", "|---|---|---|"]
    for case in v["normalize"]:
        lines.append("| %s | %s | %s |" % (case["name"], show_case_input(case), show_case_result(case)))
    lines += ["", "### 2.2 FoldKey (NFC, then Unicode full case folding)", "",
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
              "Names inside stored tree nodes are checked only with rules that do not depend on Unicode "
              "tables: valid UTF-8, non-empty, no `/`, then the first offending character (backslash, "
              "control_char, disallowed_char), not `.`/`..`, at most 255 bytes as stored. No NFC, case-fold "
              "or unassigned checks, so trees stay readable after a Unicode upgrade.", "",
              "| name | input | result |", "|---|---|---|"]
    for case in v["stored_names"]:
        res = "✗ `%s`" % case["error"] if "error" in case else "✓ accepted"
        lines.append("| %s | %s | %s |" % (case["name"], show_case_input(case), res))
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
        lines += ["", "root: `%s`" % c["root"], ""]
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
              ", ".join("`%s`" % c["name"] for c in v["decode_invalid"]) + ".", "",
              "### 3.5 Accepted when reading, never produced when writing", "",
              "A decoder must accept these nodes (the read-time rules do not use Unicode tables); the write "
              "path never produces them: `tree.Build` rejects the equivalent input or normalizes the name.", "",
              "| name | violated write-time rule | node |", "|---|---|---|"]
    for c in v["decode_valid_build_rejects"]:
        lines.append("| %s | `%s` | %s |" % (c["name"], c["write_rule"], show(c["bytes"])))
    lines.append("")


REVIEW_COVERAGE = """## 4. Coverage checklist

| requirement | where |
|---|---|
| contenthash: empty, `hello`, `hello\\n`, non-ASCII, 1 MiB deterministic pseudo-random | `contenthash.json` (+ NFC/NFD content, bytes 00–ff) |
| contenthash: strict parsing | `contenthash.json` → `parse` |
| path: NFC and NFD `café`; `Plan.dwg` vs `plan.dwg`; `ß` vs `SS` | `pathx.json` → `cafe-*`, `case-conflict`, `sharp-s-vs-ss` |
| path: `..`, `a//b`, trailing `/`, absolute, backslash, control characters | `pathx.json` → `dot*`, `double-slash`, `trailing-slash`, `absolute`, `backslash*`, `nul`/`newline`/`tab`/`del` |
| path: 256-byte segment, over-long path, emoji, Chinese | `segment-256-bytes`, `path-4097-bytes` (+ 255/4096 boundaries), `emoji`, `chinese` |
| tree: empty tree, single file, nested, Chinese/emoji/`<&>`/`"` names, executable | `empty-root`, `single-file`, `nested`, `deep-path`, `special-names`, `executable-mode` |
| tree: exactly 1024 entries (no split), 1025 entries (2 pages) | `dir-1024-entries-one-node`, `dir-1025-entries-two-pages`, `subdir-3000-entries-three-pages` |
| tree: page split caused by the 1 MiB limit | **not constructible with valid names** — see note 1; covered by `TestSplitByBytesGreedy` with reduced limits and by `longest-names-1024-entries-still-one-node` |
| tree: conflicting input rejected | `reject-*` |
| tree: multi-level index | `two-level-index-1048577-entries` (heavy) and `TestSplitByCountMultiLevel` |
| tree: input order does not change the root; a change rewrites only the path to the root | `internal/tree/property_test.go` |
| tree: Build → Walk restores the input | every case in `TestTreeVectors` |
| tree: strict decoding | `decode_invalid`, `FuzzDecode` |

## 5. Notes for the reviewer

1. **The 1 MiB rule cannot trigger with valid paths.** A file entry is at most 651 bytes
   (255-byte name made of `"` escapes to 510 bytes, plus a 16-digit size), so 1024 entries
   encode to at most ~667 KB, and 1024 index references to at most ~644 KB. The splitting
   therefore always happens at the 1024-entry limit. The byte rule stays in the format as a
   guard; `longest-names-1024-entries-still-one-node` shows the largest single node
   (649,253 bytes) that real names produce.
2. **`count` in a `dir_index` reference** is the number of directory entries below the
   referenced node (for a lower-level index: the sum), and `first` is the first entry name
   below it. The specification did not say which count to use for nested indexes; this
   choice keeps one meaning at every level.
3. **Integers are limited to 0…2^53−1** so that any RFC 8785 implementation reproduces the
   bytes (JCS serializes numbers as IEEE-754 doubles).
4. **Unicode version V = 15.0.0.** NFC, case folding and the unassigned check depend on
   Unicode tables. Under Go 1.26 the standard library (`unicode`) and `golang.org/x/text`
   both use Unicode 15.0.0 (an earlier version of this file said 17.0.0; that was wrong:
   x/text selects its 15.0.0 tables for this Go version). The Python check ran with Unicode
   %(pyuni)s; every vector whose result depends on whether a code point is assigned uses
   code points with the same status in both versions (U+0378, U+40000, U+FFFF, U+FDD0 are
   unassigned in both; the 4,489 characters added in Unicode 15.0 are not used).
5. **Allowed on purpose**: ZWNJ U+200C and ZWJ U+200D (emoji sequences such as
   `👨\\u200d👩\\u200d👧`, Persian and Indic text), fullwidth solidus U+FF0F, private-use
   characters, names that are reserved on some platforms (`CON`, trailing dots or spaces).
   Platform-specific names are left to a future export layer.
"""


REVIEW_HEADER = """# Test vector review summary

> **Status: pending human review.** Every vector file in this directory carries
> `"review_status": "pending-human-review"`. Treat the expected values as
> provisional until a maintainer has checked this summary and flipped the status.
>
> This file is generated by `python3 scripts/vectors.py review`; do not edit it by hand.

"""

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


REVIEW_CHANGES = """## Changes in this revision (本轮变更)

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
* new section `stored_names` (27 cases): the read-time rules for names in stored nodes
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
"""


def write_review():
    lines = [REVIEW_HEADER, REVIEW_CHANGES, REVIEW_METHOD]
    review_contenthash(lines)
    review_pathx(lines)
    review_tree(lines)
    lines.append(REVIEW_COVERAGE % {"pyuni": unicodedata.unidata_version})
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
        return 0
    if args.cmd == "review":
        write_review()
        return 0
    if args.cmd == "verify":
        counts = {
            "contenthash": verify_contenthash(),
            "pathx": verify_pathx(),
            "tree": verify_tree(args.heavy),
        }
        for msg in FAILURES:
            print("FAIL:", msg)
        print("checked: " + ", ".join("%s=%d" % kv for kv in counts.items()))
        print("unicode data version (python):", unicodedata.unidata_version)
        if FAILURES:
            print("%d failure(s)" % len(FAILURES))
            return 1
        print("all vectors agree")
        return 0
    return 2


if __name__ == "__main__":
    sys.exit(main())
