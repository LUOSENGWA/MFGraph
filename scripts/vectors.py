#!/usr/bin/env python3
"""Independent reference implementation for the MFGraph test vectors.

This script re-implements the frozen content formats from the written
specification (docs/spec/content-format.md) using only the Python standard
library (hashlib, unicodedata, json). It shares no code with the Go
implementation, so agreement between the two is a cross-check.

Usage:
  python3 scripts/vectors.py verify             # check all vector files
  python3 scripts/vectors.py verify --heavy     # also the multi-million-entry cases

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


def _check_chars(s):
    for ch in s:
        if ch == "\\":
            raise PathError("backslash")
        if ord(ch) < 0x20 or ord(ch) == 0x7F:
            raise PathError("control_char")


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
    return n


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
              "Rule order: UTF-8 + non-empty → absolute / backslash / control (U+0000–U+001F, U+007F) → NFC → "
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
    lines.append("")


REVIEW_HEADER = """# Test vector review summary

> **Status: pending human review.** Every vector file in this directory carries
> `"review_status": "pending-human-review"`. Treat the expected values as
> provisional until a maintainer has checked this summary and flipped the status.
>
> This file is generated by `python3 scripts/vectors.py review`; do not edit it by hand.

## How the expected values were produced

* **contenthash** — inputs are written by hand; the expected hashes were
  computed with `shasum -a 256` and with `python3 -I` + `hashlib` (both agree),
  not with the Go code. The Go tests then check the implementation against them.
* **pathx** — expected results are written by hand from the rules, and checked by
  an independent Python implementation (`scripts/vectors.py`, stdlib
  `unicodedata`) as well as by the Go tests.
* Unicode data: the Go implementation uses `golang.org/x/text` (Unicode 17.0.0);
  the Python check ran with Python's `unicodedata` (Unicode %(pyuni)s). The vectors
  only use characters whose NFC and case-fold behavior is the same in both versions.

Re-run all checks:

```bash
python3 scripts/vectors.py verify
go test ./internal/contenthash ./internal/pathx
printf 'hello\\n' | shasum -a 256   # 5891b5b5…6be03, see table below
```

"""


def write_review():
    lines = [REVIEW_HEADER % {"pyuni": unicodedata.unidata_version}]
    review_contenthash(lines)
    review_pathx(lines)
    path = os.path.join(VECTORS, "REVIEW.md")
    with open(path, "w", encoding="utf-8", newline="\n") as f:
        f.write("\n".join(lines).rstrip("\n") + "\n")
    print("wrote", os.path.relpath(path, ROOT))


# ----------------------------------------------------------------------- main

def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    vp = sub.add_parser("verify", help="check every vector file")
    vp.add_argument("--heavy", action="store_true", help="include heavy cases")
    sub.add_parser("review", help="regenerate testdata/vectors/REVIEW.md")
    args = ap.parse_args()

    if args.cmd == "review":
        write_review()
        return 0
    if args.cmd == "verify":
        counts = {
            "contenthash": verify_contenthash(),
            "pathx": verify_pathx(),
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
