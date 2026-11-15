-- Workspaces: versioned content roots. storage_prefix is written once at
-- creation and never changed; storage keys in other tables are relative to it.
-- workspace_type is a UI/default-policy hint only; access control is done by
-- the authorizer, never by this column.
-- Paths and the storage prefix are VARBINARY and compared byte for byte
-- (utf8mb4_bin ignores trailing spaces, which the path rules allow).
-- storage_prefix_key is the hex SHA-256 of the ASCII-lowercased prefix; the
-- unique key rejects two prefixes on one backend that differ only in case.
-- Nested prefixes are rejected by the application when a workspace is
-- created (see the specification, storage keys).
-- Actors (who owns or created a row) are actor references, not IDs: *_ref is
-- the string the host platform (or the standalone token configuration) uses
-- for the caller, at most 255 bytes of UTF-8 without control characters,
-- stored as given and compared byte for byte (see the API documentation).
-- *_principal_uid is an optional ULID of the same actor that a host platform
-- may provide later; it stays NULL until then.

CREATE TABLE IF NOT EXISTS workspaces (
  workspace_id       CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  tenant_id          CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  project_id         CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  scope_node_id      CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NULL,
  workspace_type     VARCHAR(32) NOT NULL,
  scope_path         VARBINARY(4096) NOT NULL DEFAULT '',
  owner_ref          VARBINARY(255) NOT NULL,
  owner_principal_uid CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NULL,
  storage_backend_id VARCHAR(64) NOT NULL,
  storage_prefix     VARBINARY(942) NOT NULL,
  storage_prefix_key CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  display_name       VARCHAR(255) NOT NULL DEFAULT '',
  status             VARCHAR(32) NOT NULL DEFAULT 'active',
  version            BIGINT UNSIGNED NOT NULL DEFAULT 1,
  created_at_us      BIGINT NOT NULL,
  updated_at_us      BIGINT NOT NULL,
  PRIMARY KEY (workspace_id),
  UNIQUE KEY uk_workspaces_storage_prefix (storage_backend_id, storage_prefix_key),
  KEY idx_workspaces_tenant (tenant_id, status, updated_at_us),
  KEY idx_workspaces_project (project_id, scope_node_id, status, updated_at_us),
  KEY idx_workspaces_owner (owner_ref, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
