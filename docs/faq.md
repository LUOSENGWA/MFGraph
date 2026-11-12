# FAQ

[English](faq.md) | [简体中文](zh-CN/faq.md)

## Is MFGraph a Git server?

No. It borrows content-addressed snapshots/branches but does not implement Git protocol, `git clone/push`, rebase, or cherry-pick.

## Is it a DMS/ECM?

No. It is a versioning substrate. Metadata, approvals, and retention policies can be layered on top; MFGraph does not aim to replace M-Files/Alfresco/Nuxeo.

## Does it require an agent runtime or Matrix?

No. Humans, agents, and services are all principals. Matrix and MCP are optional adapters.

## Does it require a workflow engine?

No. It emits events and exposes commands. A workflow adapter is optional. Without it, REST/CLI/MCP still work.

## Can it handle large binary files?

Yes. Large files use content-addressed chunks and packfiles; binary content uses exclusive locks rather than auto-merge.

## What databases are supported?

v1: MySQL-compatible (MySQL 8.0+, OceanBase MySQL, seekdb 5.7 subset). PostgreSQL is a later adapter, not a v1 gate.

## What object stores are supported?

v1 first-class: S3-compatible and POSIX/NFS. Azure Blob and GCS are planned native adapters. WebDAV/HDFS/CIFS are not first-class.

## Why not Git LFS?

Git LFS solves large files inside Git. MFGraph targets general project workspaces, non-code teams, binary locks, format handlers, and optional workflow integration.

## Why not Rust?

Go is the primary language for service/deployment/ecosystem fit. Rust is an option for a CPU-heavy chunk/pack sidecar if benchmarks require it, not for the core.

## License?

Apache-2.0. Dependencies must be MIT/BSD/Apache/ISC; GPL/AGPL/LGPL/SSPL/BUSL are not accepted in core.
