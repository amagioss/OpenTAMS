# Tech Stack Decisions — M11 Middleware

## Dependencies

| Package | Dependency | Version | Rationale |
|---|---|---|---|
| `pkg/requestid` | `github.com/google/uuid` | existing | UUID v4 generation; already in go.mod |
| `pkg/requestid/ginadapter` | `github.com/gin-gonic/gin` | new | Gin adapter; pin exact version in go.mod |
| `pkg/httplog` | `go.uber.org/zap` | existing | Structured logging; already in go.mod |
| `pkg/httplog` | `github.com/gin-gonic/gin` | new | |
| `pkg/httpmetrics` | `github.com/prometheus/client_golang` | existing | Metrics; already in go.mod |
| `pkg/httpmetrics` | `github.com/gin-gonic/gin` | new | |
| `pkg/httprecovery` | stdlib `net/http` only | — | Agnostic core; zero new dependencies |
| `pkg/httprecovery/ginadapter` | `github.com/gin-gonic/gin`, `go.uber.org/zap` | new/existing | |
| `internal/httpx/middleware` | `github.com/gin-gonic/gin` | new | |
| `internal/httpx/middleware` | `internal/auth`, `internal/apperror` | internal | |
| `internal/httpx/middleware` | `pkg/httprecovery` | internal pkg | PanicWriter uses ResponseWriter contract |

## Testing

| Decision | Choice | Rationale |
|---|---|---|
| Test engine | `gin.New()` + `httptest.NewRecorder()` | No real server; full middleware chain testable |
| Auth mock | Hand-rolled `mockProvider` with function field | Consistent with M8–M10 pattern |
| Coverage target | 100% statement coverage | REQ-TEST-02 |

## Gin version pinning

Pin `github.com/gin-gonic/gin` to an exact version in `go.mod`. All three Gin-dependent packages (`pkg/requestid/ginadapter`, `pkg/httplog`, `pkg/httpmetrics`, `pkg/httprecovery/ginadapter`, `internal/httpx/middleware`) share the same version via the module graph — no per-package pinning needed.
