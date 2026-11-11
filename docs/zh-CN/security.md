# 安全

[English](../security.md) | **简体中文**

## 身份

- `principal_uid`（ULID）是权威身份。
- 人、Agent、服务、团队都是 principal；授权不按 `kind` 分支。
- 外部身份：OIDC `sub`、Matrix MXID、email；均为可选。
- Agent/服务 token 是短时能力 token。

## 授权

角色：

| 角色 | 范围 |
|---|---|
| owner | 全部 + ACL/删除/保留 |
| admin | 管理 workspace/workstream/ACL |
| editor | 读/写/snapshot/lock/upload |
| reviewer | 读/审核/promote |
| reader | 只读 |

- 默认拒绝；
- 继承：project → project_node → workspace → workstream；
- 子范围不能超过父范围；
- rename/move 后重新评估路径 ACL；
- 未授权读返回 404；
- 默认允许自我审批；可用策略要求独立审核。

## Agent/MCP 能力

- token 包含 project/scope/workspace/workstream/actions/exp；
- 不下发通配的对象存储凭据；
- 下载使用短时签名 URL 或代理；
- 高风险动作（删除、merge complete、legal hold、ACL）可要求人工审批；
- MCP 忽略工具参数里的 `principal_id`/`on_behalf_of`；只认认证上下文。

## 工作流安全

- HMAC-SHA256 webhook 签名；
- timestamp + event id 防重放；
- URL allowlist / egress 策略（SSRF）；
- schema 校验；未知类型进死信；
- 不从事件内容执行命令；
- secret 只存引用，绝不进 payload。

## 数据安全

- 传输 TLS；静态加密走 S3 SSE/磁盘/KMS；
- 私有 bucket；短 presigned TTL；
- 路径规范化：NFC，拒绝 `..`、绝对路径、NUL、符号链接；
- 上传病毒扫描；
- 按 `tenant_id` + 存储前缀做租户隔离；
- 受监管场景支持 retention/legal hold/WORM；
- 审计 append-only；日志不放 token/文件内容。

## 威胁模型（STRIDE）

Spoofing（OIDC/mTLS/HMAC）、Tampering（sha256/audit）、Repudiation（append-only audit）、Information disclosure（authz/private buckets）、DoS（quota/rate limit）、Elevation（capability/scope）、SSRF（allowlist）、Supply chain（license/SBOM scan）。

## 标准

设计参考：OWASP ASVS、NIST 800-207 Zero Trust、ISO 27001/SOC2、GDPR/HIPAA/legal hold。这些是设计目标，不是认证。
