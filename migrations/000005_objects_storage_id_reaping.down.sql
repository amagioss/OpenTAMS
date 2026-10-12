ALTER TABLE segments
    DROP CONSTRAINT IF EXISTS segments_bounds_nonempty;
ALTER TABLE segments
    ALTER COLUMN upper_ns DROP NOT NULL;

DROP INDEX IF EXISTS objects_gc_retry_idx;
DROP INDEX IF EXISTS objects_gc_claim_idx;

ALTER TABLE objects
    DROP COLUMN IF EXISTS reaping,
    DROP COLUMN IF EXISTS storage_id;
