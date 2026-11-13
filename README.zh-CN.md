# MFGraph

[English](README.md) | **简体中文**

**面向团队与 Agent 的项目级版本图谱。**

MFGraph 是一个设计阶段的自托管服务，用图谱方式对项目工作区做版本化。它把业务层级（`project` → `project_node`）、内容层（`workspace` → `snapshot`）、执行层（`workstream` → `branch`）和流程层（外部工作流引擎）分开。workspace 是项目图谱中的节点；项目级 commit 层（superproject，MF-D28）是 **v1 核心特性**。

> 状态：**设计/规格阶段**。本仓库暂无生产代码；这里定义的是待实现的契约。
>
> ⚠️ **快照声明（2026-11-12）**：公开仓是 **pre-M0 快照**，不是现行内部契约。部分文档仍描述已被取代的设计（如 workstream 的 `assign`/`dispatch` 已移除——MFGraph 不指派、不派发任务）。以内部设计集为准；M0 冻结后会重新同步。

## 为什么需要 MFGraph

多数工具只解决一个切片：

- 面向对象存储的 Git 式数据版本化；
- Git 的大文件指针；
- 带元数据与审批的文档管理；
- AEC/媒体/PLM 垂直系统；
- 工作流引擎。

MFGraph 关注的是这些能力的组合缺口：

1. **项目层级** — `project` 是业务根；`project_node` 可嵌套（阶段/楼栋/剧集/模块/包）；`workstream` 是执行车道。
2. **Workstream ≠ branch** — 一个 workstream 可以有多个 branch；`branch_id` / `ref_name` 是独立的版本引用。
3. **二进制/大文件是一等公民** — 内容寻址 chunk、packfile、manifest、独占锁；不做不安全的自动合并。
4. **格式与行业模板** — 文本/Office/PDF/图片/音视频/CAD/BIM/数据集/归档/DICOM 以模板配置，不硬编码垂直分支。
5. **工作流可选** — 内置 HTTP 适配器可连接外部工作流引擎；没有引擎时，REST/CLI/MCP 仍可用。
6. **人与 Agent 中立** — 统一 `principal_uid` 身份；不依赖特定 Agent 运行时或 Matrix。

## 它不是什么

- 不是 Git 服务器，也不实现 Git 兼容协议；
- 不是完整的文档管理/ECM 系统；
- 不是 PLM/BOM 系统；
- 不是工作流引擎；
- 不是只服务 Agent 或只依赖 Matrix 的产品。

## 架构速览

```text
tenant
└─ project
   └─ project_node*        (可选：子项目/阶段/楼栋/剧集/模块)
      └─ workspace         (版本化内容根)
         └─ workstream 1:N branches
            └─ snapshot     (不可变 manifest)
workflow (外部，可选)
```

## 存储与数据库

- **数据库：** v1 采用 MySQL 兼容方言（MySQL 8.0+、OceanBase MySQL、seekdb 5.7 子集）。只存状态与索引，不把文件字节存进数据库。
- **对象存储：** S3 兼容（AWS S3、RustFS、MinIO、Ceph RGW、R2、B2、OSS S3 模式、IBM COS、Oracle Object Storage S3）与 POSIX/NFS。Azure Blob 与 GCS 适配器为后续计划。
- **二进制/大文件：** chunk + packfile + manifest；二进制内容使用锁并发控制。

## 文档

- [架构](docs/zh-CN/architecture.md)
- [数据模型](docs/zh-CN/data-model.md)
- [存储](docs/zh-CN/storage.md)
- [API](docs/zh-CN/api.md)
- [工作流集成](docs/zh-CN/workflow.md)
- [安全](docs/zh-CN/security.md)
- [部署](docs/zh-CN/deployment.md)
- [路线图](docs/zh-CN/roadmap.md)
- [FAQ](docs/zh-CN/faq.md)

## 状态与许可

本仓库是**设计/规格仓库**。代码将按照 `docs/` 中的契约实现。

许可：**Apache-2.0**（见 [LICENSE](LICENSE)）。

## 开发

前置条件：Go（版本见 `go.mod`）、`make`、Python 3（仅标准库，用于文档与测试向量脚本）。**只有** MySQL 相关目标需要 Docker，其余都不需要。

```bash
make build        # 编译全部包；二进制输出到 ./bin
make test         # 单元测试（不需要数据库和网络）
make vet          # go vet
make fmt-check    # gofmt 检查
make openapi-check  # 校验 contracts/openapi.yaml（kin-openapi）及其约定
make generate     # 由契约重新生成 internal/api/gen（需要网络）
make run          # 用开发 token 在 127.0.0.1:8080 启动 mfgraph-server
```

服务端用 `MFGRAPH_*` 环境变量配置：`MFGRAPH_HTTP_ADDR`、`MFGRAPH_DB_DSN`（MySQL DSN，本版本可不配）、`MFGRAPH_MIGRATE_ON_START`、`MFGRAPH_KERNEL_MIGRATIONS`、`MFGRAPH_AUTH_TOKENS`（`token=PRINCIPAL_ULID,...`）、`MFGRAPH_LOG_LEVEL`、`MFGRAPH_LOG_FORMAT`、`MFGRAPH_SHUTDOWN_TIMEOUT`。`mfgraph-server migrate` 执行待执行的迁移后退出。本版本只实现了 `/healthz` 和 `/readyz`；[`contracts/openapi.yaml`](contracts/openapi.yaml) 中的其他接口都返回 `501 not_implemented`。

开发用 MySQL 8.0 通过 Docker 运行（`deploy/docker-compose.dev.yml`，只绑定 `127.0.0.1`，一次性本地口令）：

```bash
make dev-db-up    # 启动 MySQL 8.0 并等待就绪（需要 Docker）
make dev-db-down  # 停止（保留数据）；make dev-db-reset 会同时删除数据卷
make test-mysql   # 在其上跑迁移测试：空库执行、重跑跳过、篡改报错
```

## 贡献

见 [CONTRIBUTING.zh-CN.md](CONTRIBUTING.zh-CN.md)（[English](CONTRIBUTING.md)）。安全问题见 [SECURITY.zh-CN.md](SECURITY.zh-CN.md)（[English](SECURITY.md)）。行为准则见 [CODE_OF_CONDUCT.zh-CN.md](CODE_OF_CONDUCT.zh-CN.md)（[English](CODE_OF_CONDUCT.md)）。
