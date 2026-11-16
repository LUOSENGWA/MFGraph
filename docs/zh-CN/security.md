# 安全

[English](../security.md) | **简体中文**

## 身份

- 每个调用者都是一个操作者，用**身份引用**标识：宿主平台传入的身份字符串（worker 名、用户名、`@alice:example.com` 这样的 ID），独立部署时是该 principal 的 `principal_uid`（ULID）的字符串形式。身份引用至多 255 字节 UTF-8、不含控制字符，按原样保存、按字节比较（见[身份引用](api.md#身份引用actor-reference)）。操作者的 ULID（`*_principal_uid`）是可选的。
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
- **第一个核心切片（独立部署）：** 任何已认证的调用者都可以创建项目；创建者（其 `owner_ref`，按字节比较）是唯一能看到这个项目及其工作区、并在其中做任何操作的人，其他人一律得到 404。上面的角色以后再做；嵌入宿主平台时由宿主平台的访问控制判定；
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
- 身份引用属于个人信息：info 及以上级别的日志只用 `actor` 标识调用者，值为 HMAC-SHA256(密钥, 身份引用) 的前 16 位 hex，密钥由部署提供（`MFGRAPH_LOG_PSEUDONYM_KEY`，至少 16 字节）。同一操作者的日志仍能关联，但看不出是谁，没有密钥也无法算出已知名字的假名来比对。没有配置密钥时，服务每次启动随机生成一个（并打印警告）：重启前后的假名无法关联；更换密钥也是一样。原始身份引用（以及独立部署时与之相同的 principal UID）只出现在 debug 级别的日志里。
- **第一个核心切片的请求与响应：** 上传以外的请求体最多 `MFGRAPH_MAX_JSON_BODY_BYTES`（16 MiB），超出上限的部分不会被读取；JSON 请求体必须以 `application/json` 发送，并且是单个 JSON 值。上传受 `MFGRAPH_MAX_UPLOAD_BYTES` 限制；针对慢速上传的限制（最低速率、总时长、每个调用者的连接数）随 M1.0b 提供。下载使用 `application/octet-stream`，带 `Content-Disposition: attachment` 和 `X-Content-Type-Options: nosniff`（错误响应也带 nosniff），浏览器不会把存储的内容当作网页渲染。项目名和工作区显示名与文件名一样，不能含控制字符、双向控制字符和不可见字符。

## 威胁模型（STRIDE）

Spoofing（OIDC/mTLS/HMAC）、Tampering（sha256/audit）、Repudiation（append-only audit）、Information disclosure（authz/private buckets）、DoS（quota/rate limit）、Elevation（capability/scope）、SSRF（allowlist）、Supply chain（license/SBOM scan）。

## 标准

设计参考：OWASP ASVS、NIST 800-207 Zero Trust、ISO 27001/SOC2、GDPR/HIPAA/legal hold。这些是设计目标，不是认证。
