package server

import "errors"

// Sentinel errors returned by New on misconfiguration. Callers (M15
// cmd/opentams) should treat any non-nil error from New as fatal —
// the server has no usable state if construction failed.
var (
	ErrMissingConfig     = errors.New("server: Config is required")
	ErrMissingLogger     = errors.New("server: Logger is required")
	ErrMissingHandlers   = errors.New("server: Handlers is required")
	ErrMissingChecker    = errors.New("server: Checker is required")
	ErrMissingAuth       = errors.New("server: AuthProvider is required")
	ErrMissingGatherer   = errors.New("server: Gatherer is required")
	ErrMissingRegisterer = errors.New("server: HTTPMetricsRegisterer is required")
	ErrInvalidTLSConfig  = errors.New("server: both ServerTLSCertFile and ServerTLSKeyFile must be set together, or neither")
)
