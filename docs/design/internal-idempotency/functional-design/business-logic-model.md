# Business Logic Model — internal/idempotency

## Acquire(key, bodyHash, ttl) → AcquireResult

Purpose: atomically claim a new idempotency slot or inspect an existing one.

```
1. Validate: key non-empty and len(key) <= 255 → ErrSchemaValidation if violated
2. DELETE expired record for this key (expires_at <= now())
   Rationale: lazy expiry ensures correctness without requiring Prune to have run
3. SELECT FOR UPDATE on live record (key = $1)
   Rationale: row-level lock prevents lost-update race between concurrent callers
4. If no row:
   a. INSERT (key, bodyHash, expires_at = now() + ttl, in_flight = true)
   b. COMMIT
   c. Return StatusAcquired
5. If row exists:
   a. bodyHash mismatch → Return StatusConflict
   b. in_flight = true  → Return StatusInFlight
   c. in_flight = false → Return StatusCached{response_status, response_body}
   (no DB write needed; deferred Rollback releases row lock)
```

## Complete(key, responseStatus, responseBody)

Purpose: mark the key as done and store the response for future replays.

```
1. UPDATE SET in_flight=false, response_status=$2, response_body=$3 WHERE key=$1
2. RowsAffected == 0 → no-op (key expired and pruned; acceptable per BR-IDMP-03)
```

## Prune() → int64

Purpose: bulk delete expired records for storage hygiene.

```
1. DELETE FROM idempotency_keys WHERE expires_at <= now()
2. Return RowsAffected
```

## Error Mapping

| Condition                        | Error                            |
|----------------------------------|----------------------------------|
| key empty or > 1024 bytes        | apperror.ErrSchemaValidation     |
| DB error in Acquire/Complete     | wrapped fmt.Errorf (internal)    |
| Complete on unknown/expired key  | no error (BR-IDMP-03)            |
