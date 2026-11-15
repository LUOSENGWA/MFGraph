# Deployment

[English](deployment.md) | [简体中文](zh-CN/deployment.md)

## Modes

| Mode | Components | Use |
|---|---|---|
| embedded | host + mfgraph module + DB + object store | platform integration |
| standalone | `mfgraph-server` + DB + object store | teams, self-hosted |
| HA | multiple `mfgraph-server` + MySQL HA + S3 | enterprise |
| dev | compose + MySQL + MinIO/POSIX | local/CI |

## Database

The supported SQL dialect is **MySQL**. CI applies the migrations on two servers in every run: **MySQL 8.0** and **seekdb** (a single-node database compatible with a subset of MySQL 5.7, which host platforms use for single-machine deployments). Both build the same schema. To stay inside the common subset, ID and hash columns use `utf8mb4_bin` rather than the `ascii` character set, which seekdb does not provide; the migration lint enforces this.

For development, `deploy/docker-compose.dev.yml` runs both on `127.0.0.1`: MySQL 8.0 on port 3306 and seekdb on port 2881 (user `root` without a password; the image is pinned to an exact tag, is published for `linux/amd64` and `linux/arm64`, and is meant for tests only; it needs at least 2 GB of memory).

```bash
make ci-mysql     # start MySQL 8.0 and run the migration tests on it
make ci-seekdb    # start seekdb and run the same tests on it
```

## Minimal compose (conceptual)

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

Production adds TLS, secret manager, backups, monitoring.

## K8s

- Deployment `mfgraph-server` (>=2 replicas; single-writer lease);
- external MySQL/OceanBase;
- external S3/RustFS;
- NetworkPolicy: only gateway/DB/object store/workflow adapter;
- non-root, read-only rootfs, drop capabilities;
- PDB, readiness/liveness/startup probes;
- backup CronJob + restore drill.

## Upgrade

```text
backup DB + object manifest
drain traffic
expand migration
start new version
health/smoke
ramp traffic
contract migration in next release
```

N-1 API compatibility; prefer old binary + new schema over restore.

## Backup

- DB dump + binlog/PITR;
- object bucket/prefix manifest;
- verify manifest/pack sha256;
- restore drill monthly;
- target RPO <=15 min, RTO <=1 h (design target).

## Ports

| Port | Use |
|---|---|
| 8094 | mfgraph API |
| 3306/2881 | MySQL/OceanBase |
| 9000/9001 | S3/RustFS |
| 443 | TLS ingress |
