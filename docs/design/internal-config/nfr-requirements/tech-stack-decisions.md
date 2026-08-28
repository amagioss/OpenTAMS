---
unit: M3 internal/config
stage: NFR Requirements
status: Complete
---

# Tech Stack Decisions — internal/config

## Env-var loading: os.Getenv (stdlib)

**Decision**: `os.Getenv` + hand-rolled parsing.
**Rationale**: 28 fixed parameters is a bounded, stable set. No struct-tag magic needed. Cross-field validation rules (`APP_ENV` relaxing auth, TLS pair, key pair) don't map cleanly to library validators. Zero external dependency.
**Rejected**: `github.com/kelseyhightower/envconfig`, `github.com/caarlos0/env` — external deps with struct-tag approach that saves little for a fixed schema with custom cross-field rules.

## Multi-error collection: errors.Join (stdlib, Go 1.20+)

**Decision**: `errors.Join` — caller owns presentation/formatting.
**Rationale**: stdlib, idiomatic since Go 1.20, works with `errors.Is`/`errors.As`. Presentation (formatting for operator output) belongs in the cmd layer, not in `Load()`.
**Rejected**: `fmt.Errorf` wrap with header (presentation in wrong layer); custom multi-error type (unnecessary complexity).

## Validation: hand-rolled

**Decision**: Hand-rolled validation logic inline in `Load()`.
**Rationale**: Cross-field rules and the conditional auth requirement don't map to struct-tag validators. All validation visible in one place.
**Rejected**: `go-playground/validator` — struct tags can't express conditional required fields or cross-field pair rules cleanly.
