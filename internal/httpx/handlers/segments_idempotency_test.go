package handlers_test

// R7 regression test — idempotency replay for non-2xx finalisations
// must surface the exact problem-details body that was originally
// returned, not a synthesised version with empty detail.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/internal/httpx/handlers"
	"github.com/amagioss/opentams/internal/idempotency"
)

// Test_SCN_HTTP_R7_IdempotencyReplayPreservesProblemDetails: a cached
// 422 finalisation includes the original detail string. Replay path
// (idempotency.StatusCached) MUST return the exact same body, byte for
// byte, per REQ-RATE-09.
func Test_SCN_HTTP_R7_IdempotencyReplayPreservesProblemDetails(t *testing.T) {
	const wantDetail = "segment 0 ([0:0_5:0)) overlaps existing ([3:0_8:0)) on flow F-abc"

	// Pre-render the 422 body shape the handler would have stored.
	stored := struct {
		Type   *string `json:"type"`
		Title  *string `json:"title"`
		Detail *string `json:"detail"`
		Status *int    `json:"status"`
	}{
		Type:   strPtr("opentams:problem:segment-overlap"),
		Title:  strPtr("Segment Overlap"),
		Detail: strPtr(wantDetail),
		Status: intPtr(422),
	}
	body, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal stored body: %v", err)
	}

	// Idempotency store always returns StatusCached with the prepared body.
	idem := &mockIdempotencyStore{
		acquire: func(_ context.Context, _, _ string, _ time.Duration) (idempotency.AcquireResult, error) {
			return idempotency.AcquireResult{
				Status: idempotency.StatusCached,
				Cached: &idempotency.CachedResponse{
					Status: 422,
					Body:   json.RawMessage(body),
				},
			}, nil
		},
	}
	h := handlers.New(nil, testConfig(), nil, nil, &fakeSegmentService{}, nil, idem, nil, newFakeObjectStore())

	resp, err := h.PostFlowSegments(context.Background(), api.PostFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.PostFlowSegmentsParams{XIdempotencyKey: "replay-key"},
		Body:   postBodySingle(t, "o-x", "[0:0_5:0)"),
	})
	if err != nil {
		t.Fatalf("PostFlowSegments: %v", err)
	}
	got, ok := resp.(api.PostFlowSegments422ApplicationProblemPlusJSONResponse)
	if !ok {
		t.Fatalf("response type %T, want PostFlowSegments422...", resp)
	}
	if got.Detail == nil || *got.Detail != wantDetail {
		t.Errorf("detail = %v, want %q (replay must preserve original body, not synthesise empty)", got.Detail, wantDetail)
	}
	if got.Type == nil || *got.Type != "opentams:problem:segment-overlap" {
		t.Errorf("type = %v, want preserved", got.Type)
	}
	if got.Title == nil || *got.Title != "Segment Overlap" {
		t.Errorf("title = %v, want preserved", got.Title)
	}
}

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }
