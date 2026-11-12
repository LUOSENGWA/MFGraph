# Roadmap

[English](roadmap.md) | [简体中文](zh-CN/roadmap.md)

## M0 — contracts

- schema, BlobStore SPI, API/OpenAPI, event schema, FormatHandler SPI, identity/authz.
- gate: all contracts reviewed; no destructive change after freeze.

## M1 — core

- workspace/workstream/branch/snapshot;
- chunk/pack/upload/lock;
- diff/restore/timeline/GC/quota;
- MySQL + S3-compatible/POSIX;
- gate: 1 GiB upload, no half snapshot, concurrent CAS, lock fencing.

## M2 — integrations

- optional `mfgraph-merge` (text 3-way, binary lock-only);
- built-in HTTP workflow adapter;
- optional webhook/governance adapter;
- gate: workflow e2e; no-engine path still works.

## M3 — formats

- text, image, PDF, audio/video, dataset, archive, generic binary;
- preview/proxy extraction;
- CAD/BIM/DICOM/PSD via plugins.

## M4 — scale

- pack compaction, GC, multi-tenant quotas, backup/restore, HA;
- Azure Blob/GCS adapters;
- PostgreSQL adapter (later, not v1 gate).

## Non-goals for v1

Git protocol, full binary merge, full DMS/ECM, PLM/BOM, workflow engine, agent-only/Matrix-only runtime.
