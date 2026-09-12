-- 0002_sync_versioning.down.sql
-- Reverts 0002_sync_versioning.up.sql

DROP INDEX IF EXISTS idx_file_deletions_version;
DROP INDEX IF EXISTS idx_files_version;
DROP TABLE IF EXISTS file_deletions;
ALTER TABLE files DROP COLUMN IF EXISTS version;
DROP SEQUENCE IF EXISTS files_version_seq;
