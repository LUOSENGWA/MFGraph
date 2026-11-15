# 部署

[English](../deployment.md) | **简体中文**

## 形态

| 形态 | 组件 | 用途 |
|---|---|---|
| embedded | 宿主 + mfgraph 模块 + DB + 对象存储 | 平台集成 |
| standalone | `mfgraph-server` + DB + 对象存储 | 团队自托管 |
| HA | 多个 `mfgraph-server` + MySQL HA + S3 | 企业 |
| dev | compose + MySQL + MinIO/POSIX | 本地/CI |

## 数据库

支持的 SQL 方言是 **MySQL**。CI 每次都在两种服务器上执行迁移：**MySQL 8.0** 和 **seekdb**（兼容 MySQL 5.7 子集的单机数据库，宿主平台单机部署时使用）。两者建出的表结构完全相同。为了落在两者的公共子集内，ID 和 hash 列使用 `utf8mb4_bin`，而不是 seekdb 不支持的 `ascii` 字符集；迁移 lint 会检查这一点。

开发时，`deploy/docker-compose.dev.yml` 在 `127.0.0.1` 上同时提供两者：MySQL 8.0 在 3306 端口，seekdb 在 2881 端口（用户 `root`，无密码；镜像固定到具体 tag，提供 `linux/amd64` 与 `linux/arm64` 版本，仅供测试使用，至少需要 2 GB 内存）。

```bash
make ci-mysql     # 启动 MySQL 8.0 并在其上跑迁移测试
make ci-seekdb    # 启动 seekdb 并在其上跑同样的测试
```

## 最小 compose（概念示例）

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

## 备份

- DB dump + binlog/PITR；
- 对象 bucket/prefix 清单；
- 校验 manifest/pack sha256；
- 每月恢复演练；
- 目标 RPO <=15 分钟，RTO <=1 小时（设计目标）。

## 端口

| 端口 | 用途 |
|---|---|
| 8094 | mfgraph API |
| 3306/2881 | MySQL/OceanBase |
| 9000/9001 | S3/RustFS |
| 443 | TLS ingress |
