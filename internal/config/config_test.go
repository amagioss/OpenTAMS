package config_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/amagioss/opentams/internal/config"
)

// setRequiredEnv sets the minimum environment for a valid production Config.
func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DB_HOST", "db.example.com")
	t.Setenv("DB_NAME", "opentams")
	t.Setenv("DB_USER", "opentams")
	t.Setenv("DB_PASSWORD", "s3cr3t")
	t.Setenv("OBJECT_STORE_BUCKET", "my-bucket")
	t.Setenv("OBJECT_STORE_REGION", "us-east-1")
	t.Setenv("STORAGE_BACKEND_PROVIDER", "aws")
	t.Setenv("AUTH_EXTERNAL_ISSUER_URL", "https://external.issuer.example.com")
	t.Setenv("AUTH_EXTERNAL_AUDIENCE", "external-audience")
	t.Setenv("AUTH_INTERNAL_ISSUER_URL", "https://internal.issuer.example.com")
	t.Setenv("AUTH_INTERNAL_AUDIENCE", "internal-audience")
	t.Setenv("AUTH_INTERNAL_ALLOWED_SUBJECTS", "system:serviceaccount:opentams:loader")
}

// TC-CFG-01: minimum required env set → Load succeeds with correct types and defaults
func TestLoad_ValidMinimum(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	// required fields
	if cfg.DBHost != "db.example.com" {
		t.Errorf("DBHost = %q, want %q", cfg.DBHost, "db.example.com")
	}
	if cfg.DBPassword != "s3cr3t" {
		t.Errorf("DBPassword = %q, want %q", cfg.DBPassword, "s3cr3t")
	}

	// defaults
	if cfg.ServerPort != 8080 {
		t.Errorf("ServerPort = %d, want 8080", cfg.ServerPort)
	}
	if cfg.DBPort != 5432 {
		t.Errorf("DBPort = %d, want 5432", cfg.DBPort)
	}
	if cfg.DBPoolMin != 2 {
		t.Errorf("DBPoolMin = %d, want 2", cfg.DBPoolMin)
	}
	if cfg.DBPoolMax != 10 {
		t.Errorf("DBPoolMax = %d, want 10", cfg.DBPoolMax)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", cfg.LogLevel)
	}
	if cfg.AppEnv != "production" {
		t.Errorf("AppEnv = %q, want production", cfg.AppEnv)
	}
	if cfg.ServerGracefulShutdownPeriod != 30*time.Second {
		t.Errorf("ServerGracefulShutdownPeriod = %v, want 30s", cfg.ServerGracefulShutdownPeriod)
	}
	if cfg.ServerRateLimitRPS != 1000 {
		t.Errorf("ServerRateLimitRPS = %d, want 1000", cfg.ServerRateLimitRPS)
	}
	if cfg.ServerRateLimitBurst != 100 {
		t.Errorf("ServerRateLimitBurst = %d, want 100", cfg.ServerRateLimitBurst)
	}
	if cfg.ObjectStorePresignExpiry != time.Hour {
		t.Errorf("ObjectStorePresignExpiry = %v, want 1h", cfg.ObjectStorePresignExpiry)
	}
	if cfg.AuthJWKSTTL != 15*time.Minute {
		t.Errorf("AuthJWKSTTL = %v, want 15m", cfg.AuthJWKSTTL)
	}
	if cfg.IdempotencyKeyTTL != time.Hour {
		t.Errorf("IdempotencyKeyTTL = %v, want 1h", cfg.IdempotencyKeyTTL)
	}
	if cfg.IdempotencyStaleThreshold != 60*time.Second {
		t.Errorf("IdempotencyStaleThreshold = %v, want 60s", cfg.IdempotencyStaleThreshold)
	}
	if cfg.IdempotencyReaperInterval != time.Minute {
		t.Errorf("IdempotencyReaperInterval = %v, want 1m", cfg.IdempotencyReaperInterval)
	}
	if cfg.GCPollInterval != 5*time.Minute {
		t.Errorf("GCPollInterval = %v, want 5m", cfg.GCPollInterval)
	}
	if cfg.GCBatchSize != 100 {
		t.Errorf("GCBatchSize = %d, want 100", cfg.GCBatchSize)
	}
	// DB_SSLMODE defaults to "require" in production (setRequiredEnv leaves
	// APP_ENV unset, so AppEnv resolves to its production default).
	if cfg.DBSSLMode != "require" {
		t.Errorf("DBSSLMode = %q, want require (production default)", cfg.DBSSLMode)
	}
}

// TC-CFG-02: missing DB_HOST → error names the field
func TestLoad_MissingDBHost(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("DB_HOST", "")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() expected error for missing DB_HOST, got nil")
	}
	if !strings.Contains(err.Error(), "DB_HOST") {
		t.Errorf("error %q does not mention DB_HOST", err.Error())
	}
}

// TC-CFG-03: multiple missing required fields → all errors returned together
func TestLoad_MultipleRequiredMissing(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("DB_NAME", "")
	t.Setenv("DB_USER", "")
	t.Setenv("DB_PASSWORD", "")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() expected error for missing fields, got nil")
	}

	var errs interface{ Unwrap() []error }
	if !errors.As(err, &errs) {
		t.Fatalf("expected joined error, got %T: %v", err, err)
	}
	if len(errs.Unwrap()) < 3 {
		t.Errorf("expected at least 3 errors, got %d: %v", len(errs.Unwrap()), errs.Unwrap())
	}
}

// TC-CFG-04: APP_ENV=production + missing auth fields → errors returned
func TestLoad_ProductionMissingAuth(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("AUTH_EXTERNAL_ISSUER_URL", "")
	t.Setenv("AUTH_INTERNAL_ISSUER_URL", "")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() expected error for missing auth fields in production, got nil")
	}
	if !strings.Contains(err.Error(), "AUTH_EXTERNAL_ISSUER_URL") {
		t.Errorf("error %q does not mention AUTH_EXTERNAL_ISSUER_URL", err.Error())
	}
}

// TC-CFG-05: APP_ENV=development + missing auth fields → success
func TestLoad_DevelopmentAuthNotRequired(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("APP_ENV", "development")
	t.Setenv("AUTH_EXTERNAL_ISSUER_URL", "")
	t.Setenv("AUTH_EXTERNAL_AUDIENCE", "")
	t.Setenv("AUTH_INTERNAL_ISSUER_URL", "")
	t.Setenv("AUTH_INTERNAL_AUDIENCE", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() unexpected error in development mode: %v", err)
	}
	if cfg.AppEnv != "development" {
		t.Errorf("AppEnv = %q, want development", cfg.AppEnv)
	}
}

// TC-CFG-06: access key set without secret → error; secret set without key → error
func TestLoad_ObjectStoreKeyPairRequired(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("OBJECT_STORE_ACCESS_KEY_ID", "mykey")
	t.Setenv("OBJECT_STORE_SECRET_ACCESS_KEY", "")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error when ACCESS_KEY_ID set without SECRET, got nil")
	}
	if !strings.Contains(err.Error(), "OBJECT_STORE_SECRET_ACCESS_KEY") {
		t.Errorf("error %q does not mention OBJECT_STORE_SECRET_ACCESS_KEY", err.Error())
	}

	// reverse: secret without key
	t.Setenv("OBJECT_STORE_ACCESS_KEY_ID", "")
	t.Setenv("OBJECT_STORE_SECRET_ACCESS_KEY", "mysecret")

	_, err = config.Load()
	if err == nil {
		t.Fatal("expected error when SECRET set without ACCESS_KEY_ID, got nil")
	}
	if !strings.Contains(err.Error(), "OBJECT_STORE_ACCESS_KEY_ID") {
		t.Errorf("error %q does not mention OBJECT_STORE_ACCESS_KEY_ID", err.Error())
	}
}

// TC-CFG-07: TLS cert set without key → error; key without cert → error
func TestLoad_TLSPairRequired(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("SERVER_TLS_CERT_FILE", "/path/to/cert.pem")
	t.Setenv("SERVER_TLS_KEY_FILE", "")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error when TLS_CERT_FILE set without TLS_KEY_FILE, got nil")
	}
	if !strings.Contains(err.Error(), "SERVER_TLS_KEY_FILE") {
		t.Errorf("error %q does not mention SERVER_TLS_KEY_FILE", err.Error())
	}

	// reverse
	t.Setenv("SERVER_TLS_CERT_FILE", "")
	t.Setenv("SERVER_TLS_KEY_FILE", "/path/to/key.pem")

	_, err = config.Load()
	if err == nil {
		t.Fatal("expected error when TLS_KEY_FILE set without TLS_CERT_FILE, got nil")
	}
	if !strings.Contains(err.Error(), "SERVER_TLS_CERT_FILE") {
		t.Errorf("error %q does not mention SERVER_TLS_CERT_FILE", err.Error())
	}
}

// TC-CFG-08: invalid APP_ENV → error names field and valid values
func TestLoad_InvalidAppEnv(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("APP_ENV", "staging")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() expected error for invalid APP_ENV, got nil")
	}
	if !strings.Contains(err.Error(), "APP_ENV") {
		t.Errorf("error %q does not mention APP_ENV", err.Error())
	}
}

// TC-CFG-09: invalid LOG_LEVEL → error names field
func TestLoad_InvalidLogLevel(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("LOG_LEVEL", "verbose")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() expected error for invalid LOG_LEVEL, got nil")
	}
	if !strings.Contains(err.Error(), "LOG_LEVEL") {
		t.Errorf("error %q does not mention LOG_LEVEL", err.Error())
	}
}

// TC-CFG-10: invalid duration → error names field
func TestLoad_InvalidDuration(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("SERVER_GRACEFUL_SHUTDOWN_PERIOD", "not-a-duration")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() expected error for invalid duration, got nil")
	}
	if !strings.Contains(err.Error(), "SERVER_GRACEFUL_SHUTDOWN_PERIOD") {
		t.Errorf("error %q does not mention SERVER_GRACEFUL_SHUTDOWN_PERIOD", err.Error())
	}
}

// TC-CFG-11: invalid integer → error names field
func TestLoad_InvalidInteger(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("SERVER_PORT", "not-a-port")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() expected error for invalid SERVER_PORT, got nil")
	}
	if !strings.Contains(err.Error(), "SERVER_PORT") {
		t.Errorf("error %q does not mention SERVER_PORT", err.Error())
	}
}

// TC-CFG-12: Redacted() replaces secrets, leaves non-secrets unchanged
func TestRedacted(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("OBJECT_STORE_ACCESS_KEY_ID", "myaccesskey")
	t.Setenv("OBJECT_STORE_SECRET_ACCESS_KEY", "mysecretkey")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	r := cfg.Redacted()

	if r.DBPassword != "[redacted]" {
		t.Errorf("Redacted DBPassword = %q, want [redacted]", r.DBPassword)
	}
	if r.ObjectStoreAccessKeyID != "[redacted]" {
		t.Errorf("Redacted ObjectStoreAccessKeyID = %q, want [redacted]", r.ObjectStoreAccessKeyID)
	}
	if r.ObjectStoreSecretAccessKey != "[redacted]" {
		t.Errorf("Redacted ObjectStoreSecretAccessKey = %q, want [redacted]", r.ObjectStoreSecretAccessKey)
	}
	// non-secret unchanged
	if r.DBHost != cfg.DBHost {
		t.Errorf("Redacted DBHost = %q, want %q", r.DBHost, cfg.DBHost)
	}
	// original not mutated
	if cfg.DBPassword != "s3cr3t" {
		t.Errorf("original DBPassword mutated: got %q", cfg.DBPassword)
	}
}

// TC-CFG-13a: invalid port → error names field
func TestLoad_InvalidPort(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("SERVER_PORT", "99999")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() expected error for out-of-range SERVER_PORT, got nil")
	}
	if !strings.Contains(err.Error(), "SERVER_PORT") {
		t.Errorf("error %q does not mention SERVER_PORT", err.Error())
	}

	// zero port also invalid
	t.Setenv("SERVER_PORT", "0")
	_, err = config.Load()
	if err == nil {
		t.Fatal("Load() expected error for SERVER_PORT=0, got nil")
	}
}

// TC-CFG-13b: pool min >= max → error; pool min < 1 → error; invalid DB_PORT → error
func TestLoad_InvalidPoolSizing(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("DB_POOL_MIN", "10")
	t.Setenv("DB_POOL_MAX", "5")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() expected error for DB_POOL_MIN >= DB_POOL_MAX, got nil")
	}
	if !strings.Contains(err.Error(), "DB_POOL_MIN") {
		t.Errorf("error %q does not mention DB_POOL_MIN", err.Error())
	}

	// pool min < 1
	t.Setenv("DB_POOL_MIN", "0")
	t.Setenv("DB_POOL_MAX", "10")
	_, err = config.Load()
	if err == nil {
		t.Fatal("Load() expected error for DB_POOL_MIN=0, got nil")
	}
	if !strings.Contains(err.Error(), "DB_POOL_MIN") {
		t.Errorf("error %q does not mention DB_POOL_MIN", err.Error())
	}

	// invalid DB_PORT
	t.Setenv("DB_POOL_MIN", "2")
	t.Setenv("DB_PORT", "99999")
	_, err = config.Load()
	if err == nil {
		t.Fatal("Load() expected error for out-of-range DB_PORT, got nil")
	}
	if !strings.Contains(err.Error(), "DB_PORT") {
		t.Errorf("error %q does not mention DB_PORT", err.Error())
	}
}

// TC-CFG-13d: DB_POOL_MAX > 100_000 is rejected. The bound exists so the
// downstream int(DB_POOL_MAX) -> int32 narrowing in pgxpool wiring is
// provably safe under gosec G115; this test guards the bound from being
// silently removed (which would re-introduce the int32 overflow risk).
func TestLoad_PoolMaxAboveUpperBound(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("DB_POOL_MIN", "10")
	t.Setenv("DB_POOL_MAX", "100001")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() expected error for DB_POOL_MAX > 100000, got nil")
	}
	if !strings.Contains(err.Error(), "DB_POOL_MAX") {
		t.Errorf("error %q does not mention DB_POOL_MAX", err.Error())
	}
	if !strings.Contains(err.Error(), "100000") {
		t.Errorf("error %q does not mention the 100000 limit", err.Error())
	}

	// boundary: 100_000 itself should be accepted
	t.Setenv("DB_POOL_MAX", "100000")
	if _, err := config.Load(); err != nil {
		t.Errorf("Load() with DB_POOL_MAX=100000 should be accepted, got %v", err)
	}
}

// TC-CFG-13c: missing object store identity fields → errors named
func TestLoad_MissingObjectStoreFields(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("OBJECT_STORE_BUCKET", "")
	t.Setenv("OBJECT_STORE_REGION", "")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() expected error for missing object store fields, got nil")
	}
	if !strings.Contains(err.Error(), "OBJECT_STORE_BUCKET") {
		t.Errorf("error %q does not mention OBJECT_STORE_BUCKET", err.Error())
	}
	if !strings.Contains(err.Error(), "OBJECT_STORE_REGION") {
		t.Errorf("error %q does not mention OBJECT_STORE_REGION", err.Error())
	}
}

// TC-CFG-13b: production + missing auth audience fields → errors named
func TestLoad_ProductionMissingAuthAudience(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("AUTH_EXTERNAL_AUDIENCE", "")
	t.Setenv("AUTH_INTERNAL_AUDIENCE", "")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() expected error for missing auth audience fields, got nil")
	}
	if !strings.Contains(err.Error(), "AUTH_EXTERNAL_AUDIENCE") {
		t.Errorf("error %q does not mention AUTH_EXTERNAL_AUDIENCE", err.Error())
	}
	if !strings.Contains(err.Error(), "AUTH_INTERNAL_AUDIENCE") {
		t.Errorf("error %q does not mention AUTH_INTERNAL_AUDIENCE", err.Error())
	}
}

// TC-CFG-13: non-default values parsed correctly
func TestLoad_NonDefaultValues(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("SERVER_PORT", "9090")
	t.Setenv("DB_PORT", "5433")
	t.Setenv("DB_POOL_MIN", "5")
	t.Setenv("DB_POOL_MAX", "20")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("SERVER_GRACEFUL_SHUTDOWN_PERIOD", "60s")
	t.Setenv("OBJECT_STORE_PRESIGN_EXPIRY", "2h")
	t.Setenv("GC_BATCH_SIZE", "200")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if cfg.ServerPort != 9090 {
		t.Errorf("ServerPort = %d, want 9090", cfg.ServerPort)
	}
	if cfg.DBPort != 5433 {
		t.Errorf("DBPort = %d, want 5433", cfg.DBPort)
	}
	if cfg.DBPoolMin != 5 {
		t.Errorf("DBPoolMin = %d, want 5", cfg.DBPoolMin)
	}
	if cfg.DBPoolMax != 20 {
		t.Errorf("DBPoolMax = %d, want 20", cfg.DBPoolMax)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
	if cfg.ServerGracefulShutdownPeriod != 60*time.Second {
		t.Errorf("ServerGracefulShutdownPeriod = %v, want 60s", cfg.ServerGracefulShutdownPeriod)
	}
	if cfg.ObjectStorePresignExpiry != 2*time.Hour {
		t.Errorf("ObjectStorePresignExpiry = %v, want 2h", cfg.ObjectStorePresignExpiry)
	}
	if cfg.GCBatchSize != 200 {
		t.Errorf("GCBatchSize = %d, want 200", cfg.GCBatchSize)
	}
}

// TC-CFG-14: IDEMPOTENCY_STALE_THRESHOLD must be < IDEMPOTENCY_KEY_TTL.
//
// A threshold >= TTL would never fire (TTL-Prune deletes the row first), so
// the reaper would silently never recover orphaned in_flight rows.
func TestLoad_StaleThresholdMustBeLessThanKeyTTL(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("IDEMPOTENCY_KEY_TTL", "60s")
	t.Setenv("IDEMPOTENCY_STALE_THRESHOLD", "60s")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() expected error when STALE_THRESHOLD >= KEY_TTL, got nil")
	}
	if !strings.Contains(err.Error(), "IDEMPOTENCY_STALE_THRESHOLD") {
		t.Errorf("error %q does not mention IDEMPOTENCY_STALE_THRESHOLD", err.Error())
	}
}

// TC-CFG-15: each idempotency duration must be positive.
func TestLoad_IdempotencyDurationsMustBePositive(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
	}{
		{"IDEMPOTENCY_KEY_TTL", "IDEMPOTENCY_KEY_TTL"},
		{"IDEMPOTENCY_STALE_THRESHOLD", "IDEMPOTENCY_STALE_THRESHOLD"},
		{"IDEMPOTENCY_REAPER_INTERVAL", "IDEMPOTENCY_REAPER_INTERVAL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv(tc.env, "0s")

			_, err := config.Load()
			if err == nil {
				t.Fatalf("Load() expected error when %s = 0, got nil", tc.env)
			}
			if !strings.Contains(err.Error(), tc.env) {
				t.Errorf("error %q does not mention %s", err.Error(), tc.env)
			}
		})
	}
}

// TC-CFG-16: AUTH_INTERNAL_* is optional in production. Plain Docker / VM
// deployments often have no separate service-to-service IdP; the external
// issuer is the sole authentication surface in that topology.
func TestLoad_InternalAuthOptionalInProduction(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("AUTH_INTERNAL_ISSUER_URL", "")
	t.Setenv("AUTH_INTERNAL_AUDIENCE", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() unexpected error when internal auth pair is empty: %v", err)
	}
	if cfg.AuthInternalIssuerURL != "" {
		t.Errorf("AuthInternalIssuerURL = %q, want empty", cfg.AuthInternalIssuerURL)
	}
	if cfg.AuthInternalAudience != "" {
		t.Errorf("AuthInternalAudience = %q, want empty", cfg.AuthInternalAudience)
	}
}

// TC-CFG-17: AUTH_INTERNAL_* is both-or-neither. Setting only one half
// is a misconfiguration whether in production or development — the
// validator cannot register a partial issuer.
func TestLoad_InternalAuthBothOrNeither(t *testing.T) {
	t.Run("issuer without audience", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("AUTH_INTERNAL_AUDIENCE", "")

		_, err := config.Load()
		if err == nil {
			t.Fatal("Load() expected error when AUTH_INTERNAL_ISSUER_URL set without AUDIENCE, got nil")
		}
		if !strings.Contains(err.Error(), "AUTH_INTERNAL_AUDIENCE") {
			t.Errorf("error %q does not mention AUTH_INTERNAL_AUDIENCE", err.Error())
		}
	})

	t.Run("audience without issuer", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("AUTH_INTERNAL_ISSUER_URL", "")

		_, err := config.Load()
		if err == nil {
			t.Fatal("Load() expected error when AUTH_INTERNAL_AUDIENCE set without ISSUER_URL, got nil")
		}
		if !strings.Contains(err.Error(), "AUTH_INTERNAL_ISSUER_URL") {
			t.Errorf("error %q does not mention AUTH_INTERNAL_ISSUER_URL", err.Error())
		}
	})
}

// TC-CFG-17b: when the internal issuer is enabled, the subject allowlist is
// mandatory — it fails closed rather than granting every internal token access.
func TestLoad_InternalAuthRequiresAllowlist(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("AUTH_INTERNAL_ALLOWED_SUBJECTS", "")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() expected error when AUTH_INTERNAL_ISSUER_URL set without ALLOWED_SUBJECTS, got nil")
	}
	if !strings.Contains(err.Error(), "AUTH_INTERNAL_ALLOWED_SUBJECTS") {
		t.Errorf("error %q does not mention AUTH_INTERNAL_ALLOWED_SUBJECTS", err.Error())
	}
}

// TC-CFG-17c: AUTH_INTERNAL_ALLOWED_SUBJECTS is comma-separated; whitespace is
// trimmed and empty entries are dropped.
func TestLoad_InternalAllowedSubjectsParsing(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("AUTH_INTERNAL_ALLOWED_SUBJECTS", " system:serviceaccount:opentams:loader , ,system:serviceaccount:opentams:reaper ")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	want := []string{"system:serviceaccount:opentams:loader", "system:serviceaccount:opentams:reaper"}
	if len(cfg.AuthInternalAllowedSubjects) != len(want) {
		t.Fatalf("AuthInternalAllowedSubjects = %v, want %v", cfg.AuthInternalAllowedSubjects, want)
	}
	for i, s := range want {
		if cfg.AuthInternalAllowedSubjects[i] != s {
			t.Errorf("AuthInternalAllowedSubjects[%d] = %q, want %q", i, cfg.AuthInternalAllowedSubjects[i], s)
		}
	}
}

// TC-CFG-18: DB_SSLMODE defaults to "prefer" in development to match the
// classic libpq behaviour (TLS if offered, plaintext otherwise) so a local
// Postgres without TLS still works.
func TestLoad_DBSSLModeDefaultDevelopment(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("APP_ENV", "development")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if cfg.DBSSLMode != "prefer" {
		t.Errorf("DBSSLMode = %q, want prefer (development default)", cfg.DBSSLMode)
	}
}

// TC-CFG-19: DB_SSLMODE accepts each libpq sslmode value.
func TestLoad_DBSSLModeAcceptsAllValidValues(t *testing.T) {
	for _, mode := range []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"} {
		t.Run(mode, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("DB_SSLMODE", mode)

			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load() unexpected error for DB_SSLMODE=%s: %v", mode, err)
			}
			if cfg.DBSSLMode != mode {
				t.Errorf("DBSSLMode = %q, want %q", cfg.DBSSLMode, mode)
			}
		})
	}
}

// TC-CFG-20: DB_SSLMODE rejects unknown values.
func TestLoad_DBSSLModeInvalid(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("DB_SSLMODE", "yolo")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() expected error for invalid DB_SSLMODE, got nil")
	}
	if !strings.Contains(err.Error(), "DB_SSLMODE") {
		t.Errorf("error %q does not mention DB_SSLMODE", err.Error())
	}
}
