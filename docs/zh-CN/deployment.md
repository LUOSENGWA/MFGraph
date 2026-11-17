# 部署

[English](../deployment.md) | **简体中文**

## 形态

| 形态 | 组件 | 用途 |
|---|---|---|
| embedded | 宿主 + mfgraph 模块进程（`MFGRAPH_MODE=module`，见[模块模式](../module-mode.md)） + DB + 对象存储 | 平台集成 |
| standalone | `mfgraph-server` + DB + 对象存储 | 团队自托管 |
| HA | 多个 `mfgraph-server` + MySQL HA + S3 | 企业 |
| dev | compose + MySQL + MinIO/POSIX | 本地/CI |

## 运行 mfgraph-server（M1.0a）

服务端用环境变量配置，也可以再加一个配置文件：`mfgraph-server [--config FILE] [--socket PATH] [serve|migrate|version]`，
两个参数也可以写在子命令后面。配置文件每行一个 `KEY=VALUE`，只能写下表里的变量（`#` 注释，不处理引号，不做 `${...}` 展开）；
命令行参数优先于环境变量，环境变量优先于配置文件。同一个二进制有两种模式（`MFGRAPH_MODE`）：这里说的 `standalone`，以及作为宿主平台模块进程运行的 `module`，
后者和它的变量（`MODULE_ID`、`MODULE_SOCKET`、`MODULE_INTERNAL_TOKEN`、`MFGRAPH_OPS_DSN`、`MFGRAPH_TRUSTED_HEADER_PREFIX`、
`MFGRAPH_INTERNAL_TOKEN_HEADER`、`MFGRAPH_OUTBOX_TABLE`）见[模块模式](../module-mode.md)（英文）。

| 变量 | 默认值 | 含义 |
|---|---|---|
| `MFGRAPH_MODE` | `standalone` | `standalone` 或 `module` |
| `MFGRAPH_HTTP_ADDR` | `127.0.0.1:8094` | `host:port`，或 `unix:<path>` 表示 Unix domain socket（见下文）；`--socket PATH` 优先 |
| `MFGRAPH_API_PREFIX`（别名 `MFGRAPH_ROUTE_PREFIX`） | `/api/v1` | 业务接口的路径前缀；`/healthz` 和 `/readyz` 始终在根路径 |
| `MFGRAPH_DB_DSN`（别名 `MFGRAPH_DSN`） | — | MySQL DSN（go-sql-driver），例如 `mfgraph:<password>@tcp(127.0.0.1:3306)/mfgraph`；服务**必须**连数据库 |
| `MFGRAPH_MIGRATE_ON_START` | `false` | 开始服务前执行未执行的迁移 |
| `MFGRAPH_KERNEL_MIGRATIONS` | `true` | 是否包括 kernel 表（`projects`、`principals`）；只用于独立部署 |
| `MFGRAPH_STORAGE_ROOT`（别名 `MFGRAPH_DATA_DIR`） | — | POSIX BlobStore 的根目录；配置了数据库时必须配置，否则拒绝启动 |
| `MFGRAPH_TENANT_ID` | `00000000000000000000000000` | 独立部署的租户（ULID） |
| `MFGRAPH_MAX_UPLOAD_BYTES` | `1073741824`（1 GiB） | 单次上传上限（1 字节到 5 TiB） |
| `MFGRAPH_MAX_JSON_BODY_BYTES` | `16777216`（16 MiB） | 其他请求的请求体上限（1 KiB 到 1 GiB）；超过时返回 `413 request_too_large`，超出上限的部分不会被读取。提交请求的大小也受它限制 |
| `MFGRAPH_MAX_COMMIT_CHANGES` | `10000` | 单次提交的改动条数上限 |
| `MFGRAPH_AUTH_TOKENS` | — | 静态 bearer token：`token=ACTOR_REF[,token=ACTOR_REF...]`（token 至少 16 个字符） |
| `MFGRAPH_LOG_LEVEL` / `MFGRAPH_LOG_FORMAT` | `info` / `json` | log/slog 设置（给人看用 `text`） |
| `MFGRAPH_LOG_PSEUDONYM_KEY` | 每次启动随机生成 | 日志中 actor 假名的密钥（至少 16 字节；见[安全](security.md)） |
| `MFGRAPH_SHUTDOWN_TIMEOUT` | `8s` | 收到 SIGTERM 或 SIGINT 后的优雅退出时间 |
| `MFGRAPH_IDEMPOTENCY_LEASE` | `60s` | 一个请求持有其 `Idempotency-Key` 的时间（1s 到 24h）；超过后重复请求会重新执行，第一个请求如果还在执行，会以 `409 conflict` 失败且不做任何改动。应大于最长请求的耗时 |

- **必须连数据库。** 没有 `MFGRAPH_DB_DSN` 时服务照常启动，但 `/readyz` 返回 503，所有业务接口返回 `503 db_unavailable`；数据库连不上时也一样。用 `mfgraph-server migrate`（或 `MFGRAPH_MIGRATE_ON_START=true`）执行迁移；迁移只需要 `MFGRAPH_DB_DSN`。
- **别名。** 同一项设置的两个名字（`MFGRAPH_DB_DSN` 和 `MFGRAPH_DSN`、`MFGRAPH_STORAGE_ROOT` 和 `MFGRAPH_DATA_DIR`、`MFGRAPH_API_PREFIX` 和 `MFGRAPH_ROUTE_PREFIX`，以及 `MFGRAPH_HTTP_ADDR=unix:<path>` 和 `MODULE_SOCKET=<path>`）都设置时值必须相同，否则拒绝启动。
- **就绪检查。** `/readyz` 检查数据库（必需）和存储根目录（模块模式下还检查宿主的 outbox 表），并报告 `unicode_version`（路径规则所用的 Unicode 版本 V）和 `version`（服务版本）。
- **Unix domain socket。** `MFGRAPH_HTTP_ADDR=unix:/run/mfgraph/api.sock` 监听一个只有服务进程的用户能使用的 socket（权限 0600，创建时 umask 为 0177）。启动时如果这个路径上残留着已停止的服务留下的 socket，就删掉；如果是普通文件、目录或仍在应答的服务的 socket，就拒绝启动。退出时删除 socket。
- **路径前缀。** 设置 `MFGRAPH_API_PREFIX=/files/v1` 后，契约中的 `/api/v1/...` 接口改由 `/files/v1/...` 提供（`/api/v1/...` 返回 404），反向代理可以把模块路径原样转发过来。前缀以 `/` 开头，不以 `/` 结尾，最长 128 字节，每段只用 `A–Z a–z 0–9 . _ ~ -`。
- **存储。** POSIX BlobStore 把每个对象存成文件 `<MFGRAPH_STORAGE_ROOT>/<存储前缀>/<key>`（见[存储](storage.md)）。根目录必须存在且可写；临时文件（`.tmp-*.part`）建在对象旁边，最后 rename 到位。
- **退出。** 收到 SIGTERM 后，服务停止接受新连接，并最多等待 `MFGRAPH_SHUTDOWN_TIMEOUT`（8 秒，低于守护进程常用的 10 秒强杀时限）让进行中的请求完成；模块模式下还在同一时间内再转发一批事件。
- **事件。** 每次改动都在自己的事务里把[事件](../events.md)（英文）写进 outbox。独立部署留在 outbox 里（暂时没有发布者）；模块模式会把它们转发到宿主的 outbox 表。
- **Bundle。** `make bundle` 构建模块形态的可复现 bundle（每个平台一个 `tar.zst`，加上 `sha256sums.txt`，都在 `dist/`）；`make bundle-check` 用空的构建缓存再构建一遍并比较。见[模块模式](../module-mode.md#bundles)。

```bash
make dev-db-up
export MFGRAPH_DB_DSN='root:mfgraph@tcp(127.0.0.1:3306)/mfgraph'
./bin/mfgraph-server migrate
MFGRAPH_STORAGE_ROOT=/var/lib/mfgraph MFGRAPH_AUTH_TOKENS='<token>=alice' ./bin/mfgraph-server
```

## 数据库

支持的 SQL 方言是 **MySQL**。CI 每次都在两种服务器上执行迁移：**MySQL 8.0** 和 **seekdb**（兼容 MySQL 5.7 子集的单机数据库，宿主平台单机部署时使用）。两者建出的表结构完全相同。为了落在两者的公共子集内，ID 和 hash 列使用 `utf8mb4_bin`，而不是 seekdb 不支持的 `ascii` 字符集；迁移 lint 会检查这一点。

在 MySQL 上，表必须使用 InnoDB 的 **DYNAMIC** 行格式，这是 MySQL 5.7.9 起的默认值（`innodb_default_row_format = DYNAMIC`）。COMPACT 和 REDUNDANT 把索引键限制在 767 字节以内，而 `idempotency_keys` 的主键（actor reference、route 和 key）有 1403 字节；DYNAMIC 允许 3072 字节。默认行格式不是 DYNAMIC 时，迁移会失败。

开发时，`deploy/docker-compose.dev.yml` 在 `127.0.0.1` 上同时提供两者：MySQL 8.0 在 3306 端口，seekdb 在 2881 端口（用户 `root`，无密码；镜像固定到具体 tag，提供 `linux/amd64` 与 `linux/arm64` 版本，仅供测试使用，至少需要 2 GB 内存）。

```bash
make ci-mysql     # 启动 MySQL 8.0 并在其上跑迁移测试
make ci-seekdb    # 启动 seekdb 并在其上跑同样的测试
```

## 启动检查

路径规则依赖 Unicode 数据表，[内容格式](../spec/zh-CN/content-format.md#24-unicode-版本-v)规定了它们的版本 V。`mfgraph-server serve` 或 `migrate` 在处理任何数据之前，先把编译进程序的数据表与 V 比较：Go 标准库的数据表（决定码位是否已分配）必须正好是 V，`golang.org/x/text` 的数据表（NFC、大小写折叠）必须不低于 V。不满足就在日志里写明实际版本和要求的 V，并以非 0 状态退出。没有任何跳过检查的开关；升级 V 必须同时修改代码和规范。`mfgraph debug` 只打印警告。

## 最小 compose（概念示例）

S3 相关的配置项属于以后的切片；M1.0a 使用 POSIX 存储（`MFGRAPH_STORAGE_ROOT`）。

```yaml
services:
  mysql:
    image: mysql:8.0
    environment:
      MYSQL_DATABASE: mfgraph
      MYSQL_USER: mfgraph
      MYSQL_PASSWORD_FILE: /run/secrets/mysql_password
    volumes: [mysql-data:/var/lib/mysql]

  rustfs:
    image: rustfs/rustfs:latest
    command: server /data --console-address ":9001"
    volumes: [rustfs-data:/data]
    ports: ["9000:9000", "9001:9001"]

  mfgraph:
    image: mfgraph/mfgraph-server:0.1.0
    environment:
      MFGRAPH_DB_DSN: mfgraph:${MYSQL_PASSWORD}@tcp(mysql:3306)/mfgraph
      MFGRAPH_BLOB_BACKEND: s3
      MFGRAPH_S3_ENDPOINT: http://rustfs:9000
      MFGRAPH_S3_BUCKET: mfgraph
      MFGRAPH_PUBLIC_URL: https://mfgraph.example.com
    ports: ["8094:8094"]
    depends_on: [mysql, rustfs]
```

生产环境需加 TLS、secret manager、备份、监控。

## K8s

- Deployment `mfgraph-server`（>=2 副本；单写者租约）；
- 外部 MySQL/OceanBase；
- 外部 S3/RustFS；
- NetworkPolicy：只允许 gateway/DB/对象存储/工作流适配器；
- 非 root、只读 rootfs、drop capabilities；
- PDB、readiness/liveness/startup 探针；
- 备份 CronJob + 恢复演练。

## 升级

```text
备份 DB + 对象清单
摘流量
expand 迁移
启动新版本
健康/冒烟
逐步放量
下个 release 做 contract 迁移
```

N-1 API 兼容；优先旧二进制 + 新 schema，而不是回滚恢复。

迁移只新增表、可为空的列和索引，所以旧二进制在新 schema 上照常工作。启动新版本前先执行迁移：M1.0a 模块化版本新增
`0009_actor_types`（可为空的身份类型列），新二进制会读这些列。

## 备份

- DB dump + binlog/PITR；
- 对象 bucket/prefix 清单；
- 校验 manifest/pack sha256；
- 每月恢复演练；
- 目标 RPO <=15 分钟，RTO <=1 小时（设计目标）。

## 端口

| 端口 | 用途 |
|---|---|
| 8094 | mfgraph API（`MFGRAPH_HTTP_ADDR` 默认 127.0.0.1:8094） |
| 3306/2881 | MySQL/OceanBase |
| 9000/9001 | S3/RustFS |
| 443 | TLS ingress |
