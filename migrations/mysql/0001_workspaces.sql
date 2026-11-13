-- Workspaces: versioned content roots. storage_prefix is written once at
-- creation and never changed; storage keys in other tables are relative to it.
-- workspace_type is a UI/default-policy hint only; access control is done by
-- the authorizer, never by this column.

CREATE TABLE IF NOT EXISTS workspaces (
  workspace_id       CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  tenant_id          CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  project_id         CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  scope_node_id      CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NULL,
  workspace_type     VARCHAR(32) NOT NULL,
  scope_path         VARCHAR(1024) NOT NULL DEFAULT '',
  owner_principal_id CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  storage_backend_id VARCHAR(64) NOT NULL,
  storage_prefix     VARCHAR(1024) NOT NULL,
  display_name       VARCHAR(255) NOT NULL DEFAULT '',
  status             VARCHAR(32) NOT NULL DEFAULT 'active',
  version            BIGINT UNSIGNED NOT NULL DEFAULT 1,
  created_at_us      BIGINT NOT NULL,
  updated_at_us      BIGINT NOT NULL,
  PRIMARY KEY (workspace_id),
  KEY idx_workspaces_tenant (tenant_id, status, updated_at_us),
  KEY idx_workspaces_project (project_id, scope_node_id, status, updated_at_us),
  KEY idx_workspaces_owner (owner_principal_id, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
