# Events

MFGraph records an event for every change, in the same database transaction
as the change itself (the transactional outbox, table `outbox_events`): an
event exists exactly when its change was committed. The event kinds,
envelopes and IDs are the same in both deployment forms:

- **standalone:** the events stay in `outbox_events` (status `pending`); no
  publisher reads them yet;
- **module mode:** a forwarder in the server process copies them to the
  outbox table of the host platform, in the order described below (see
  [Module mode](module-mode.md#events)).

This document is the vocabulary of the events. A host platform can use it to
check that what a module emits is what it registered (a drift check): the
event kinds and `data` fields below are checked against the code by a test.

## Envelope

Every event is one JSON object:

```json
{
  "v": 1,
  "event_id": "24a8aaa9d8587be65a35434a315fa98a712eaf74b228393fccb9ccb282d6c433",
  "occurred_at": "2026-11-16T08:00:00.000000Z",
  "project_id": "01JD3X5Q8Z9W2V4T6R8P0N2P01",
  "actor": {"type": "human", "id": "manager"},
  "data": {"workspace_id": "01JD3X5Q8Z9W2V4T6R8P0N2W01", "main_branch_id": "01JD3X5Q8Z9W2V4T6R8P0N2B01", "storage_backend_id": "default"}
}
```

| Field | Meaning |
|---|---|
| `v` | Version of the envelope: `1`. Incompatible changes increment it. |
| `event_id` | 64 lowercase hex digits, derived from the content (see [Event IDs](#event-ids)): the same event always has the same ID. Consumers deduplicate by it. |
| `occurred_at` | When the change was made: RFC 3339, UTC, with microseconds. The events of one change have the same time. |
| `project_id` | The project of the change (a ULID). |
| `actor.type` | The type of the actor that made the change, as the host platform names it (for example `human` or `worker`); `null` in a standalone deployment. |
| `actor.id` | The actor reference of that actor, complete (at most 255 bytes of UTF-8). |
| `data` | The fields of the event kind, below. |

Events carry IDs, refs, hashes, counts and actor references; never file
contents, file paths, tokens or storage locations. An envelope has at most
256 KiB: a larger one is refused, never truncated (the change fails and is
rolled back). Real envelopes have a few KiB at most.

## Event IDs

```text
event_id = lowercase hex of sha256("mfgraph\n" + kind + "\n" + scope_id + "\n" + key + "\n" + version)
```

- `kind` is the event kind, for example `mfgraph.commit_created`;
- `scope_id` is the project ID;
- `key` is the snapshot ID for `mfgraph.commit_created` and
  `mfgraph.snapshot_restored`, the branch ID for `mfgraph.branch_created`
  and `mfgraph.branch_updated`, and the workspace ID for
  `mfgraph.workspace_created`;
- `version` is the new generation of the branch for the branch events and
  `1` for the others, in decimal.

A replayed request (the same `Idempotency-Key`) returns the stored response
and emits nothing, and a redelivered event keeps its ID. The ID of the
example above is that of `mfgraph.workspace_created` for project
`01JD3X5Q8Z9W2V4T6R8P0N2P01` and workspace `01JD3X5Q8Z9W2V4T6R8P0N2W01`.

## Vocabulary

| Event | Emitted when | `data` fields |
|---|---|---|
| `mfgraph.workspace_created` | a workspace is created; followed by `mfgraph.branch_created` for its main branch, in the same transaction | `workspace_id`, `main_branch_id`, `storage_backend_id` |
| `mfgraph.branch_created` | a branch is created (in this version: the main branch of a new workspace) | `workspace_id`, `branch_id`, `ref_name`, `generation` |
| `mfgraph.commit_created` | a commit creates a snapshot; followed by `mfgraph.branch_updated` with reason `commit` | `workspace_id`, `snapshot_id`, `branch_id`, `ref_name`, `root_tree_hash`, `parent_ids`, `trigger_kind`, `entry_count`, `total_bytes`, `on_behalf_of` |
| `mfgraph.snapshot_restored` | a restore creates a snapshot; followed by `mfgraph.branch_updated` with reason `restore` | `workspace_id`, `snapshot_id`, `branch_id`, `ref_name`, `root_tree_hash`, `parent_ids`, `trigger_kind`, `entry_count`, `total_bytes`, `on_behalf_of`, `restored_from` |
| `mfgraph.branch_updated` | a commit or a restore moved the head of a branch | `workspace_id`, `branch_id`, `ref_name`, `head_snapshot_id`, `previous_head_snapshot_id`, `generation`, `reason` |
| `mfgraph.snapshot_created` | registered, not emitted by this version: it is reserved for snapshots made explicitly (a checkpoint, or a snapshot taken before a restore). This version creates snapshots only by commits and restores, which have their own events, and a restore keeps the history, so it needs no snapshot before it. The version that adds explicit snapshots emits it and lists its fields here. | — |

This version therefore emits five kinds: `mfgraph.workspace_created`,
`mfgraph.branch_created`, `mfgraph.commit_created`,
`mfgraph.snapshot_restored` and `mfgraph.branch_updated`. Later versions add
kinds (for example for locks) together with the features, and list them
here first.

## Data fields

| Field | Type | Meaning |
|---|---|---|
| `workspace_id`, `snapshot_id`, `branch_id`, `main_branch_id`, `head_snapshot_id`, `restored_from` | ULID | IDs; `restored_from` is the snapshot a restore copied from. |
| `previous_head_snapshot_id` | ULID or `null` | The head before the change; `null` for the first commit of a branch. |
| `ref_name` | string | The ref of the branch, for example `main`. |
| `generation` | integer | The generation of the branch after the change (a new branch has 1; every commit and restore adds 1). |
| `reason` | string | `commit` or `restore`. |
| `root_tree_hash` | string | `sha256:` and 64 lowercase hex digits. |
| `parent_ids` | array of ULIDs | The parents of the snapshot (empty for the first one). |
| `trigger_kind` | string | `manual` (a commit) or `restore`. |
| `entry_count`, `total_bytes` | integer | Files of the snapshot and the sum of their sizes. |
| `storage_backend_id` | string | The storage backend of the workspace (never its storage location). |
| `on_behalf_of` | object | `{"type": ..., "id": ...}`: the actor the author acted for, when the host platform named one; absent otherwise. `type` is `null` when it was not given. |

## Order and delivery

- **Within one branch, events follow its generations.** The events of one
  transaction are emitted in the order of the table rows above
  (`workspace_created` before `branch_created`; the snapshot event before
  `branch_updated`) and are one microsecond apart in the outbox, and a
  change of a branch is recorded at least two microseconds after its
  previous change. Within a committed batch the forwarder orders by time and row ID.
- **Between branches (in this version: between workspaces), events follow
  the order in which their transactions completed,** not the time they
  record. Starting with 0.1.1 a transactional sequence records this order at commit.
  Previously pending rows drain first in their original timestamp order. A change that started earlier but committed later reaches the
  host table after a change that committed first. A consumer must therefore
  read the host's table in the order of its own row ID (the order of
  insertion) and must not use `created_at` (the time of the change) as a
  cursor: a later row can carry an earlier time.
- Delivery is at least once: an event may arrive again (after a restart, or
  when the forwarder could not record that it delivered it), always with the
  same `event_id`; the host's table deduplicates by a unique index on
  `event_id`.
- An event that can never be delivered stops the delivery of the module's
  later events until it is repaired or skipped by hand (see
  [Module mode](module-mode.md#a-stalled-forwarder)); a skipped event does
  not reach the host.
