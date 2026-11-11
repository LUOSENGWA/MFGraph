# Security Policy

[English](SECURITY.md) | [简体中文](SECURITY.zh-CN.md)

## Reporting a vulnerability

Please use GitHub's **private vulnerability reporting** for this repository. Do not open a public issue for a vulnerability.

If private reporting is unavailable, contact the maintainers through the repository's security contact configured in GitHub.

Include:

- affected version/commit;
- reproduction steps;
- impact;
- any suggested fix.

## Scope

Security-relevant areas include:

- authentication and authorization bypass;
- tenant/project isolation;
- path traversal and symlink handling;
- presigned URL/S3 credential scope;
- webhook signature/replay/SSRF;
- object-store or database credential leakage;
- denial of service via uploads/diffs;
- supply-chain/license issues.

## Response

Maintainers will acknowledge, triage, and coordinate a fix and disclosure. Do not disclose publicly before a fix is available.
