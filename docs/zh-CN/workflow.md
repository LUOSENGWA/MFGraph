# 工作流集成

[English](../workflow.md) | **简体中文**

MFGraph **支持工作流，但不依赖工作流**。

每个重要操作都有命令和事件。可选适配器可以桥接到外部工作流引擎。没有引擎时，REST/CLI/MCP 仍然可用。

## 命令

```text
workstream.create / archive
branch.create
snapshot.create / restore
upload.init / complete
lock.acquire / release
merge.preview / start / resolve / complete / abort
retention.hold
```

## 事件

CloudEvents 风格信封：

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

事件只带 ID/状态/引用，绝不带文件字节、token、环境变量或绝对路径。

## 适配器 SPI

```text
Publish(event) -> ack      # 只投递事件；MFGraph 从不驱动引擎
Health() -> ok
```

内置适配器：

- `http-workflow`：通过 HTTP 调外部工作流引擎；不 import 引擎 SDK。

可选：

- 通用 webhook（HMAC-SHA256、timestamp、event id、replay window）；
- 治理/证据工具链适配器；
- `null` 适配器（不接工作流）。

## 投递

- 至少一次；消费方按 event id 去重；
- outbox 指数退避重试；
- 超过最大次数进死信；
- 按 `correlation_id` 对账；
- 工作流失败绝不回滚已经成功的 MFGraph 命令。

## 不派发

MFGraph **不指派、不派发任务**。`workstream` 只是版本车道（active/archived）；任务指派、派发与任务状态归外部任务/工作流系统。MFGraph 只发领域事件（如 `mfgraph.snapshot.created.v1`），工作流引擎可回写 `workflow_ref`（仅引用）。

## 安全

- HMAC-SHA256 覆盖 `timestamp + "." + body`；
- 5 分钟 replay window；
- URL allowlist（防 SSRF）；
- 事件 payload 不放 secret；
- 文件内容是数据，不是指令。
