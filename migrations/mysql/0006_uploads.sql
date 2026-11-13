-- Multipart upload sessions (S3 multipart or POSIX temporary parts). Defined
-- now so that the schema is frozen; the first core slice uploads whole files
-- in a single request and does not use these tables yet.
-- status: initiated / uploading / completed / aborted / expired.
-- part_size_bytes is chosen by the server; content identity is always the
-- whole-file SHA-256, never derived from parts.

CREATE TABLE IF NOT EXISTS upload_sessions (
  upload_id          CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  workspace_id       CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  workstream_id      CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NULL,
  path               VARCHAR(1024) NOT NULL,
  size_bytes         BIGINT UNSIGNED NOT NULL,
  content_type       VARCHAR(255) NULL,
  checksum_sha256    CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  part_size_bytes    BIGINT UNSIGNED NOT NULL,
  total_parts        INT UNSIGNED NOT NULL,
  storage_backend_id VARCHAR(64) NOT NULL,
  backend_upload_id  VARCHAR(1024) NOT NULL,
  status             VARCHAR(16) NOT NULL DEFAULT 'initiated',
  created_by         CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  created_at_us      BIGINT NOT NULL,
  expires_at_us      BIGINT NOT NULL,
  completed_at_us    BIGINT NULL,
  PRIMARY KEY (upload_id),
  KEY idx_uploads_workspace (workspace_id, status, expires_at_us),
  KEY idx_uploads_expiry (status, expires_at_us)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS upload_parts (
  upload_id       CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  part_number     INT UNSIGNED NOT NULL,
  size_bytes      BIGINT UNSIGNED NOT NULL,
  etag            VARCHAR(128) NULL,
  checksum_sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  status          VARCHAR(16) NOT NULL DEFAULT 'pending',
  updated_at_us   BIGINT NOT NULL,
  PRIMARY KEY (upload_id, part_number)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
