-- D-24 / D-26 / D-28 — Phase 1 GC worker support.
--
-- storage_id: nullable text. NULL means BYOS (client supplies get_urls,
--   server holds no bytes). Non-NULL means controlled — the segment
--   service supplies the deployment-wide constant per
--   InsertBatch.ControlledStorageID; Phase 2 promotes this to FK
--   against a storage_backends table. Operationally immutable on a row
--   once set (INV-META-14 / BR-META-08).
-- reaping: GC claim flag. The GC worker flips it true when claiming a
--   ref_count = 0 row for byte-deletion; the metastore's race-aware
--   ref-count increment refuses to bump it (D-26 / INV-META-15) so the
--   client gets a transient ErrSegmentObjectReaping and retries with a
--   fresh object_id.
ALTER TABLE objects
    ADD COLUMN IF NOT EXISTS storage_id TEXT,
    ADD COLUMN IF NOT EXISTS reaping BOOLEAN NOT NULL DEFAULT false;

-- Partial indexes for the GC sweep — both views matter (BR-META-13 /
-- D-26).
--
-- objects_gc_claim_idx: claim hot path. Runs every GC sweep interval to
--   find ref_count=0, reaping=false rows. Without this index the sweep
--   scales linearly with the objects table.
-- objects_gc_retry_idx: post-claim retry view. Indexes rows GC has
--   claimed but not yet reaped (transient: in-flight sweep or failed
--   delete pending next attempt). Partial keeps the index tiny since
--   reaping=true is rare in steady state.
CREATE INDEX IF NOT EXISTS objects_gc_claim_idx
    ON objects (id) WHERE ref_count = 0 AND reaping = false;
CREATE INDEX IF NOT EXISTS objects_gc_retry_idx
    ON objects (id) WHERE reaping = true;

-- Segment bounds invariants (ADR-0040 rule 4).
--
-- lower_ns and upper_ns are the half-open [lower, upper) bounds that
-- timerange.NsBounds derives from the segment timerange. A segment
-- timerange is always bounded and non-empty, so upper_ns is NOT NULL and
-- upper_ns > lower_ns. Negative bounds are valid: TAMS permits
-- timestamps before 0:0. The service rejects invalid timeranges before
-- the store is called; these constraints are backstops for a server
-- defect, and they do not detect bounds that were converted wrongly.
ALTER TABLE segments
    ALTER COLUMN upper_ns SET NOT NULL;
ALTER TABLE segments
    DROP CONSTRAINT IF EXISTS segments_bounds_nonempty;
ALTER TABLE segments
    ADD CONSTRAINT segments_bounds_nonempty
    CHECK (upper_ns > lower_ns);
