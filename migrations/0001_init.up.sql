-- 0001_init.up.sql
-- Initial schema: users and files (see REQUIREMENTS.md, users/files schema).

CREATE TABLE users (
    id            BIGSERIAL PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL
);

CREATE TABLE files (
    id           BIGSERIAL PRIMARY KEY,
    name         TEXT NOT NULL,
    storage_key  TEXT NOT NULL UNIQUE, -- path/key into the FileStorage backend (local disk now, S3/R2 later)
    size         BIGINT NOT NULL CHECK (size >= 0),
    extension    TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    modified_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    uploaded_by  BIGINT NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    edited_by    BIGINT NOT NULL REFERENCES users (id) ON DELETE RESTRICT
);

CREATE INDEX idx_files_extension ON files (extension);
CREATE INDEX idx_files_edited_by ON files (edited_by);
CREATE INDEX idx_files_uploaded_by ON files (uploaded_by);
