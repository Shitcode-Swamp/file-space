-- 0004_file_sha256.up.sql
-- Adds a content-hash column so the sync/diff protocol (REQUIREMENTS.md
-- §6.1/§6.2) can tell a genuine conflict ("exists, hash differs") apart from
-- an ordinary download -- SyncService.Diff previously had no server-side
-- hash to compare a client-submitted manifest entry against.
--
-- Nullable and not backfilled: existing rows predate hashing and have no
-- content-derived value to fill it with. A NULL sha256 simply means that
-- file is never treated as a conflict until it's next re-uploaded (at which
-- point FileService.Create computes and stores its hash) -- safe fallback
-- to "download", matching the "no data" case the sync service already
-- treats as a non-conflict.

ALTER TABLE files
    ADD COLUMN sha256 TEXT;
