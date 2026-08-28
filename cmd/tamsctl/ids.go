package main

import (
	"fmt"
	"regexp"
)

// uuidPattern mirrors the server's path/param validation exactly
// (api/schemas/uuid.json, enforced by the OpenAPI request validator): a
// canonical RFC-9562 UUID — lowercase hex, version 1-5, RFC-4122 variant.
// google/uuid's own Parse/Validate is intentionally looser (accepts uppercase,
// hyphen-less, and urn:uuid: forms the server rejects), so we match the server
// here rather than use the library validator. ids minted via uuid.NewString()
// (v4) always satisfy this pattern.
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// validateUUID rejects a non-empty value that is not a canonical UUID, with a
// message naming the flag. Empty values pass (the caller decides whether the
// flag is required). Only genuine UUID fields use this — object ids are
// free-form strings per the TAMS schema and are NOT validated here.
func validateUUID(flag, value string) error {
	if value == "" {
		return nil
	}
	if !uuidPattern.MatchString(value) {
		return fmt.Errorf("invalid --%s %q: must be a canonical lowercase UUID", flag, value)
	}
	return nil
}
