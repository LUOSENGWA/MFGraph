#!/usr/bin/env python3
"""Check that the host platform's name and its header prefix appear only in
packaging/host-module/, the one directory of the repository allowed to name
the host (its manifest template, module.env, acceptance script and this
check).

Scans the paths and the contents of every file git knows (tracked, and
untracked but not ignored). Run from the repository root:

  python3 packaging/host-module/check_host_names.py

Prints file:line of every hit. Exit code: 0 = clean, 1 = hits.
"""
import re
import subprocess
import sys

ALLOWED = "packaging/host-module/"
# The host platform's name (also as in agentteams-service) and the prefix
# of its identity headers.
PATTERN = re.compile(r"agent[-_ ]?teams|x[-_]atf", re.IGNORECASE)


def main():
    out = subprocess.run(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
                         capture_output=True, check=True).stdout
    hits = []
    for raw in out.split(b"\0"):
        if not raw:
            continue
        name = raw.decode("utf-8", "replace")
        if name.startswith(ALLOWED):
            continue
        if PATTERN.search(name):
            hits.append("%s: in the path" % name)
        try:
            with open(name, "rb") as f:
                text = f.read().decode("utf-8", "replace")
        except OSError:
            continue  # deleted in the working tree
        for lineno, line in enumerate(text.splitlines(), 1):
            if PATTERN.search(line):
                hits.append("%s:%d" % (name, lineno))
    for h in hits:
        print(h)
    print("%d hit(s) outside %s" % (len(hits), ALLOWED))
    return 1 if hits else 0


if __name__ == "__main__":
    sys.exit(main())
