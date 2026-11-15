-- Kernel bootstrap for standalone deployments: minimal projects and
-- principals. When MFGraph is embedded in a host platform these tables are
-- owned by the host and this directory is not executed.
-- Conventions: IDs are ULIDs (CHAR(26) utf8mb4_bin), times are epoch
-- microseconds (BIGINT), states are VARCHAR checked by the application.
-- ID and hash columns use utf8mb4_bin, not the ascii character set: the
-- migrations run unchanged on MySQL 8.0 and on seekdb, which supports only
-- the utf8mb4 and binary character sets.

CREATE TABLE IF NOT EXISTS projects (
  project_id     CHAR(26) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  tenant_id      CHAR(26) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  name           VARCHAR(255) NOT NULL,
  status         VARCHAR(32) NOT NULL DEFAULT 'active',
  created_at_us  BIGINT NOT NULL,
  updated_at_us  BIGINT NOT NULL,
  PRIMARY KEY (project_id),
  KEY idx_projects_tenant (tenant_id, status, updated_at_us)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- kind: human / agent / service / team (MFGraph treats them alike).
-- principal_uid is the identity of a principal in a standalone deployment:
-- there, the actor reference stored in the *_ref columns of the MFGraph
-- tables is the string form of this ULID (and *_principal_uid is the same
-- value). Embedded in a host platform, actor references are the host's own
-- identity strings and this table is not used.
CREATE TABLE IF NOT EXISTS principals (
  principal_uid  CHAR(26) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  tenant_id      CHAR(26) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  display_name   VARCHAR(255) NOT NULL DEFAULT '',
  kind           VARCHAR(32) NOT NULL,
  status         VARCHAR(32) NOT NULL DEFAULT 'active',
  created_at_us  BIGINT NOT NULL,
  PRIMARY KEY (principal_uid),
  KEY idx_principals_tenant (tenant_id, kind, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
