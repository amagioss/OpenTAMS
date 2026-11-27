-- BR-GC-50. Rename the GC claim flag. PostgreSQL updates the predicates
-- of objects_gc_claim_idx and objects_gc_retry_idx automatically.
ALTER TABLE objects RENAME COLUMN reaping TO deleting;

-- BR-GC-65. Time of the claim. NULL while deleting = false.
ALTER TABLE objects ADD COLUMN IF NOT EXISTS claimed_at TIMESTAMPTZ;
UPDATE objects SET claimed_at = now() WHERE deleting = true;

-- BR-GC-01. One row for each object ID with an issued upload URL that no
-- segment has registered. No foreign key to flows (BR-GC-08).
CREATE TABLE IF NOT EXISTS allocations (
    id          TEXT        PRIMARY KEY,
    flow_id     UUID        NOT NULL,
    storage_id  TEXT        NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleting    BOOLEAN     NOT NULL DEFAULT false,
    claimed_at  TIMESTAMPTZ
);

-- Sweep 2 claim (BR-GC-40).
CREATE INDEX IF NOT EXISTS allocations_gc_claim_idx
    ON allocations (expires_at, id) WHERE deleting = false;

-- Sweep 2 retry reclaim (BR-GC-64).
CREATE INDEX IF NOT EXISTS allocations_gc_retry_idx
    ON allocations (id) WHERE deleting = true;

-- BR-GC-51. Segment count of sweep 1 and the foreign-key check of an
-- objects delete. A plain index: it is part of this transaction.
CREATE INDEX IF NOT EXISTS segments_object_id_idx ON segments (object_id);
