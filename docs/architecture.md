# Architecture

[English](architecture.md) | [简体中文](zh-CN/architecture.md)

## 1. Scope

MFGraph is a project-level version graph service. Each workspace is a node in the graph; the service owns:

- workspace / workstream / branch / snapshot;
- content-addressed blobs, chunks, packs, manifests;
- locks, uploads, diffs, restores, optional merges;
- format handlers and industry profile templates.

It does not own:

- workflow state (external engine);
- identity provider (OIDC or host kernel);
- object storage server;
- generic document management, PLM, or AEC authoring.

## 2. Layers

```text
business:   tenant → project → project_node* → workspace
execution:  workstream 1:N branches → snapshot
process:    external workflow instance (optional)
```

- `project` is the business/accountability root.
- `project_node` is an optional, nestable decomposition (subproject/phase/building/episode/module/package).
- `workspace` is a versioned content root; it must belong to a project (and may belong to a project_node).
- `workstream` is an execution lane; it belongs to a workspace and has a default branch.
- `branch` is a version ref (`branch_id`, `ref_name`, `branch_name`), not a workstream.
- `snapshot` is an immutable manifest.

## 3. Components

```text
host / server
├─ kernel
│  ├─ identity / authz / audit
│  ├─ DB pool / migrations / leases
│  ├─ blob store registry
│  ├─ event log / outbox
│  └─ module registry
├─ mfgraph core
│  ├─ project tree / workspace / workstream
│  ├─ branch / snapshot / manifest
│  ├─ chunk / pack / upload / lock
│  └─ diff / restore
├─ mfgraph-merge (optional)
└─ mfgraph-workflow (optional; built-in HTTP adapter)
```

## 4. Module boundaries

- `mfgraph` core is one process/binary.
- `mfgraph-merge` is optional; without it, core supports fast-forward/replace.
- `mfgraph-workflow` contains the built-in HTTP adapter for external workflow engines; it does not import an engine SDK.
- Format handlers/preview workers are optional external processes.

## 5. Deployment modes

- embedded module in a host platform: the same binary in module mode ([module mode](module-mode.md));
- standalone `mfgraph-server`;
- single-node or HA;
- S3-compatible or POSIX/NFS storage.

## 6. Non-goals

Git protocol compatibility, binary auto-merge, full DMS/ECM, PLM/BOM, workflow engine, agent-only/Matrix-only runtime.
