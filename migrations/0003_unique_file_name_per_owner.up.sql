-- 0003_unique_file_name_per_owner.up.sql
-- Sync (REQUIREMENTS.md §6) identifies files purely by name -- the desktop
-- client keys its local<->remote matching and its on-disk sync folder paths
-- by name alone, so two files owned by the same user with identical names
-- would be ambiguous (and crash the desktop sync loop, which builds a
-- name-keyed dictionary with a uniqueness assumption). FileService.Create
-- already auto-renames ("file (1).txt") on a collision going forward; this
-- index is the backstop that makes that guarantee actually hold under
-- concurrent uploads, not just in the common single-request case.
--
-- A straight CREATE UNIQUE INDEX would fail outright if any duplicate
-- (uploaded_by, name) pairs already exist, so this first renames every row
-- past the first per (uploaded_by, name) group (ordered by id, i.e. oldest
-- keeps its name), using the same "name (n).ext" convention
-- FileService.Create uses -- splitting on the *last* dot only (mirrors
-- Go's filepath.Ext), so "archive.tar.gz" becomes "archive.tar (1).gz", not
-- "archive (1).tar.gz". Unlike a single-pass rename, this checks each
-- candidate name against the *whole* current table and bumps the suffix
-- again on a further collision (e.g. "report (1).pdf" already existing as
-- its own unrelated file) -- a single pass over sibling duplicates alone
-- isn't enough, as a duplicate-data dry run against a real copy of this
-- database confirmed.
DO $$
DECLARE
    rec RECORD;
    dot_pos INT;
    stem TEXT;
    ext TEXT;
    suffix INT;
    candidate TEXT;
BEGIN
    FOR rec IN
        SELECT id, uploaded_by, name,
               row_number() OVER (PARTITION BY uploaded_by, name ORDER BY id) AS rn
        FROM files
        ORDER BY id
    LOOP
        CONTINUE WHEN rec.rn = 1;

        dot_pos := position('.' IN reverse(rec.name));
        IF dot_pos = 0 THEN
            stem := rec.name;
            ext := '';
        ELSE
            stem := left(rec.name, length(rec.name) - dot_pos);
            ext := right(rec.name, dot_pos); -- includes the dot
        END IF;

        suffix := rec.rn - 1;
        candidate := stem || ' (' || suffix || ')' || ext;

        WHILE EXISTS (
            SELECT 1 FROM files
            WHERE uploaded_by = rec.uploaded_by AND name = candidate AND id <> rec.id
        ) LOOP
            suffix := suffix + 1;
            candidate := stem || ' (' || suffix || ')' || ext;
        END LOOP;

        UPDATE files SET name = candidate WHERE id = rec.id;
    END LOOP;
END $$;

CREATE UNIQUE INDEX files_uploaded_by_name_key ON files (uploaded_by, name);
