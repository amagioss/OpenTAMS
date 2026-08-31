package requestid_test

import (
	"context"
	"testing"

	"github.com/amagioss/opentams/pkg/requestid"
)

func TestGenerate_ReturnsNonEmpty(t *testing.T) {
	if id := requestid.Generate(); id == "" {
		t.Error("Generate must return a non-empty string")
	}
}

func TestGenerate_UniqueEachCall(t *testing.T) {
	a, b := requestid.Generate(), requestid.Generate()
	if a == b {
		t.Error("Generate must return unique values")
	}
}

func TestWithContext_FromContext_RoundTrip(t *testing.T) {
	ctx := requestid.WithContext(context.Background(), "test-id")
	if got := requestid.FromContext(ctx); got != "test-id" {
		t.Errorf("expected test-id, got %q", got)
	}
}

func TestFromContext_EmptyWhenNotSet(t *testing.T) {
	if got := requestid.FromContext(context.Background()); got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestHeader_Value(t *testing.T) {
	if requestid.Header != "X-Request-ID" {
		t.Errorf("expected X-Request-ID, got %q", requestid.Header)
	}
}
