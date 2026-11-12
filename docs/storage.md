# Storage

[English](storage.md) | [简体中文](zh-CN/storage.md)

## BlobStore interface

```text
Put / PutIfAbsent / Get / GetRange / Stat / Delete / List / Copy
MultipartInit / MultipartPutPart / MultipartComplete / MultipartAbort
PresignPut / PresignGet
Capabilities
```

## Adapter tiers

| Adapter | v1 | Notes |
|---|---|---|
| S3-compatible (AWS S3, RustFS, MinIO, Ceph RGW, R2, B2, OSS S3 mode, IBM COS, Oracle S3) | ✅ first-class | multipart, presign, range; capability probe |
| POSIX (local/NFS) | ✅ first-class | temp file + atomic rename; no multipart; DB lease for locks |
| Azure Blob | planned | native block blob/SAS; do not force S3 gateway |
| GCS | planned | native resumable upload/signed URL |
| WebDAV | optional | no standard multipart |
| HDFS / CIFS/SMB | not first-class | semantics/locking differ |

## RustFS vs POSIX

| Dimension | RustFS (S3-compatible) | POSIX (local/NFS) |
|---|---|---|
| API | S3 REST | file path |
| Large files | multipart, presign, range | temp file/merge, pread |
| Atomicity | object PUT; no directory rename | same-directory rename |
| Directories | flat key prefixes | real directories/inodes |
| Locks | no file locks; DB lease | local flock; NFS locks unreliable |
| Scale | multi-node/HA/cloud | single node; shared FS for HA |
| Fit | shared/HA/K8s | single-node/air-gap/dev |

## Content layout

```text
tenants/<tenant_id>/
  projects/<project_id>/
    nodes/<scope_node_id>/          # optional
      workspaces/<workspace_id>/
        packs/<pack_id>.pack
        manifests/<snapshot_id>.json.zst
        uploads/<upload_id>/
        derived/<source_hash>/<kind>/
        trash/<yyyy>/<mm>/
```

## Commit protocol

1. write content objects (immutable; whole-file `sha256`);
2. write pack files (optional);
3. write tree nodes (hash-addressed; the root node **is** the manifest);
4. DB transaction: snapshot (`root_tree_hash`) + parents + branch CAS + outbox — no bulk `snapshot_entries` write;
5. orphan objects are GC'd (see GC below).

There is no distributed transaction across DB and object storage. A commit is a **file/version correlation point**; workflow refs are observed, not transactional.

## Locks

- `exclusive` for binary/large files; `shared` for read-only inputs; shared locks may coexist, exclusive ones are mutually exclusive (helper table `lock_exclusive_active`);
- TTL + heartbeat + fencing token; the holder capability is the **lock_id**, not the principal;
- lease expiry is compared against **DB time** (`NOW(6)`), and lock acquire/takeover commit share the `workspace_fencing` row lock (serialization point);
- expired writer commits get 409; break-glass is admin-only and audited;
- commit checks `base_head`: concurrent edits to the same path return 409 `path_conflict` — retries never silently overwrite.
