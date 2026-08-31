package tamsctl

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// ParseExpiry extracts the `exp` claim from a JWT without verifying its
// signature. ok is false if the token is malformed or carries no numeric
// `exp` claim.
func ParseExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp *float64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == nil {
		return time.Time{}, false
	}
	return time.Unix(int64(*claims.Exp), 0).UTC(), true
}

// IsExpired reports whether token has a parseable `exp` claim that is at or
// before now. Tokens without a readable expiry are not considered expired
// here — the server's 401 is the authority in that case.
func IsExpired(token string, now time.Time) bool {
	exp, ok := ParseExpiry(token)
	if !ok {
		return false
	}
	return !exp.After(now)
}
