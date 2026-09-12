-- 0002_sync_versioning.up.sql
-- Adds a shared monotonically increasing version sequence used to drive the
-- sync/diff protocol (REQUIREMENTS.md §6), plus a file_deletions table so a
-- delete can be distinguished from "never downloaded yet".

CREATE SEQUENCE files_version_seq;

ALTER TABLE files
    ADD COLUMN version BIGINT NOT NULL DEFAULT nextval('files_version_seq');

CREATE TABLE file_deletions (
    id          BIGSERIAL PRIMARY KEY,
    file_id     BIGINT NOT NULL,
    uploaded_by BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    storage_key TEXT NOT NULL,
    version     BIGINT NOT NULL DEFAULT nextval('files_version_seq'),
    deleted_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_files_version ON files (version);
CREATE INDEX idx_file_deletions_version ON file_deletions (version);
