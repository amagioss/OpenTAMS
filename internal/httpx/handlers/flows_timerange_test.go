package handlers_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/amagioss/opentams/gen/api"
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
