DROP INDEX IF EXISTS idempotency_keys_inflight_acquired;
ALTER TABLE idempotency_keys DROP COLUMN IF EXISTS acquired_at;
