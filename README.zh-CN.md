# MFGraph

[English](README.md) | **简体中文**

**面向团队与 Agent 的项目级版本图谱。**

MFGraph 是一个处于早期开发阶段的自托管服务，用图谱方式对项目工作区做版本化。它把业务层级（`project` → `project_node`）、内容层（`workspace` → `snapshot`）、执行层（`workstream` → `branch`）和流程层（外部工作流引擎）分开。workspace 是项目图谱中的节点；项目级 commit 层（superproject）是 **v1 核心特性**。

> 状态：**早期开发（M1.0a）**。第一个核心切片已经可用：通过 REST 和薄 CLI 完成项目、带 `main` 分支的工作区、整文件上传、提交、浏览、下载、diff 和恢复；数据库用 MySQL 8.0 或 seekdb，对象存放在本地（POSIX）文件系统。尚未支持的能力（分片上传、S3、锁、GC、配额、角色、更多分支）见[路线图](docs/zh-CN/roadmap.md)。
>
> 说明：[`docs/spec/`](docs/spec/zh-CN/content-format.md) 中的字节格式、迁移脚本和 OpenAPI 契约具有约束力。其他文档描述规划中的 v1 设计；两者不一致时以契约为准。MFGraph 不指派、不派发任务。

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
6. **人与 Agent 中立** — 每个调用者都是一个操作者，用身份引用记录（宿主平台的身份字符串，独立部署时是 ULID）；不依赖特定 Agent 运行时或 Matrix。

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

- **数据库：** v1 采用 MySQL 兼容方言（MySQL 8.0+、OceanBase MySQL、seekdb 5.7 子集）；CI 在 MySQL 8.0 和 seekdb 上测试迁移。只存状态与索引，不把文件字节存进数据库。
- **对象存储：** S3 兼容（AWS S3、RustFS、MinIO、Ceph RGW、R2、B2、OSS S3 模式、IBM COS、Oracle Object Storage S3）与 POSIX/NFS。Azure Blob 与 GCS 适配器为后续计划。
- **二进制/大文件：** chunk + packfile + manifest；二进制内容使用锁并发控制。

## 文档

- [内容格式规范](docs/spec/zh-CN/content-format.md)
- [HTTP API 契约（OpenAPI）](contracts/openapi.yaml)
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

代码遵循 `docs/spec/`、`contracts/` 和 `migrations/` 中的契约；格式的测试向量在 `testdata/vectors/`。

许可：**Apache-2.0**（见 [LICENSE](LICENSE)）。

## 快速上手

需要 Go（版本见 `go.mod`）、`make` 和 Docker（用于 MySQL）。

```bash
make build                      # ./bin/mfgraph-server 与 ./bin/mfgraph
make dev-db-up                  # MySQL 8.0，127.0.0.1:3306（一次性开发凭据）
export MFGRAPH_DB_DSN='root:mfgraph@tcp(127.0.0.1:3306)/mfgraph'
./bin/mfgraph-server migrate    # 建表
mkdir -p data/objects
MFGRAPH_STORAGE_ROOT=data/objects MFGRAPH_AUTH_TOKENS='dev-token-change-me-0000=alice' ./bin/mfgraph-server
```

服务监听 `127.0.0.1:8094`（`make run` 做同样的事）。在另一个终端里：

```bash
export MFGRAPH_TOKEN=dev-token-change-me-0000       # MFGRAPH_URL 默认为 http://127.0.0.1:8094
P=$(./bin/mfgraph project create demo)
WS=$(./bin/mfgraph workspace create "$P")         # <workspace_id> TAB main_branch=<branch_id>
W=${WS%%$'\t'*}; B=${WS##*=}
echo 'hello' > hello.txt && echo 'draft' > plan.md
S1=$(./bin/mfgraph commit "$W" -m 'first' hello.txt=hello.txt docs/plan.md=plan.md)
echo 'final' > plan.md
S2=$(./bin/mfgraph commit "$W" -m 'second' docs/plan.md=plan.md)
./bin/mfgraph log "$W"                  # 两个快照，新的在前
./bin/mfgraph ls "$S2" docs             # 列出快照中的一个目录
./bin/mfgraph cat "$S1" docs/plan.md    # draft
./bin/mfgraph diff "$S2"                # modified docs/plan.md
S3=$(./bin/mfgraph restore "$S1" --branch "$B")   # 生成一个树等于 S1 的新快照
./bin/mfgraph cat "$S3" docs/plan.md    # 又是 draft
```

这些操作都是普通的 REST 调用：见 [API](docs/zh-CN/api.md) 和[契约](contracts/openapi.yaml)。配置项（Unix domain socket、API 前缀、各种上限）见[部署](docs/zh-CN/deployment.md)。

## 开发

前置条件：Go（版本见 `go.mod`）、`make`、Python 3（仅标准库，用于文档与测试向量脚本）。**只有**数据库相关目标（MySQL 8.0 与 seekdb）需要 Docker，其余都不需要。

```bash
make build        # 编译全部包；二进制输出到 ./bin
make test         # 单元测试（不需要数据库和网络）
make vet          # go vet
make fmt-check    # gofmt 检查
make openapi-check  # 校验 contracts/openapi.yaml（kin-openapi）及其约定
make generate     # 由契约重新生成 internal/api/gen（需要网络）
make run          # 用开发 token 和开发用 MySQL 在 127.0.0.1:8094 启动 mfgraph-server
```

服务端用 `MFGRAPH_*` 环境变量配置（完整表格见[部署](docs/zh-CN/deployment.md)）：`MFGRAPH_HTTP_ADDR`（`host:port` 或 `unix:<path>`）、`MFGRAPH_API_PREFIX`、`MFGRAPH_DB_DSN`（服务必须连数据库）、`MFGRAPH_MIGRATE_ON_START`、`MFGRAPH_KERNEL_MIGRATIONS`、`MFGRAPH_STORAGE_ROOT`、`MFGRAPH_TENANT_ID`、`MFGRAPH_MAX_UPLOAD_BYTES`、`MFGRAPH_MAX_JSON_BODY_BYTES`、`MFGRAPH_MAX_COMMIT_CHANGES`、`MFGRAPH_AUTH_TOKENS`（`token=ACTOR_REF,...`，例如 `token=alice` 或 `token=<ULID>`；见[身份引用](docs/zh-CN/api.md#身份引用actor-reference)）、`MFGRAPH_LOG_LEVEL`、`MFGRAPH_LOG_FORMAT`、`MFGRAPH_LOG_PSEUDONYM_KEY`、`MFGRAPH_SHUTDOWN_TIMEOUT`、`MFGRAPH_IDEMPOTENCY_LEASE`。`mfgraph-server migrate` 执行待执行的迁移后退出。

开发用的 MySQL 8.0 和 seekdb 通过 Docker 运行（`deploy/docker-compose.dev.yml`，只绑定 `127.0.0.1`，一次性本地口令；见[部署](docs/zh-CN/deployment.md#数据库)）：

```bash
make dev-db-up      # 启动 MySQL 8.0 并等待就绪（需要 Docker）
make dev-seekdb-up  # 启动 seekdb（MySQL 5.7 子集，端口 2881）并等待就绪
make dev-db-down    # 停止（保留数据）；make dev-db-reset 会同时删除数据卷
make test-mysql     # 在 MySQL 上跑迁移与仓储测试：空库执行、重跑跳过、篡改报错等
make test-seekdb    # 在 seekdb 上跑同样的测试
make test-e2e-mysql # 在开发用 MySQL 上跑验收测试（test/e2e）；seekdb 用 test-e2e-seekdb
```

其他检查：

```bash
make ci           # 在本地跑 CI 工作流中除数据库任务以外的全部步骤（不需要 Docker）
make ci-mysql     # 用本地容器跑 CI 的 MySQL 任务：迁移、仓储、验收测试
make ci-seekdb    # 用本地 seekdb 容器跑同样的任务
make test-e2e     # 验收测试；需要 MFGRAPH_E2E_DB_DSN，没有时跳过
make vectors-verify vectors-crosscheck   # 对 testdata/vectors 做独立核对
```

CI（`.github/workflows/ci.yml`）在 GitHub Actions 上运行同样的 Make 目标，数据库任务（迁移与仓储、验收测试）分别使用 MySQL 8.0 和 seekdb 服务容器。

## 贡献

见 [CONTRIBUTING.zh-CN.md](CONTRIBUTING.zh-CN.md)（[English](CONTRIBUTING.md)）。安全问题见 [SECURITY.zh-CN.md](SECURITY.zh-CN.md)（[English](SECURITY.md)）。行为准则见 [CODE_OF_CONDUCT.zh-CN.md](CODE_OF_CONDUCT.zh-CN.md)（[English](CODE_OF_CONDUCT.md)）。
