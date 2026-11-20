# Storage

[English](storage.md) | [简体中文](zh-CN/storage.md)

## BlobStore interface

```text
Put / PutIfAbsent / Get / GetRange / Stat / Delete / List / Copy
MultipartInit / MultipartPutPart / MultipartComplete / MultipartAbort
PresignPut / PresignGet
Capabilities
```

The first core slice implements `PutIfAbsent`, `PutStream`, `Get` (with ranges), `Stat` and `Delete` (`internal/ports`), in memory for tests and on a POSIX file system (`internal/blob/posix`):

- an object is the file `<root>/<storage prefix>/<key>`; keys with empty, `.` or `..` segments, backslashes or NUL bytes are refused;
- a put writes a temporary file on the same file system (`.tmp-*.part`, created with `O_EXCL`), fsyncs it, renames it to its final name and fsyncs the directory (and the parents of new directories) before it returns. A put that finds the object, or a directory on its way, already there (possibly written a moment ago by a concurrent put) fsyncs the object's directory, and the parent of every directory the process has not handled before, before it returns too;
- `PutStream` hashes an upload while it writes it; the key follows from the hash, so the bytes are written once and renamed, and memory use does not grow with the size; a size limit or a checksum mismatch stores nothing;
- objects are content-addressed: an existing object of the expected size is kept; one of another size, or one that cannot be examined, is treated as damaged and replaced atomically, with a warning in the log (a background audit of full hashes comes later). A download checks the size of the stored object before it starts: a missing or damaged object gives `410 blob_missing` and a warning, never a truncated response.

Tree nodes go through the tree store (`internal/treestore`), which keeps the zstd-compressed canonical bytes in the same blob store under `tree/<hh>/<hex>.json.zst`.

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
<storage_prefix>/                   # default: tenants/<tenant_id>/projects/<project_id>/workspaces/<workspace_id>
  objects/<hh>/<sha256>             # whole-file content (frozen)
  tree/<hh>/<sha256>.json.zst       # tree nodes: zstd of the canonical bytes (frozen)
  packs/<pack_id>.pack              # later slice
  uploads/<upload_id>/              # later slice
  derived/<source_hash>/<kind>/
  trash/<yyyy>/<mm>/
```

The storage prefix is fixed when the workspace is created; the database stores keys relative to it. The key rules are specified in the [content format specification](spec/content-format.md#4-storage-keys).

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
- commits carry `expected_generation`; a commit based on an outdated branch head returns 409 `conflict` — retries never silently overwrite.

## Garbage collection

- v1 **never deletes committed snapshots**: every committed snapshot is a root, so GC only reclaims orphans, staged objects and trash;
- two phases: **mark (DB) → sweep (object store)** with a 24h grace period; objects younger than the grace window are never deleted;
- bucket versioning is enabled by default: the sweeper deletes **specific version ids** (a plain `DELETE` only adds a delete marker); the grace window doubles as an undelete window; noncurrent versions expire after 30 days;
- GC never crosses workspaces; `gc_state` (one row per workspace) holds the watermark and the mutex;
- packs are not compressed in v1; range reads map directly to `offset/length`.

## S3 in a standalone deployment

Set `MFGRAPH_BLOB_BACKEND=s3`, `MFGRAPH_S3_ENDPOINT` (an HTTP or HTTPS
origin), and `MFGRAPH_S3_BUCKET` (an existing bucket). Supply
`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, optionally `AWS_SESSION_TOKEN`
and `AWS_REGION`. The account needs bucket listing and object read, write,
copy, multipart and removal privileges. A readiness probe creates, reads
and removes its own small object. Whole-file uploads and downloads go through the server. Standalone multipart
sessions issue bounded S3 PUT URLs for individual parts. The endpoint and bucket identify the workspace's backend.
Changing that identity requires migrating the data separately.

When the backend reports that native multipart is unsupported, clients upload
parts through the server. It verifies the length and SHA-256 on local scratch
disk before issuing one PUT. Allow scratch space for each concurrent whole
object or pack. The backend's single-PUT size limit applies to each object.
For version-aware storage, the account needs permission to list and remove
object versions so a terminal session can reclaim its private bytes.

### Fixed chunks and packs

Standalone packed writes require **both** `MFGRAPH_CHUNK_BYTES` (4–16 MiB)
and `MFGRAPH_PACK_BYTES` (64–256 MiB, a multiple of the chunk size). Both
settings are absent by default. Known-length uploads smaller than a chunk
use whole-file staging. A commit that references two or more new small files
groups them into immutable packs. Blobs already stored as whole files keep their
identity and remain readable. Packed files also remain readable when these
settings are removed. The hash and ETag are the SHA-256 of the original file.
Ranges read and verify the participating chunks and can cross pack boundaries.

### Request limits

Operators can set `MFGRAPH_UPLOAD_CONCURRENCY` for whole-file uploads,
relayed parts and session completion. `MFGRAPH_TREE_CONCURRENCY` independently
bounds tree listings, diffs, commits and restores. Both take positive integer
limits per server process. A full pool returns HTTP 429 with `Retry-After`.
`MFGRAPH_UPLOAD_TIMEOUT` sets the duration available to receive a streamed
request body, for example `10m`. An overdue body returns HTTP 408. These
settings are opt-in. Health checks and lightweight metadata use neither pool.
