#!/usr/bin/env python3
"""Scan files for likely secrets / sensitive markers.

Prints only file:line and hit category (never the matched value).
Exit code: 0 = clean, 1 = hits.
"""
import argparse
import os
import re
import sys

PATTERNS = [
    ("private_key", re.compile(r"-----BEGIN (?:RSA |EC |OPENSSH |PGP )?PRIVATE KEY-----")),
    ("aws_access_key_id", re.compile(r"\bAKIA[0-9A-Z]{16}\b")),
    ("aws_secret", re.compile(r"(?i)aws_secret_access_key\s*[:=]\s*\S{20,}")),
    ("github_token", re.compile(r"\bgh[pousr]_[A-Za-z0-9]{36,}\b")),
    ("slack_token", re.compile(r"\bxox[abprs]-[A-Za-z0-9-]{10,}\b")),
    ("openai_key", re.compile(r"\bsk-[A-Za-z0-9]{20,}\b")),
    ("google_api_key", re.compile(r"\bAIza[0-9A-Za-z_-]{35}\b")),
    ("credential_assignment", re.compile(r"(?i)\b(password|passwd|secret|api_key|apikey|access_token)\s*[:=]\s*['\"]?[A-Za-z0-9_\-/+]{8,}")),
    ("internal_ip", re.compile(r"\b(?:10\.\d{1,3}|172\.(?:1[6-9]|2\d|3[01])|192\.168)\.\d{1,3}\.\d{1,3}\b")),
    ("email", re.compile(r"\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b")),
]
ALLOW_EMAIL_DOMAINS = ("example.com", "example.org", "example.net")
SKIP_DIRS = {".git", "node_modules", "vendor", ".venv", "__pycache__", "dist", "build"}
TEXT_EXT = {".md", ".txt", ".yml", ".yaml", ".json", ".toml", ".ini", ".cfg", ".env",
            ".sh", ".py", ".go", ".rs", ".ts", ".js", ".sql", ".xml", ".gitignore"}


def scan(root):
    hits = []
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames if d not in SKIP_DIRS]
        for name in filenames:
            ext = os.path.splitext(name)[1].lower()
            if name not in (".gitignore", ".dockerignore") and ext not in TEXT_EXT:
                continue
            path = os.path.join(dirpath, name)
            try:
                with open(path, encoding="utf-8", errors="ignore") as f:
                    for lineno, line in enumerate(f, 1):
                        for label, rx in PATTERNS:
                            m = rx.search(line)
                            if not m:
                                continue
                            if label == "email" and any(
                                d in m.group(0).lower() for d in ALLOW_EMAIL_DOMAINS
                            ):
                                continue
                            hits.append((path, lineno, label))
            except OSError:
                continue
    return hits


def main():
    ap = argparse.ArgumentParser(description="Scan for likely secrets (no values printed).")
    ap.add_argument("root", help="directory to scan")
    args = ap.parse_args()
    hits = scan(args.root)
    for path, lineno, label in hits:
        print(f"{path}:{lineno}: {label}")
    print(f"\n{len(hits)} hit(s)")
    return 1 if hits else 0


if __name__ == "__main__":
    sys.exit(main())
