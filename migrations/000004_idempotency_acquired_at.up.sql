-- Track when an idempotency key was acquired so a stale-in-flight reaper can
-- DELETE rows whose owning request has clearly outlived the server's
-- WriteTimeout. See REQ-RATE-09 (idempotency lifecycle) and BR-IDMP-04
-- (handler-finalised, threshold-reaped).
ALTER TABLE idempotency_keys
    ADD COLUMN acquired_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- Partial index over in_flight rows only — that is the working set the reaper
-- scans every IDEMPOTENCY_REAPER_INTERVAL. Completed rows are reaped via
-- expires_at by Prune (existing idempotency_keys_expires index).
CREATE INDEX idempotency_keys_inflight_acquired
    ON idempotency_keys (acquired_at)
    WHERE in_flight;
