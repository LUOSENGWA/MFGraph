#!/usr/bin/env python3
"""Check that relative Markdown links resolve to existing files.

Skips absolute URLs (http/https/mailto/ftp/tel), pure anchors and empty targets.
Exit code: 0 = all links resolve, 1 = broken links found.
"""
import argparse
import os
import re
import sys
import urllib.parse

LINK = re.compile(r"\[[^\]]*\]\(([^)]+)\)")
SKIP_DIRS = {".git", "node_modules", ".venv", "__pycache__"}


def check(root):
    broken = []
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames if d not in SKIP_DIRS]
        for name in filenames:
            if not name.endswith(".md"):
                continue
            path = os.path.join(dirpath, name)
            try:
                with open(path, encoding="utf-8", errors="ignore") as f:
                    for lineno, line in enumerate(f, 1):
                        for m in LINK.finditer(line):
                            target = m.group(1).strip()
                            if not target or target.startswith(
                                ("http://", "https://", "mailto:", "ftp:", "tel:", "#")
                            ):
                                continue
                            target = urllib.parse.unquote(
                                target.split("#")[0].split("?")[0]
                            )
                            if not target:
                                continue
                            resolved = os.path.normpath(os.path.join(dirpath, target))
                            if not os.path.exists(resolved):
                                broken.append((path, lineno, target))
            except OSError:
                continue
    return broken


def main():
    ap = argparse.ArgumentParser(description="Check relative Markdown links.")
    ap.add_argument("root", help="directory to scan")
    args = ap.parse_args()
    broken = check(args.root)
    for path, lineno, target in broken:
        print(f"{path}:{lineno}: {target}")
    print(f"\n{len(broken)} broken link(s)")
    return 1 if broken else 0


if __name__ == "__main__":
    sys.exit(main())
