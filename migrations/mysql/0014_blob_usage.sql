-- Older writers leave these markers NULL. The application can meter old
-- content in bounded batches while new uploads are being completed.
ALTER TABLE blobs ADD COLUMN usage_counted_at_us BIGINT NULL;
ALTER TABLE blobs ADD KEY idx_blobs_usage (usage_counted_at_us, workspace_id, blob_hash);
ALTER TABLE workspace_usage ADD COLUMN usage_started_at_us BIGINT NULL;
