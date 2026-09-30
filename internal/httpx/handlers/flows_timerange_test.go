package handlers_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/metastore"
	"github.com/amagioss/opentams/internal/timerange"
)

// BR-META-21: a query timerange whose bounds do not fit in int64 returns
// timerange.ErrOutOfRange from the store, and the caller maps it to 400
// invalid-timerange, not a 500.
func Test_BR_META_21_OutOfRangeFlowsQueryIs400(t *testing.T) {
	v := "[9223372036:854775807]"
	fh := newFlowHandler(&mockFlowService{
		listFlows: func(_ context.Context, _ metastore.ListFlowsParams) (*metastore.FlowPage, error) {
			return nil, fmt.Errorf("metastore: ListFlows: timerange: %w", timerange.ErrOutOfRange)
		},
	})
	ctx := context.Background()

	gf, err := fh.GetFlows(ctx, api.GetFlowsRequestObject{Params: api.GetFlowsParams{Timerange: &v}})
	require.NoError(t, err)
	resp, ok := gf.(api.GetFlows400ApplicationProblemPlusJSONResponse)
	require.True(t, ok, "GET /flows: got %T, want 400", gf)
	require.NotNil(t, resp.Type)
	assert.Equal(t, invalidTimerangeType, *resp.Type)

	hf, err := fh.HeadFlows(ctx, api.HeadFlowsRequestObject{Params: api.HeadFlowsParams{Timerange: &v}})
	require.NoError(t, err)
	assert.IsType(t, api.HeadFlows400ApplicationProblemPlusJSONResponse{}, hf)
}

// BR-CONV-09, SCN-HTTP-08: GET /flows/{flowId} used to ignore a timerange
// that does not parse and return the flow unfiltered. It is a 400
// invalid-timerange, like every other timerange query. The spec declares no
// 400 for this endpoint, so the handler returns an AppError and the error
// middleware writes the problem.
func Test_BR_CONV_09_UnparseableGetFlowTimerangeIs400(t *testing.T) {
	for _, v := range []string{"(10:0)", "[10:0)", "[abc_1:0)"} {
		t.Run(v, func(t *testing.T) {
			tr := api.Timerange(v)
			called := false
			fh := newFlowHandler(&mockFlowService{
				getFlow: func(_ context.Context, _ uuid.UUID, _ bool, _ *timerange.TimeRange) (*metastore.Flow, error) {
					called = true
					return &metastore.Flow{}, nil
				},
			})
			resp, err := fh.GetFlow(context.Background(), api.GetFlowRequestObject{
				FlowId: uuid.NewString(),
				Params: api.GetFlowParams{Timerange: &tr},
			})
			assert.Nil(t, resp)
			var ae *apperror.AppError
			require.True(t, errors.As(err, &ae), "got %v, want an AppError", err)
			assert.Equal(t, apperror.ErrInvalidTimerange, ae.Code)
			assert.False(t, called, "flow service must not be called")
		})
	}
}

// An empty timerange is a valid filter on GET /flows/{flowId}: it reaches
// the service, it is not a 400.
func Test_ADR0039_EmptyGetFlowTimerangeReachesService(t *testing.T) {
	tr := api.Timerange("[10:0_5:0)")
	var got *timerange.TimeRange
	fh := newFlowHandler(&mockFlowService{
		getFlow: func(_ context.Context, _ uuid.UUID, _ bool, f *timerange.TimeRange) (*metastore.Flow, error) {
			got = f
			return &metastore.Flow{}, nil
		},
	})
	_, err := fh.GetFlow(context.Background(), api.GetFlowRequestObject{
		FlowId: uuid.NewString(),
		Params: api.GetFlowParams{Timerange: &tr},
	})
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, got.IsEmpty())
}
