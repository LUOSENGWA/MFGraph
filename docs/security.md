# Security

[English](security.md) | [简体中文](zh-CN/security.md)

## Identity

- `principal_uid` (ULID) is the canonical identity.
- Human, agent, service, and team are all principals; authorization does not branch on `kind`.
- External identities: OIDC `sub`, Matrix MXID, email; optional.
- Agent/service tokens are short-lived capability tokens.

## Authorization

Roles:

| Role | Scope |
|---|---|
| owner | full + ACL/delete/retention |
| admin | manage workspace/workstream/ACL |
| editor | read/write/snapshot/lock/upload |
| reviewer | read/review/promote |
| reader | read-only |

- default deny;
- inheritance: project → project_node → workspace → workstream;
- child scope cannot exceed parent;
- path ACL re-evaluated on rename/move;
- unauthorized reads return 404;
- self-approval is allowed by default; optional independent review via policy.

## Agent/MCP capability

- token contains project/scope/workspace/workstream/actions/exp;
- no wildcard object-store credentials;
- downloads use short-lived signed URLs or a proxy;
- high-risk actions (delete, merge complete, legal hold, ACL) can require human approval;
- MCP ignores `principal_id`/`on_behalf_of` from tool arguments; only auth context counts.

## Workflow security

- HMAC-SHA256 webhook signatures;
- timestamp + event id replay protection;
- URL allowlist / egress policy (SSRF);
- schema validation; unknown types go to dead-letter;
- no command execution from event content;
- secrets only by reference, never in payloads.

## Data security

- TLS in transit; encryption at rest via S3 SSE/disk/KMS;
- private buckets; short presigned TTL;
- path normalization: NFC, reject `..`, absolute paths, NUL, symlinks;
- virus scan for uploads;
- tenant isolation by `tenant_id` + storage prefix;
- retention/legal hold/WORM for regulated profiles;
- audit append-only; no tokens/file content in logs.

## Threat model (STRIDE)

Spoofing (OIDC/mTLS/HMAC), Tampering (sha256/audit), Repudiation (append-only audit), Information disclosure (authz/private buckets), DoS (quota/rate limit), Elevation (capability/scope), SSRF (allowlist), supply chain (license/SBOM scan).

## Standards

Design references: OWASP ASVS, NIST 800-207 Zero Trust, ISO 27001/SOC2, GDPR/HIPAA/legal hold. These are design targets, not certifications.
