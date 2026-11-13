-- Snapshots are immutable. root_tree_hash is the hash of the root tree node
-- (the manifest); the tree itself lives in object storage. branch_id is the
-- branch the snapshot was created on and may be NULL. Parents are rows in
-- snapshot_parents (0 for a root snapshot, 1 for a commit, 2 for a merge).
-- The optional head path index (snapshot_entries) is not part of this
-- schema version.

CREATE TABLE IF NOT EXISTS snapshots (
  snapshot_id          CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  workspace_id         CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  project_id           CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  scope_node_id        CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NULL,
  scope_path           VARCHAR(1024) NOT NULL DEFAULT '',
  workstream_id        CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NULL,
  branch_id            CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NULL,
  ref_name             VARCHAR(255) NOT NULL DEFAULT '',
  author_principal_id  CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  on_behalf_of_id      CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NULL,
  message              VARCHAR(1024) NOT NULL DEFAULT '',
  trigger_kind         VARCHAR(32) NOT NULL,
  root_tree_hash       CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  tree_format_version  INT UNSIGNED NOT NULL DEFAULT 1,
  parent_count         TINYINT UNSIGNED NOT NULL DEFAULT 0,
  entry_count          BIGINT UNSIGNED NOT NULL DEFAULT 0,
  total_bytes          BIGINT UNSIGNED NOT NULL DEFAULT 0,
  workflow_instance_id CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NULL,
  observed_event_seq   BIGINT NULL,
  observed_at_us       BIGINT NULL,
  state_hash           CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  status               VARCHAR(32) NOT NULL DEFAULT 'committed',
  created_at_us        BIGINT NOT NULL,
  PRIMARY KEY (snapshot_id),
  KEY idx_snapshots_project (project_id, scope_node_id, created_at_us),
  KEY idx_snapshots_workspace (workspace_id, created_at_us),
  KEY idx_snapshots_branch (workspace_id, branch_id, created_at_us),
  KEY idx_snapshots_workflow (workflow_instance_id, observed_event_seq)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- parent_order: 0 = first parent, 1 = second parent.
CREATE TABLE IF NOT EXISTS snapshot_parents (
  snapshot_id  CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  parent_id    CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  parent_order TINYINT UNSIGNED NOT NULL,
  PRIMARY KEY (snapshot_id, parent_id),
  KEY idx_snapshot_parents_parent (parent_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
