-- A frozen manifest can be retried after native multipart assembly or a crash.
ALTER TABLE upload_sessions ADD COLUMN manifest_hash CHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL;
ALTER TABLE upload_sessions ADD COLUMN result_hash CHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL;
ALTER TABLE upload_sessions ADD COLUMN created_by_type VARCHAR(16) NULL;
ALTER TABLE upload_sessions ADD COLUMN cleaned_at_us BIGINT NULL;
