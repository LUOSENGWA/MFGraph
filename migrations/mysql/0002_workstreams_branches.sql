-- Workstreams are version lanes (active/archived); they never carry task
-- assignment or dispatch state. Branches are refs with a generation counter
-- used for compare-and-swap updates of the head.
-- The main branch of a workspace: branch_type 'main', workstream_id NULL,
-- ref_name 'main'. Workstream branches: ref_name
-- 'workstreams/<workstream_id>/<branch_name>'.

CREATE TABLE IF NOT EXISTS workstreams (
  workstream_id       CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  project_id          CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  scope_node_id       CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NULL,
  workspace_id        CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  owner_principal_id  CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  created_by          CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  display_name        VARCHAR(255) NOT NULL DEFAULT '',
  default_branch_name VARCHAR(128) NOT NULL DEFAULT 'main',
  status              VARCHAR(32) NOT NULL DEFAULT 'active',
  version             BIGINT UNSIGNED NOT NULL DEFAULT 1,
  created_at_us       BIGINT NOT NULL,
  updated_at_us       BIGINT NOT NULL,
  PRIMARY KEY (workstream_id),
  KEY idx_workstreams_project (project_id, scope_node_id, status),
  KEY idx_workstreams_owner (owner_principal_id, status),
  KEY idx_workstreams_status (status, updated_at_us),
  KEY idx_workstreams_workspace (workspace_id, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS branches (
  branch_id         CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  workspace_id      CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  project_id        CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  scope_node_id     CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NULL,
  workstream_id     CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NULL,
  branch_name       VARCHAR(128) NOT NULL,
  ref_name          VARCHAR(255) NOT NULL,
  branch_type       VARCHAR(16) NOT NULL DEFAULT 'workstream',
  head_snapshot_id  CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NULL,
  generation        BIGINT UNSIGNED NOT NULL DEFAULT 1,
  status            VARCHAR(32) NOT NULL DEFAULT 'active',
  created_by        CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  created_at_us     BIGINT NOT NULL,
  updated_at_us     BIGINT NOT NULL,
  PRIMARY KEY (branch_id),
  UNIQUE KEY uk_branch_ref (workspace_id, ref_name),
  UNIQUE KEY uk_branch_workstream_name (workspace_id, workstream_id, branch_name),
  KEY idx_branches_project (project_id, scope_node_id, status),
  KEY idx_branches_workstream (workspace_id, workstream_id, status),
  KEY idx_branches_head (head_snapshot_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
