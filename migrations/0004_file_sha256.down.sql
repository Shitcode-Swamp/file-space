-- 0004_file_sha256.down.sql
ALTER TABLE files
    DROP COLUMN sha256;
