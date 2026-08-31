# Domain Entities — internal/idempotency

## IdempotencyRecord

Represents one in-flight or completed idempotency claim.

| Field           | Type             | Nullable | Notes                                      |
|----------------|------------------|----------|--------------------------------------------|
| key            | string           | no       | Client-provided idempotency key, max 255 chars |
| body_hash      | string           | no       | Caller-computed hash of request body       |
| in_flight      | bool             | no       | true = processing; false = complete        |
| response_status| int              | yes      | HTTP status of completed response          |
| response_body  | json.RawMessage  | yes      | Serialized response body; NULL when in_flight |
| created_at     | time.Time        | no       | When the record was first created          |
| expires_at     | time.Time        | no       | Record is logically dead after this point  |

## Lifecycle States

```
[new request]
      |
      v
   IN_FLIGHT  ──(Complete called)──> COMPLETED
      |
      | (TTL expires, Complete never called)
      v
   [logically expired — deleted on next Acquire or Prune]
```

## AcquireResult (output value object)

Returned by `Store.Acquire`. Not persisted.

| Field  | Type            | Notes                                              |
|--------|-----------------|--------------------------------------------------- |
| Status | Status (int)    | StatusAcquired / StatusInFlight / StatusCached / StatusConflict |
| Cached | *CachedResponse | Non-nil only when Status == StatusCached           |

## CachedResponse (value object)

| Field  | Type            | Notes                       |
|--------|-----------------|----------------------------|
| Status | int             | Original HTTP response code |
| Body   | json.RawMessage | Original response body      |
