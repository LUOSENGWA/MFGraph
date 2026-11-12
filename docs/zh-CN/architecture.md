# 架构

[English](../architecture.md) | **简体中文**

## 1. 范围

MFGraph 是项目级版本图谱服务。workspace 是图谱中的节点；它负责：

- workspace / workstream / branch / snapshot；
- 内容寻址 blob、chunk、pack、manifest；
- lock、upload、diff、restore、可选 merge；
- 格式处理器与行业模板。

它不负责：

- 工作流状态（外部引擎）；
- 身份提供方（OIDC 或宿主内核）；
- 对象存储服务本身；
- 通用文档管理、PLM 或 AEC 创作工具。

## 2. 分层

```text
业务层:   tenant → project → project_node* → workspace
执行层:   workstream 1:N branches → snapshot
流程层:   外部工作流实例（可选）
```

- `project` 是业务/责任根。
- `project_node` 是可选的、可嵌套的分解（子项目/阶段/楼栋/剧集/模块/包）。
- `workspace` 是版本化内容根；必须属于一个 project（可选属于某个 project_node）。
- `workstream` 是执行车道；属于 workspace，并有默认 branch。
- `branch` 是版本引用（`branch_id`、`ref_name`、`branch_name`），不是 workstream。
- `snapshot` 是不可变 manifest。

## 3. 组件

```text
宿主 / 服务端
├─ kernel
│  ├─ 身份 / 授权 / 审计
│  ├─ DB 连接池 / 迁移 / 租约
│  ├─ blob store 注册表
│  ├─ 事件日志 / outbox
│  └─ 模块注册表
├─ mfgraph core
│  ├─ 项目树 / workspace / workstream
│  ├─ branch / snapshot / manifest
│  ├─ chunk / pack / upload / lock
│  └─ diff / restore
├─ mfgraph-merge（可选）
└─ mfgraph-workflow（可选；内置 HTTP 适配器）
```

## 4. 模块边界

- `mfgraph` core 是单进程/单二进制。
- `mfgraph-merge` 可选；没有它时 core 支持 fast-forward/replace。
- `mfgraph-workflow` 包含面向外部工作流引擎的内置 HTTP 适配器；不 import 引擎 SDK。
- 格式处理器/预览 worker 是可选的外部进程。

## 5. 部署形态

- 作为宿主平台内的嵌入式模块；
- 独立 `mfgraph-server`；
- 单机或高可用；
- S3 兼容或 POSIX/NFS 存储。

## 6. 非目标

Git 协议兼容、二进制自动合并、完整 DMS/ECM、PLM/BOM、工作流引擎、只服务 Agent 或只依赖 Matrix 的运行时。
