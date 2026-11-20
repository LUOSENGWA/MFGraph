-- Existing blobs retain their whole-file key and content hash.
ALTER TABLE blobs ADD COLUMN chunking VARCHAR(16) NULL;

CREATE TABLE IF NOT EXISTS packs (
  workspace_id CHAR(26) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  pack_id CHAR(26) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  storage_key VARBINARY(1024) NOT NULL,
  size_bytes BIGINT UNSIGNED NOT NULL,
  sha256 CHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  status VARCHAR(32) NOT NULL DEFAULT 'live',
  created_at_us BIGINT NOT NULL,
  PRIMARY KEY (workspace_id, pack_id),
  UNIQUE KEY uk_packs_sha (workspace_id, sha256),
  KEY idx_packs_gc (workspace_id, status, created_at_us)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS chunks (
  workspace_id CHAR(26) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  chunk_hash CHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  pack_id CHAR(26) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  offset_bytes BIGINT UNSIGNED NOT NULL,
  length_bytes BIGINT UNSIGNED NOT NULL,
  created_at_us BIGINT NOT NULL,
  PRIMARY KEY (workspace_id, chunk_hash, pack_id, offset_bytes),
  KEY idx_chunks_pack (workspace_id, pack_id, offset_bytes)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS blob_chunks (
  workspace_id CHAR(26) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  blob_hash CHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  file_offset BIGINT UNSIGNED NOT NULL,
  chunk_hash CHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  length_bytes BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (workspace_id, blob_hash, file_offset),
  KEY idx_blob_chunks_references (workspace_id, chunk_hash)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
