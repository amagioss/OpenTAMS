package requestid

import (
	"context"

	"github.com/google/uuid"
)

// Header is the HTTP header carrying the request correlation ID
// across servers and clients.
const Header = "X-Request-ID"

type contextKey struct{}

// Generate returns a fresh request ID suitable for the X-Request-ID
// header. The current implementation returns a UUIDv4 string.
func Generate() string {
	return uuid.New().String()
}

// WithContext returns a derived context carrying the supplied request
// ID. Use FromContext to read it back.
func WithContext(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// FromContext returns the request ID previously stored via
// WithContext. Returns the empty string if no ID is attached.
func FromContext(ctx context.Context) string {
	v, _ := ctx.Value(contextKey{}).(string)
	return v
}
