# Security

[English](security.md) | [简体中文](zh-CN/security.md)

## Identity

- Every caller is an actor, identified by its **actor reference**: the identity string the host platform passes (a worker name, a user name, an ID such as `@alice:example.com`), or in a standalone deployment the string form of the principal's `principal_uid` (ULID). Actor references are at most 255 bytes of UTF-8 without control characters, stored as given and compared byte for byte (see [actor references](api.md#actor-references)). A ULID for an actor (`*_principal_uid`) is optional.
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
- **first core slice (standalone):** any authenticated caller may create a project; the creator (its `owner_ref`, compared byte for byte) is the only one who can see the project and its workspaces and do anything there; everyone else gets 404. The roles above come later; embedded in a host platform, the host's access control decides;
- **module mode:** the host's proxy authenticates the caller and admits the request to one project; MFGraph trusts its identity headers only together with the internal token (`MODULE_INTERNAL_TOKEN`, compared in constant time, never logged), listens only on a Unix domain socket of mode 0600, and lets a request see exactly the resources of its project (404 otherwise, never 403). A header sent twice, a missing required header or an actor reference that breaks the rules gives 401. Signed identity headers (HMAC) and authorization by team come later (see [module mode](module-mode.md));
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
- actor references are personal data: log records at info level and above identify a caller only as `actor`, the first 16 hex digits of HMAC-SHA256(key, actor reference) with a key of the deployment (`MFGRAPH_LOG_PSEUDONYM_KEY`, at least 16 bytes), so the records of one actor can be linked without showing who it is, and the pseudonym of a known name cannot be computed without the key. Without a configured key the server uses a random one per start (and warns): pseudonyms then change with every restart, and changing the key has the same effect. The raw reference (and a principal UID, which in a standalone deployment equals it) appears only in debug-level records. The same holds for the actor a module request acts on behalf of (`on_behalf_of`); the actor's type, the project and the trace ID are logged as they are.
- **first core slice, requests and responses:** the body of every request but an upload is at most `MFGRAPH_MAX_JSON_BODY_BYTES` (16 MiB) and is never read beyond that limit; JSON bodies must be sent as `application/json` and be one JSON value. Uploads are bounded by `MFGRAPH_MAX_UPLOAD_BYTES`; limits for slow uploads (a minimum rate, a total time, connections per caller) come with M1.0b. Downloads are `application/octet-stream` with `Content-Disposition: attachment` and `X-Content-Type-Options: nosniff` (error responses carry nosniff too), so a browser does not render stored content as a page. Project names and workspace display names, like file names, may not contain control characters, bidi controls or invisible characters.

## Threat model (STRIDE)

Spoofing (OIDC/mTLS/HMAC), Tampering (sha256/audit), Repudiation (append-only audit), Information disclosure (authz/private buckets), DoS (quota/rate limit), Elevation (capability/scope), SSRF (allowlist), supply chain (license/SBOM scan).

## Standards

Design references: OWASP ASVS, NIST 800-207 Zero Trust, ISO 27001/SOC2, GDPR/HIPAA/legal hold. These are design targets, not certifications.
