// Package config loads and validates OpenTAMS configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all validated runtime configuration for OpenTAMS.
type Config struct {
	// Server
	ServerPort                   int
	ServerTLSCertFile            string
	ServerTLSKeyFile             string
	ServerGracefulShutdownPeriod time.Duration
	ServerRateLimitRPS           int
	ServerRateLimitBurst         int

	// DB
	DBHost     string
	DBPort     int
	DBName     string
	DBUser     string
	DBPassword string
	DBSSLMode  string
	DBPoolMin  int
	DBPoolMax  int

	// Object store
	ObjectStoreBucket          string
	ObjectStoreRegion          string
	ObjectStoreEndpoint        string
	ObjectStoreAccessKeyID     string
	ObjectStoreSecretAccessKey string
	ObjectStorePresignExpiry   time.Duration
	StorageBackendProvider     string
	StorageBackendProduct      string

	// Auth
	AuthExternalIssuerURL string
	AuthExternalAudience  string
	AuthInternalIssuerURL string
	AuthInternalAudience  string
	// AuthInternalAllowedSubjects is the allowlist of exact "sub" claims (e.g.
	// system:serviceaccount:<ns>:<name>) permitted on internal-issuer tokens.
	AuthInternalAllowedSubjects []string
	AuthJWKSTTL                 time.Duration

	// App
	AppEnv                    string
	LogLevel                  string
	IdempotencyKeyTTL         time.Duration
	IdempotencyStaleThreshold time.Duration
	IdempotencyReaperInterval time.Duration

	// GC
	GCPollInterval time.Duration
	GCBatchSize    int
}

// Load reads all environment variables, applies defaults, validates, and returns
// a Config. All validation errors are collected and returned together via errors.Join.
func Load() (*Config, error) {
	var errs []error

	add := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	c := &Config{}

	var err error

	c.ServerPort, err = getInt("SERVER_PORT", 8080)
	add(err)
	c.ServerTLSCertFile = getString("SERVER_TLS_CERT_FILE", "")
	c.ServerTLSKeyFile = getString("SERVER_TLS_KEY_FILE", "")
	c.ServerGracefulShutdownPeriod, err = getDuration("SERVER_GRACEFUL_SHUTDOWN_PERIOD", 30*time.Second)
	add(err)
	c.ServerRateLimitRPS, err = getInt("SERVER_RATE_LIMIT_RPS", 1000)
	add(err)
	c.ServerRateLimitBurst, err = getInt("SERVER_RATE_LIMIT_BURST", 100)
	add(err)

	c.DBHost = getString("DB_HOST", "")
	c.DBPort, err = getInt("DB_PORT", 5432)
	add(err)
	c.DBName = getString("DB_NAME", "")
	c.DBUser = getString("DB_USER", "")
	c.DBPassword = getString("DB_PASSWORD", "")
	c.DBPoolMin, err = getInt("DB_POOL_MIN", 2)
	add(err)
	c.DBPoolMax, err = getInt("DB_POOL_MAX", 10)
	add(err)

	c.ObjectStoreBucket = getString("OBJECT_STORE_BUCKET", "")
	c.ObjectStoreRegion = getString("OBJECT_STORE_REGION", "")
	c.ObjectStoreEndpoint = getString("OBJECT_STORE_ENDPOINT", "")
	c.ObjectStoreAccessKeyID = getString("OBJECT_STORE_ACCESS_KEY_ID", "")
	c.ObjectStoreSecretAccessKey = getString("OBJECT_STORE_SECRET_ACCESS_KEY", "")
	c.ObjectStorePresignExpiry, err = getDuration("OBJECT_STORE_PRESIGN_EXPIRY", time.Hour)
	add(err)
	c.StorageBackendProvider = getString("STORAGE_BACKEND_PROVIDER", "")
	c.StorageBackendProduct = getString("STORAGE_BACKEND_PRODUCT", "s3")

	c.AuthExternalIssuerURL = getString("AUTH_EXTERNAL_ISSUER_URL", "")
	c.AuthExternalAudience = getString("AUTH_EXTERNAL_AUDIENCE", "")
	c.AuthInternalIssuerURL = getString("AUTH_INTERNAL_ISSUER_URL", "")
	c.AuthInternalAudience = getString("AUTH_INTERNAL_AUDIENCE", "")
	c.AuthInternalAllowedSubjects = getStringSlice("AUTH_INTERNAL_ALLOWED_SUBJECTS")
	c.AuthJWKSTTL, err = getDuration("AUTH_JWKS_TTL", 15*time.Minute)
	add(err)

	c.AppEnv, err = getEnum("APP_ENV", "production", []string{"production", "development"})
	add(err)
	c.LogLevel, err = getEnum("LOG_LEVEL", "info", []string{"debug", "info", "warn", "error"})
	add(err)

	// DB_SSLMODE default depends on AppEnv: "require" in production (fail
	// closed on plaintext PG) and "prefer" in development (libpq's classic
	// default — TLS if the server offers it, plaintext otherwise — so a
	// local Postgres without TLS still works).
	sslDefault := "require"
	if c.AppEnv == "development" {
		sslDefault = "prefer"
	}
	c.DBSSLMode, err = getEnum("DB_SSLMODE", sslDefault,
		[]string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"})
	add(err)

	c.IdempotencyKeyTTL, err = getDuration("IDEMPOTENCY_KEY_TTL", time.Hour)
	add(err)
	c.IdempotencyStaleThreshold, err = getDuration("IDEMPOTENCY_STALE_THRESHOLD", 60*time.Second)
	add(err)
	c.IdempotencyReaperInterval, err = getDuration("IDEMPOTENCY_REAPER_INTERVAL", time.Minute)
	add(err)

	c.GCPollInterval, err = getDuration("GC_POLL_INTERVAL", 5*time.Minute)
	add(err)
	c.GCBatchSize, err = getInt("GC_BATCH_SIZE", 100)
	add(err)

	// Required fields
	if c.DBHost == "" {
		errs = append(errs, errors.New("DB_HOST is required"))
	}
	if c.DBName == "" {
		errs = append(errs, errors.New("DB_NAME is required"))
	}
	if c.DBUser == "" {
		errs = append(errs, errors.New("DB_USER is required"))
	}
	if c.DBPassword == "" {
		errs = append(errs, errors.New("DB_PASSWORD is required"))
	}
	if c.ObjectStoreBucket == "" {
		errs = append(errs, errors.New("OBJECT_STORE_BUCKET is required"))
	}
	if c.ObjectStoreRegion == "" {
		errs = append(errs, errors.New("OBJECT_STORE_REGION is required"))
	}
	if c.StorageBackendProvider == "" {
		errs = append(errs, errors.New("STORAGE_BACKEND_PROVIDER is required"))
	}

	// External auth: required in production. The external issuer authenticates
	// human/API clients and is the primary authentication surface. AppEnv default
	// is "production", so guard on non-development.
	if c.AppEnv != "development" {
		if c.AuthExternalIssuerURL == "" {
			errs = append(errs, errors.New("AUTH_EXTERNAL_ISSUER_URL is required in production"))
		}
		if c.AuthExternalAudience == "" {
			errs = append(errs, errors.New("AUTH_EXTERNAL_AUDIENCE is required in production"))
		}
	}

	// Internal auth: optional. When configured, it accepts service-to-service
	// JWTs from a second issuer (e.g. Kubernetes ServiceAccount tokens, an M2M
	// IdP). Plain Docker / VM deployments often have no separate internal
	// issuer — clients authenticate via the external IdP regardless of network
	// origin. Both-or-neither: if exactly one of the two fields is set, that
	// is a misconfiguration.
	if (c.AuthInternalIssuerURL == "") != (c.AuthInternalAudience == "") {
		if c.AuthInternalIssuerURL == "" {
			errs = append(errs, errors.New("AUTH_INTERNAL_ISSUER_URL is required when AUTH_INTERNAL_AUDIENCE is set"))
		} else {
			errs = append(errs, errors.New("AUTH_INTERNAL_AUDIENCE is required when AUTH_INTERNAL_ISSUER_URL is set"))
		}
	}
	// When the internal issuer is enabled, an explicit subject allowlist is
	// mandatory: it fails closed so a misconfiguration can never grant every
	// internal-issuer token blanket access.
	if c.AuthInternalIssuerURL != "" && len(c.AuthInternalAllowedSubjects) == 0 {
		errs = append(errs, errors.New("AUTH_INTERNAL_ALLOWED_SUBJECTS is required when AUTH_INTERNAL_ISSUER_URL is set"))
	}

	// Port ranges
	if c.ServerPort < 1 || c.ServerPort > 65535 {
		errs = append(errs, fmt.Errorf("SERVER_PORT: %d is not a valid port (1–65535)", c.ServerPort))
	}
	if c.DBPort < 1 || c.DBPort > 65535 {
		errs = append(errs, fmt.Errorf("DB_PORT: %d is not a valid port (1–65535)", c.DBPort))
	}

	// Idempotency timing must be positive; cross-coupling with server WriteTimeout
	// is enforced at runtime startup (we cannot import server here without a cycle).
	if c.IdempotencyKeyTTL <= 0 {
		errs = append(errs, fmt.Errorf("IDEMPOTENCY_KEY_TTL must be positive, got %v", c.IdempotencyKeyTTL))
	}
	if c.IdempotencyStaleThreshold <= 0 {
		errs = append(errs, fmt.Errorf("IDEMPOTENCY_STALE_THRESHOLD must be positive, got %v", c.IdempotencyStaleThreshold))
	}
	if c.IdempotencyReaperInterval <= 0 {
		errs = append(errs, fmt.Errorf("IDEMPOTENCY_REAPER_INTERVAL must be positive, got %v", c.IdempotencyReaperInterval))
	}
	if c.IdempotencyStaleThreshold >= c.IdempotencyKeyTTL {
		errs = append(errs, fmt.Errorf("IDEMPOTENCY_STALE_THRESHOLD (%v) must be less than IDEMPOTENCY_KEY_TTL (%v)",
			c.IdempotencyStaleThreshold, c.IdempotencyKeyTTL))
	}

	// Pool sizing — bounded above so the pgxpool int32 cast in
	// cmd/opentams/serve.go is safe (G115 mitigation). 100k is far
	// above any reasonable production deployment and well below
	// MaxInt32.
	if c.DBPoolMin < 1 {
		errs = append(errs, fmt.Errorf("DB_POOL_MIN must be at least 1, got %d", c.DBPoolMin))
	}
	if c.DBPoolMin >= c.DBPoolMax {
		errs = append(errs, fmt.Errorf("DB_POOL_MIN (%d) must be less than DB_POOL_MAX (%d)", c.DBPoolMin, c.DBPoolMax))
	}
	if c.DBPoolMax > 100_000 {
		errs = append(errs, fmt.Errorf("DB_POOL_MAX must not exceed 100000, got %d", c.DBPoolMax))
	}

	// Object store key pair must be set together
	if (c.ObjectStoreAccessKeyID == "") != (c.ObjectStoreSecretAccessKey == "") {
		if c.ObjectStoreAccessKeyID == "" {
			errs = append(errs, errors.New("OBJECT_STORE_ACCESS_KEY_ID is required when OBJECT_STORE_SECRET_ACCESS_KEY is set"))
		} else {
			errs = append(errs, errors.New("OBJECT_STORE_SECRET_ACCESS_KEY is required when OBJECT_STORE_ACCESS_KEY_ID is set"))
		}
	}

	// TLS cert+key must be set together
	if (c.ServerTLSCertFile == "") != (c.ServerTLSKeyFile == "") {
		if c.ServerTLSCertFile == "" {
			errs = append(errs, errors.New("SERVER_TLS_CERT_FILE is required when SERVER_TLS_KEY_FILE is set"))
		} else {
			errs = append(errs, errors.New("SERVER_TLS_KEY_FILE is required when SERVER_TLS_CERT_FILE is set"))
		}
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return c, nil
}

// Redacted returns a shallow copy of c with secret values replaced by "[redacted]",
// safe for structured logging at startup (REQ-USE-04).
func (c *Config) Redacted() *Config {
	r := *c
	r.DBPassword = "[redacted]"
	r.ObjectStoreAccessKeyID = "[redacted]"
	r.ObjectStoreSecretAccessKey = "[redacted]"
	return &r
}

func getString(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// getStringSlice parses a comma-separated env var into a slice, trimming
// whitespace and dropping empty entries. Returns nil when unset or all-empty.
func getStringSlice(key string) []string {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(v, ",") {
		if s := strings.TrimSpace(part); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func getInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid integer %q", key, v)
	}
	return n, nil
}

func getDuration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration %q", key, v)
	}
	return d, nil
}

func getEnum(key, def string, valid []string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	for _, vv := range valid {
		if v == vv {
			return v, nil
		}
	}
	return def, fmt.Errorf("%s: invalid value %q, must be one of %v", key, v, valid)
}
