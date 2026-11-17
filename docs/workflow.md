# Workflow integration

[English](workflow.md) | [简体中文](zh-CN/workflow.md)

MFGraph is **workflow-enabled, not workflow-dependent**.

Every important operation has a command and an event. An optional adapter can bridge to an external workflow engine. Without an engine, REST/CLI/MCP still work.

## Commands

```text
workstream.create / archive
branch.create
snapshot.create / restore
upload.init / complete
lock.acquire / release
merge.preview / start / resolve / complete / abort
retention.hold
```

## Events

Every change records its events in the outbox of its own transaction, with this envelope (see [events](events.md) for the kinds, the `data` fields and how `event_id` is derived):

```json
{
  "v": 1,
  "event_id": "<64 lowercase hex digits, derived from the content>",
  "occurred_at": "2026-11-16T08:00:00.000000Z",
  "project_id": "01J...",
  "actor": {"type": "human", "id": "manager"},
  "data": {"workspace_id": "01J...", "snapshot_id": "01J...", "branch_id": "01J...", "ref_name": "main", "...": "..."}
}
```

Events carry IDs/status/refs only. Never file bytes, tokens, env vars, or absolute paths.

## Adapter SPI

```text
Publish(event) -> ack      # event delivery only; MFGraph never drives the engine
Health() -> ok
```

Built-in adapter:

- `http-workflow`: calls an external workflow engine over HTTP; no engine SDK import.

Optional:

- generic webhook (HMAC-SHA256, timestamp, event id, replay window);
- governance/evidence toolchain adapter;
- `null` adapter (no workflow).

## Delivery

- at-least-once; consumers deduplicate by event id;
- outbox retries with exponential backoff;
- dead-letter after max attempts;
- reconcile by `correlation_id`;
- a workflow failure never rolls back a successful MFGraph command.

## No dispatch

MFGraph does **not** assign or dispatch tasks. A `workstream` is only a version lane (active/archived); task assignment, dispatch and task status belong to the external task/workflow system. MFGraph emits domain events (e.g. `mfgraph.commit_created`, see [events](events.md)) and the workflow engine may write back `workflow_ref` (reference only).

## Security

- HMAC-SHA256 over `timestamp + "." + body`;
- 5-minute replay window;
- URL allowlist (no SSRF);
- no secrets in event payloads;
- file content is data, not instructions.
