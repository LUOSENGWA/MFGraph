-- Transactions reserve their batch number immediately before commit. An
-- older writer leaves commit_seq NULL; its backlog drains in its old order.
CREATE TABLE IF NOT EXISTS outbox_sequence (
  singleton   TINYINT NOT NULL,
  last_batch  BIGINT NOT NULL,
  PRIMARY KEY (singleton)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

ALTER TABLE outbox_events ADD COLUMN commit_seq BIGINT NULL;
ALTER TABLE outbox_events ADD KEY idx_outbox_delivery (status, commit_seq, created_at_us, event_id);
