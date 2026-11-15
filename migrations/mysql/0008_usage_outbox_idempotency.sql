-- Quota ledger, transactional outbox and API idempotency records.
-- Outbox rows are written in the same transaction as the business change
-- (for example snapshot + parents + branch compare-and-swap) and published
-- afterwards (at least once; consumers deduplicate by event_id).
-- Idempotency: the same actor, key and route with a different request_hash
-- is rejected (idempotency_key_reuse); the same key on another route, or of
-- another actor, is a different record. actor_ref is the caller's actor
-- reference (see 0001_workspaces.sql); it and the client's key are VARBINARY
-- (compared byte for byte). Primary key length: 255 + 255 * 4 + 128 = 1403
-- bytes, below the InnoDB limit of 3072.

CREATE TABLE IF NOT EXISTS workspace_usage (
  workspace_id   CHAR(26) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  used_bytes     BIGINT UNSIGNED NOT NULL DEFAULT 0,
  reserved_bytes BIGINT UNSIGNED NOT NULL DEFAULT 0,
  file_count     BIGINT UNSIGNED NOT NULL DEFAULT 0,
  snapshot_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
  version        BIGINT UNSIGNED NOT NULL DEFAULT 1,
  updated_at_us  BIGINT NOT NULL,
  PRIMARY KEY (workspace_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS outbox_events (
  event_id         CHAR(26) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  workspace_id     CHAR(26) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  event_type       VARCHAR(64) NOT NULL,
  aggregate_id     CHAR(26) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  payload_json     LONGTEXT NOT NULL,
  status           VARCHAR(16) NOT NULL DEFAULT 'pending',
  retry_count      INT UNSIGNED NOT NULL DEFAULT 0,
  next_retry_at_us BIGINT NULL,
  created_at_us    BIGINT NOT NULL,
  published_at_us  BIGINT NULL,
  PRIMARY KEY (event_id),
  KEY idx_outbox_pending (status, next_retry_at_us, created_at_us)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS idempotency_keys (
  idempotency_key VARBINARY(128) NOT NULL,
  actor_ref       VARBINARY(255) NOT NULL,
  route           VARCHAR(255) NOT NULL,
  request_hash    CHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  response_json   LONGTEXT NULL,
  status          VARCHAR(16) NOT NULL DEFAULT 'in_progress',
  created_at_us   BIGINT NOT NULL,
  expires_at_us   BIGINT NOT NULL,
  PRIMARY KEY (actor_ref, route, idempotency_key),
  KEY idx_idem_expiry (expires_at_us)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
