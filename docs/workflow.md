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

CloudEvents-style envelope:

```json
{
  "specversion": "1.0",
  "id": "01J...",
  "source": "mfgraph://tenant/t1/project/p1",
  "type": "mfgraph.snapshot.created.v1",
  "subject": "workstreams/01J...",
  "time": "2026-11-11T12:00:00Z",
  "data": {
    "tenant_id": "t1",
    "project_id": "p1",
    "scope_node_id": "n1",
    "workstream_id": "w1",
    "correlation_id": "01J..."
  }
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

MFGraph does **not** assign or dispatch tasks. A `workstream` is only a version lane (active/archived); task assignment, dispatch and task status belong to the external task/workflow system. MFGraph emits domain events (e.g. `mfgraph.snapshot.created.v1`) and the workflow engine may write back `workflow_ref` (reference only).

## Security

- HMAC-SHA256 over `timestamp + "." + body`;
- 5-minute replay window;
- URL allowlist (no SSRF);
- no secrets in event payloads;
- file content is data, not instructions.
