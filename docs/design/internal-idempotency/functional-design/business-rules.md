# Business Rules — internal/idempotency

## BR-IDMP-01: Body hash ownership
The caller (HTTP handler) computes the body hash before invoking `Store.Acquire`.
The store receives an opaque `bodyHash string` and treats it as an equality key only.
Rationale: the store has no knowledge of request serialization or encoding — keeping hash computation at the boundary keeps the store generic.

## BR-IDMP-02: Deterministic response codes are cached; transient codes are released
`Complete` stores the response for **deterministic** outcomes — the same input would produce the same outcome on retry. In practice this means:

- **Cache (`Complete`)**: catalogued `apperror.AppError` with HTTP status `< 500`. Examples: `201` success, `200` partial success, `422` segment-overlap, `403` read-only flow, `404` flow-not-found, `400` schema-validation.
- **Release (`Release`)**: anything else — non-`AppError` errors (DB blip, ctx cancellation), `AppError` with `Status == 0` (programming bug), and any `5xx` (`ErrDependencyUnavailable`, internal-server-error). Underlying cause may resolve before the retry, so caching would falsely lock the client out of correct behaviour for `IDEMPOTENCY_KEY_TTL`.

On replay, `Acquire` returns `StatusCached` with the exact original status and body for cached outcomes; for released keys the row is gone, so `Acquire` returns `StatusAcquired` and the handler reprocesses the request.

Rationale: REQ-RATE-09 requires "return the original result" for replays. For deterministic failures (e.g. 422 overlap) the original *is* the correct result. For transient failures (e.g. 5xx), the original is **not** the correct result — the client retried precisely because they expect a different outcome. Caching transients would defeat the retry's purpose.

## BR-IDMP-03: Handlers MUST finalise every Acquired row before returning
Once `Acquire` returns `StatusAcquired`, the row is `in_flight = true` and will block subsequent retries with `StatusInFlight` (mapped to 409) until either `Complete`, `Release`, or `expires_at` runs.

Handlers must therefore call exactly one of `Complete` or `Release` on every return path past `Acquire`, and additionally install a `defer` safety net that calls `Release` with a fresh background context if the explicit finalise didn't run (panic, unhandled exit, crash inside the handler body). The defer uses a fresh context because the request context may be cancelled by the time we need it (5xx from a downstream timeout commonly cancels the parent).

Without this rule a single 5xx leaves the row in_flight for 24 hours and the client gets a misleading 409 on every retry instead of the original error — exactly the opposite of REQ-RATE-09's intent.

If `Complete`/`Release` itself fails (e.g. transient DB error during finalisation), the handler logs and lets the deferred backstop run. The primary outcome is still returned to the client; the row will eventually be released by the defer or by TTL.

## BR-IDMP-04: Abandoned in-flight records are reaped after a stale threshold
If a process crash or kernel-level kill prevents both the explicit finalise (BR-IDMP-03) and the defer backstop from running, the row stays `in_flight = true` past the point where any handler could legitimately still be running. `ReapStale(ctx, threshold)` deletes all such rows in a single `DELETE WHERE in_flight = true AND acquired_at <= now() - threshold`.

A background goroutine in `cmd/opentams/serve.go` invokes `Prune` (TTL-expired rows) and `ReapStale` (orphaned in-flight rows) every `IDEMPOTENCY_REAPER_INTERVAL` with up to ±25% jitter. The TTL `Prune` cleans completed-and-cached rows; `ReapStale` is the recovery path for crashed-mid-flight rows.

**Threshold contract**: `IDEMPOTENCY_STALE_THRESHOLD` MUST be `>= http.Server.WriteTimeout`. A genuinely-running request can hold its row in_flight for the full WriteTimeout window; reaping faster than that races the handler's own `Complete`/`Release` and silently drops legitimate work. The serve loop enforces this clamp at startup — if the configured value is smaller it is silently raised to `WriteTimeout` and a warning is logged. Default: `60s` (matches the default WriteTimeout).

After reap, `Acquire` treats the key as new and issues `StatusAcquired` to the next caller — exactly as it would after a `Release`. A client retry within `[acquired_at, acquired_at + threshold)` after the crash still receives 409, but only for one reaper interval at most. With defaults (`threshold=60s`, `interval=1m`), worst-case lockout after a crash is ~2 minutes, down from the 1h TTL backstop the table-level row otherwise relies on.

Concurrent ReapStale calls across HA replicas are race-safe: the DELETE is atomic and a row reaped by replica A is simply absent for replica B (no error). Jitter avoids thundering-herd DELETE storms when an entire fleet starts at the same instant.

Rationale: BR-IDMP-03's defer Release is sufficient when the Go runtime gets to run deferred functions. It is *not* sufficient under hard process death (OOM-kill, kernel panic, SIGKILL) — the Go runtime never gets to schedule the defer, so the row survives in_flight with nothing pointing at it. Without a reaper, every such crash translates to a `IDEMPOTENCY_KEY_TTL`-long client lockout for that key. The reaper closes the gap; the WriteTimeout clamp is what makes it safe to do so without racing live handlers. Storing `acquired_at` (vs. computing it from `created_at` or `expires_at - TTL`) keeps the reaper's `WHERE` predicate index-friendly via a partial index over the in-flight subset only — important because the in-flight set is the only thing the reaper ever scans, and the full table is dominated by completed/cached rows.

## BR-IDMP-05: Correctness via TTL check in Acquire; periodic Prune+Reap for storage hygiene
`Acquire` proactively deletes expired records at the start of each call (lazy expiry). This ensures correctness without requiring Prune to have run first.
`Prune` removes expired records (`expires_at <= now()`) in bulk for storage hygiene. `ReapStale` removes orphaned in-flight rows (`in_flight = true AND acquired_at <= now() - threshold`). Both are called periodically by the background reaper goroutine in `cmd/opentams/serve.go` — not by the store itself.

## BR-IDMP-06: Key format — any non-empty string, max 255 characters
The store accepts any non-empty string as a key. No UUID or format validation is applied.
Maximum enforced length: **255 characters**. Exceeding this returns `apperror.ErrSchemaValidation`. The HTTP handler validates `X-Idempotency-Key` presence (returns `ErrMissingIdempotencyKey` on absent header). The store enforces max length on `Acquire`, `Complete`, and `Release`.
Rationale: 255 chars is the standard VARCHAR limit that fits within a single B-tree index page entry. Clients typically use UUIDs (36 chars) so this is never a practical constraint.

## BR-IDMP-07: Eventual consistency for Complete/retry race
If a retry arrives concurrently with `Complete` (the original request just finished), the retry may receive `StatusInFlight` (409) before `Complete` commits. On the next retry the caller will receive `StatusCached`. This transient 409 followed by replay is acceptable per acceptance criterion 29.

## BR-IDMP-08: Key uniqueness scope
Keys are globally unique within the store (no per-flow or per-user namespace). The caller is responsible for generating sufficiently unique keys (e.g., UUIDs per logical write operation).

## BR-IDMP-09: Release semantics — full row deletion
`Release(ctx, key)` deletes the row entirely (`DELETE WHERE key = $1`), not a state transition to `in_flight = false`. After Release the next `Acquire` with the same key — even with a different body hash — succeeds with `StatusAcquired` as if the key had never been used.
Rationale: the row's only purpose post-Acquire is to gate replays; once we've decided this attempt should not be replayed (transient failure), the row carries no useful information and full deletion is simpler than maintaining a third state.
Consequence (intentional): calling Release after Complete erases the cached response. Handlers should never do this in the normal flow (see BR-IDMP-03), but the defer safety net's "if not finalised, Release" check prevents the misordered case.
