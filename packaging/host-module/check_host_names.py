#!/usr/bin/env python3
"""Check that the host platform's name and its header prefix appear only in
packaging/host-module/, the one directory of the repository allowed to name
the host (its manifest template, module.env, acceptance script, this check
and its tests).

Scans the paths and the contents of every file git knows (tracked, and
untracked but not ignored). Run from the repository root (or pass the root):

  python3 packaging/host-module/check_host_names.py [ROOT]

Prints file:line of every hit. Exit code: 0 = clean, 1 = hits.

What counts as a hit (case as noted):

- the name joined or with '-' or '_' between its words, in any case and
  inside identifiers: AgentTeams, AgentTeam (singular), agentteams-service,
  agent_teams, NewAgentTeamsClient;
- the name as a proper noun, Agent Teams or AGENT TEAMS, with any white
  space between the words (several spaces, a no-break space, a line break,
  also with a comment leader such as // or # at the start of the next line);
  plain English such as "multi-agent teams" or "agent teams up" is not a hit;
- the header prefix: "atf" as a word of its own or as a word of an
  identifier (X-Atf-Actor, Atf-Actor, XAtfActor, x_atf, ATF_TOKEN), but not
  inside a word (Matfield) or a run of capitals and digits (an ID such as a
  ULID);
- the same after joining string literals that are concatenated
  ("X-" + "Atf-", "Agent" + "Teams", adjacent literals);
- in files encoded as UTF-16 or UTF-32 (with or without a byte order mark)
  as well as UTF-8.

A line that contains the marker host-name-check: allow is not reported
(for a hit across lines: any of its lines).
"""
import os
import re
import subprocess
import sys

ALLOWED = "packaging/host-module/"
ALLOW_MARKER = "host-name-check: allow"

# White space and invisible separators between the two words of the name,
# and comment leaders at the start of a continued line.
_SEP = r"[\s\u200b\u2060\ufeff/#*;>]+"
JOINED = re.compile(r"agent[-_]?teams?", re.IGNORECASE)
SPACED = re.compile(r"(?<![A-Za-z0-9_-])(?:Agent|AGENT)" + _SEP + r"(?:Teams?|TEAMS?)(?![a-z])")
RUN = re.compile(r"[A-Za-z0-9]+")
CAMEL = re.compile(r"[A-Z]+(?=[A-Z][a-z])|[A-Z]?[a-z]+|[A-Z]+|[0-9]+")
# Two string literals joined by +, . (concatenation), , or nothing.
CONCAT = re.compile(r"[\"'`]\s*(?:\+|\.|,)?\s*[\"'`]")


def atf_hits(text):
    """Offsets of "atf" as a word or a word of an identifier."""
    out = []
    for run in RUN.finditer(text):
        word = run.group()
        if "atf" not in word.lower():
            continue
        if any(c.isdigit() for c in word) and not any(c.islower() for c in word):
            continue  # an ID of capitals and digits
        for tok in CAMEL.finditer(word):
            if tok.group().lower() == "atf":
                out.append(run.start() + tok.start())
    return out


def hits_in(text):
    """(offset, end, what) of every hit in text."""
    out = []
    for m in JOINED.finditer(text):
        out.append((m.start(), m.end(), "the host's name"))
    for m in SPACED.finditer(text):
        out.append((m.start(), m.end(), "the host's name"))
    for off in atf_hits(text):
        out.append((off, off + 3, "the header prefix"))
    return out


def joined_literals(text):
    """text with concatenated string literals joined, and for each of its
    characters the offset in text."""
    parts, index, pos = [], [], 0
    for m in CONCAT.finditer(text):
        parts.append(text[pos:m.start()])
        index.extend(range(pos, m.start()))
        pos = m.end()
    parts.append(text[pos:])
    index.extend(range(pos, len(text)))
    return "".join(parts), index


def decodings(data):
    """The texts a file may hold: UTF-8, or UTF-16/UTF-32 by its byte order
    mark, or every UTF-16/UTF-32 decoding when at least a third of its bytes
    are zero (text of 2 or 4 bytes per character); [] for other binary
    files."""
    boms = [(b"\xff\xfe\x00\x00", "utf-32-le"), (b"\x00\x00\xfe\xff", "utf-32-be"), (b"\xff\xfe", "utf-16-le"),
            (b"\xfe\xff", "utf-16-be")]
    for bom, enc in boms:
        if data.startswith(bom):
            return [data[len(bom):].decode(enc, "replace")]
    if b"\x00" not in data:
        return [data.decode("utf-8", "replace")]
    if data.count(b"\x00") * 3 >= len(data):  # text of 2 or 4 bytes per character
        return [data.decode(enc, "replace") for enc in ("utf-16-le", "utf-16-be", "utf-32-le", "utf-32-be")]
    return []  # binary


def scan_text(text):
    """Sorted line numbers (1-based) of the hits in text."""
    lines = text.split("\n")
    starts, n = [], 0
    for line in lines:
        starts.append(n)
        n += len(line) + 1

    def line_of(offset):
        lo, hi = 0, len(starts) - 1
        while lo < hi:
            mid = (lo + hi + 1) // 2
            if starts[mid] <= offset:
                lo = mid
            else:
                hi = mid - 1
        return lo

    found = set()
    joined, index = joined_literals(text)
    for source, to_text in ((text, None), (joined, index)):
        for start, end, _ in hits_in(source):
            if to_text is not None:
                start, end = to_text[start], to_text[max(end - 1, start)] + 1
            first, last = line_of(start), line_of(max(end - 1, start))
            if any(ALLOW_MARKER in lines[i] for i in range(first, last + 1)):
                continue
            found.add(first + 1)
    return sorted(found)


def scan(name, data):
    """The hits of one file: its path and its content."""
    out = []
    if scan_text(name):
        out.append("%s: in the path" % name)
    lines = set()
    for text in decodings(data):
        lines.update(scan_text(text))
    out.extend("%s:%d" % (name, n) for n in sorted(lines))
    return out


def main(argv):
    root = argv[1] if len(argv) > 1 else "."
    out = subprocess.run(["git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
                         capture_output=True, check=True).stdout
    hits = []
    for raw in out.split(b"\0"):
        if not raw:
            continue
        name = raw.decode("utf-8", "replace")
        if name.startswith(ALLOWED):
            continue
        try:
            with open(os.path.join(root, name), "rb") as f:
                data = f.read()
        except OSError:
            data = b""  # deleted in the working tree: the path is still checked
        hits.extend(scan(name, data))
    for h in hits:
        print(h)
    print("%d hit(s) outside %s" % (len(hits), ALLOWED))
    return 1 if hits else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
