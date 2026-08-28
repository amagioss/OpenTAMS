package main

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Every id the CLI mints must satisfy the validator (which mirrors the server
// regex). This locks the generator↔validator↔server agreement.
func TestGeneratedIDsPassValidation(t *testing.T) {
	for range 10000 {
		id := uuid.NewString()
		require.NoErrorf(t, validateUUID("id", id), "generated id %q rejected by validator", id)
	}
}

func TestValidateUUID_EmptyPasses(t *testing.T) {
	require.NoError(t, validateUUID("id", ""))
}
