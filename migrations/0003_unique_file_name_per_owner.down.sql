-- 0003_unique_file_name_per_owner.down.sql
-- Reverts 0003_unique_file_name_per_owner.up.sql

DROP INDEX IF EXISTS files_uploaded_by_name_key;
