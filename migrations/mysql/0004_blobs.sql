-- Whole-file content objects, isolated per workspace (no cross-workspace
-- deduplication; every lookup by hash includes workspace_id).
-- blob_hash is the 64-digit lowercase hex SHA-256 of the file bytes and
-- hash_algo names the algorithm. storage_key is relative to the workspace
-- storage prefix (objects/<hh>/<hex>) and is VARBINARY like every key and path.
-- status: staged (uploaded, not yet referenced by a snapshot) or live.
-- Chunk and pack tables belong to a later schema version.

CREATE TABLE IF NOT EXISTS blobs (
  workspace_id  CHAR(26) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  blob_hash     CHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  hash_algo     VARCHAR(16) NOT NULL DEFAULT 'sha256',
  size_bytes    BIGINT UNSIGNED NOT NULL,
  storage_key   VARBINARY(1024) NOT NULL,
  status        VARCHAR(32) NOT NULL DEFAULT 'live',
  created_at_us BIGINT NOT NULL,
  PRIMARY KEY (workspace_id, blob_hash),
  KEY idx_blobs_gc (workspace_id, status, created_at_us)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
