#!/usr/bin/env python3
"""Check Go dependencies against the allowlist and their license files.

Every module that is compiled into a binary or a test (including the e2e
tests) must be listed below with its expected license family, and its
LICENSE file must match. Copyleft licenses (GPL, LGPL, AGPL, SSPL, BUSL) are
always rejected; MPL-2.0 is accepted only for the modules listed with it
(used as unmodified dependencies). Run from the repository root:

  python3 scripts/check_deps.py

Exit code: 0 = all dependencies allowed, 1 = violations.
"""
import os
import re
import subprocess
import sys

# module path -> (expected license families, how it is used)
ALLOWED = {
    "golang.org/x/text": ({"BSD"}, "runtime: Unicode NFC and case folding"),
    "github.com/klauspost/compress": ({"BSD"}, "runtime: zstd for stored tree nodes"),
    "github.com/oklog/ulid/v2": ({"Apache-2.0"}, "runtime: ULIDs"),
    "github.com/go-sql-driver/mysql": ({"MPL-2.0"}, "runtime: MySQL driver (MPL-2.0 exception: used unmodified)"),
    "filippo.io/edwards25519": ({"BSD"}, "runtime: dependency of the MySQL driver"),
    "github.com/oapi-codegen/runtime": ({"Apache-2.0"}, "runtime: support code of the generated server"),
    "github.com/apapsch/go-jsonmerge/v2": ({"MIT"}, "runtime: dependency of oapi-codegen/runtime"),
    "github.com/google/uuid": ({"BSD"}, "runtime: dependency of oapi-codegen/runtime"),
    "github.com/getkin/kin-openapi": ({"MIT"}, "tests: OpenAPI validation"),
    "github.com/go-openapi/jsonpointer": ({"Apache-2.0"}, "tests: dependency of kin-openapi"),
    "github.com/go-openapi/swag/jsonname": ({"Apache-2.0"}, "tests: dependency of kin-openapi"),
    "github.com/santhosh-tekuri/jsonschema/v6": ({"Apache-2.0"}, "tests: dependency of kin-openapi"),
    "go.yaml.in/yaml/v4": ({"Apache-2.0"}, "tests: dependency of kin-openapi (parts MIT, see its NOTICE)"),
}

# Headings of the license texts themselves. (MPL-2.0 mentions the GPL family as
# "Secondary Licenses" in running text; that must not count.)
COPYLEFT = [
    ("GPL/LGPL/AGPL", re.compile(r"^\s*GNU (AFFERO |LESSER |LIBRARY )?GENERAL PUBLIC LICENSE\s*$", re.M)),
    ("SSPL", re.compile(r"^\s*Server Side Public License", re.M | re.I)),
    ("BUSL", re.compile(r"^\s*Business Source License", re.M | re.I)),
    ("Commons Clause", re.compile(r"Commons Clause", re.I)),
]
FAMILIES = [
    ("Apache-2.0", re.compile(r"Apache License,?\s+Version 2\.0", re.I)),
    ("MIT", re.compile(r"Permission is hereby granted, free of charge", re.I)),
    ("BSD", re.compile(r"Redistribution and use in source and binary forms", re.I)),
    ("MPL-2.0", re.compile(r"Mozilla Public License,?\s+(Version|v\.)\s*2\.0", re.I)),
]
LICENSE_FILE = re.compile(r"^(licen[cs]e|copying)(\.(md|txt))?$", re.I)


def used_modules():
    out = subprocess.run(
        ["go", "list", "-deps", "-test", "-tags", "e2e",
         "-f", "{{with .Module}}{{if not .Main}}{{.Path}}|{{.Version}}|{{.Dir}}{{end}}{{end}}", "./..."],
        check=True, capture_output=True, text=True).stdout
    mods = {}
    for line in out.splitlines():
        if line:
            path, version, d = line.split("|", 2)
            mods[path] = (version, d)
    return mods


def families(text):
    return {name for name, rx in FAMILIES if rx.search(text)}


def main():
    problems = []
    mods = used_modules()
    for path in sorted(mods):
        version, d = mods[path]
        if path not in ALLOWED:
            problems.append("%s %s: not on the allowlist" % (path, version))
            continue
        want, use = ALLOWED[path]
        files = [f for f in os.listdir(d) if LICENSE_FILE.match(f)] if d and os.path.isdir(d) else []
        if not files:
            problems.append("%s %s: no license file found" % (path, version))
            continue
        text = "\n".join(open(os.path.join(d, f), encoding="utf-8", errors="replace").read() for f in files)
        for name, rx in COPYLEFT:
            if rx.search(text):
                problems.append("%s %s: %s license text found" % (path, version, name))
        got = families(text)
        if not want <= got:
            problems.append("%s %s: license looks like %s, allowlist says %s" % (
                path, version, sorted(got) or "unknown", sorted(want)))
        if "MPL-2.0" in got and "MPL-2.0" not in want:
            problems.append("%s %s: MPL-2.0 is only allowed by explicit exception" % (path, version))
        print("%-42s %-10s %-22s %s" % (path, version, "+".join(sorted(got)), use))
    stale = sorted(set(ALLOWED) - set(mods))
    for path in stale:
        print("note: allowlisted but unused: %s" % path)
    for p in problems:
        print("FAIL:", p)
    print("%d module(s) checked, %d problem(s)" % (len(mods), len(problems)))
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
