DROP INDEX IF EXISTS segments_object_id_idx;
DROP TABLE IF EXISTS allocations;
ALTER TABLE objects DROP COLUMN IF EXISTS claimed_at;
ALTER TABLE objects RENAME COLUMN deleting TO reaping;
