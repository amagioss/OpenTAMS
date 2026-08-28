package handlers_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/httpx/handlers"
	"github.com/amagioss/opentams/internal/metastore"
	"github.com/amagioss/opentams/internal/timerange"
)

func newFlowHandler(svc *mockFlowService) *handlers.Handler {
	return handlers.New(nil, testConfig(), nil, svc, nil, nil, nil, nil, nil)
}

func testFlow(id, srcID uuid.UUID) *metastore.Flow {
	label := "test-label"
	desc := "test-desc"
	avg := int64(1000)
	maxRate := int64(2000)
	mv := int64(1)
	return &metastore.Flow{
		ID:                id,
		SourceID:          srcID,
		Format:            "urn:x-nmos:format:data",
		Label:             &label,
		Description:       &desc,
		Tags:              map[string]json.RawMessage{"env": json.RawMessage(`"prod"`)},
		EssenceParameters: json.RawMessage(`{"data_type":"urn:x-tams:data:test"}`),
		AvgBitRate:        &avg,
		MaxBitRate:        &maxRate,
		MetadataVersion:   &mv,
		ReadOnly:          false,
		Created:           time.Now(),
		MetadataUpdated:   time.Now(),
	}
}

// TC-HAND-FLOW-01
func TestGetFlows_returnsFlowList(t *testing.T) {
	id := uuid.New()
	srcID := uuid.New()
	svc := &mockFlowService{
		listFlows: func(_ context.Context, p metastore.ListFlowsParams) (*metastore.FlowPage, error) {
			return &metastore.FlowPage{Items: []*metastore.Flow{testFlow(id, srcID)}}, nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.GetFlows(context.Background(), api.GetFlowsRequestObject{})
	require.NoError(t, err)
	body, ok := resp.(api.GetFlows200JSONResponse)
	require.True(t, ok)
	require.Len(t, body.Body, 1)
	disc, err := body.Body[0].Discriminator()
	require.NoError(t, err)
	assert.Equal(t, "urn:x-nmos:format:data", disc)
}

// TC-HAND-FLOW-02
func TestHeadFlows_returns200(t *testing.T) {
	svc := &mockFlowService{
		listFlows: func(_ context.Context, _ metastore.ListFlowsParams) (*metastore.FlowPage, error) {
			return &metastore.FlowPage{}, nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.HeadFlows(context.Background(), api.HeadFlowsRequestObject{})
	require.NoError(t, err)
	_, ok := resp.(api.HeadFlows200Response)
	assert.True(t, ok)
}

// TC-HAND-FLOW-03
func TestGetFlow_returnsFlow(t *testing.T) {
	id := uuid.New()
	srcID := uuid.New()
	svc := &mockFlowService{
		getFlow: func(_ context.Context, gotID uuid.UUID, _ bool, _ *timerange.TimeRange) (*metastore.Flow, error) {
			assert.Equal(t, id, gotID)
			return testFlow(id, srcID), nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.GetFlow(context.Background(), api.GetFlowRequestObject{FlowId: id.String()})
	require.NoError(t, err)
	_, ok := resp.(api.GetFlow200JSONResponse)
	assert.True(t, ok)
}

// TC-HAND-FLOW-03b: regression test for oapi-codegen issue #1250.
//
// Strict-server response types are emitted as `type GetFlow200JSONResponse Flow`,
// which does not inherit Flow's custom MarshalJSON. Without the bridge generated
// by tools/genunionbridges, json.Encoder falls back to default struct encoding
// and the wire body collapses to `{}` — the handler-level type-assertion test
// above would still pass and the bug would ship. This test drives the response
// through VisitGetFlowResponse, which is what the Gin adapter calls in
// production, and asserts the body is real JSON carrying the flow fields.
//
// If this test fails after a codegen regeneration, re-run
// `go run ./tools/genunionbridges -in gen/api/opentams.gen.go -out gen/api/opentams_json_bridges.gen.go`.
func TestGetFlow_wireBodyContainsUnionFields(t *testing.T) {
	id := uuid.New()
	srcID := uuid.New()
	svc := &mockFlowService{
		getFlow: func(_ context.Context, _ uuid.UUID, _ bool, _ *timerange.TimeRange) (*metastore.Flow, error) {
			return testFlow(id, srcID), nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.GetFlow(context.Background(), api.GetFlowRequestObject{FlowId: id.String()})
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	require.NoError(t, resp.VisitGetFlowResponse(rec))

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body),
		"wire body was not valid JSON — probable oapi-codegen #1250 regression. body=%q", rec.Body.String())

	assert.Equal(t, id.String(), body["id"],
		"wire body missing id (Flow.MarshalJSON not invoked on response wrapper); body=%s", rec.Body.String())
	assert.Equal(t, srcID.String(), body["source_id"],
		"wire body missing source_id; body=%s", rec.Body.String())
	assert.Equal(t, "urn:x-nmos:format:data", body["format"],
		"wire body missing format; body=%s", rec.Body.String())
}

// TC-HAND-FLOW-04
func TestGetFlow_notFound_returns404(t *testing.T) {
	svc := &mockFlowService{
		getFlow: func(_ context.Context, _ uuid.UUID, _ bool, _ *timerange.TimeRange) (*metastore.Flow, error) {
			return nil, apperror.New(apperror.ErrNotFound, "not found")
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.GetFlow(context.Background(), api.GetFlowRequestObject{FlowId: uuid.New().String()})
	require.NoError(t, err)
	_, ok := resp.(api.GetFlow404ApplicationProblemPlusJSONResponse)
	assert.True(t, ok)
}

// TC-HAND-FLOW-05
func TestGetFlow_invalidUUID_returns404(t *testing.T) {
	h := newFlowHandler(&mockFlowService{})
	resp, err := h.GetFlow(context.Background(), api.GetFlowRequestObject{FlowId: "not-a-uuid"})
	require.NoError(t, err)
	_, ok := resp.(api.GetFlow404ApplicationProblemPlusJSONResponse)
	assert.True(t, ok)
}

// TC-HAND-FLOW-06
func TestHeadFlow_returns200(t *testing.T) {
	id := uuid.New()
	srcID := uuid.New()
	svc := &mockFlowService{
		getFlow: func(_ context.Context, _ uuid.UUID, _ bool, _ *timerange.TimeRange) (*metastore.Flow, error) {
			return testFlow(id, srcID), nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.HeadFlow(context.Background(), api.HeadFlowRequestObject{FlowId: id.String()})
	require.NoError(t, err)
	_, ok := resp.(api.HeadFlow200Response)
	assert.True(t, ok)
}

// TC-HAND-FLOW-07
func TestHeadFlow_notFound_returns404(t *testing.T) {
	svc := &mockFlowService{
		getFlow: func(_ context.Context, _ uuid.UUID, _ bool, _ *timerange.TimeRange) (*metastore.Flow, error) {
			return nil, apperror.New(apperror.ErrNotFound, "not found")
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.HeadFlow(context.Background(), api.HeadFlowRequestObject{FlowId: uuid.New().String()})
	require.NoError(t, err)
	_, ok := resp.(api.HeadFlow404ApplicationProblemPlusJSONResponse)
	assert.True(t, ok)
}

// TC-HAND-FLOW-08
func TestPutFlow_create_returns201(t *testing.T) {
	id := uuid.New()
	srcID := uuid.New()
	svc := &mockFlowService{
		upsertFlow: func(_ context.Context, f *metastore.Flow) (bool, error) {
			assert.Equal(t, id, f.ID)
			return true, nil
		},
		getFlow: func(_ context.Context, _ uuid.UUID, _ bool, _ *timerange.TimeRange) (*metastore.Flow, error) {
			return testFlow(id, srcID), nil
		},
	}
	h := newFlowHandler(svc)
	body := buildDataFlow(t, id, srcID)
	resp, err := h.PutFlow(context.Background(), api.PutFlowRequestObject{
		FlowId: id.String(),
		Body:   &body,
	})
	require.NoError(t, err)
	_, ok := resp.(api.PutFlow201JSONResponse)
	assert.True(t, ok)
}

// TC-HAND-FLOW-09
func TestPutFlow_update_returns204(t *testing.T) {
	id := uuid.New()
	srcID := uuid.New()
	svc := &mockFlowService{
		upsertFlow: func(_ context.Context, _ *metastore.Flow) (bool, error) {
			return false, nil
		},
		getFlow: func(_ context.Context, _ uuid.UUID, _ bool, _ *timerange.TimeRange) (*metastore.Flow, error) {
			return testFlow(id, srcID), nil
		},
	}
	h := newFlowHandler(svc)
	body := buildDataFlow(t, id, srcID)
	resp, err := h.PutFlow(context.Background(), api.PutFlowRequestObject{
		FlowId: id.String(),
		Body:   &body,
	})
	require.NoError(t, err)
	_, ok := resp.(api.PutFlow204Response)
	assert.True(t, ok)
}

// TC-HAND-FLOW-10
func TestDeleteFlow_returns204(t *testing.T) {
	id := uuid.New()
	svc := &mockFlowService{
		deleteFlow: func(_ context.Context, gotID uuid.UUID) error {
			assert.Equal(t, id, gotID)
			return nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.DeleteFlow(context.Background(), api.DeleteFlowRequestObject{FlowId: id.String()})
	require.NoError(t, err)
	_, ok := resp.(api.DeleteFlow204Response)
	assert.True(t, ok)
}

// TC-HAND-FLOW-11
func TestDeleteFlow_notFound_returns404(t *testing.T) {
	svc := &mockFlowService{
		deleteFlow: func(_ context.Context, _ uuid.UUID) error {
			return apperror.New(apperror.ErrNotFound, "not found")
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.DeleteFlow(context.Background(), api.DeleteFlowRequestObject{FlowId: uuid.New().String()})
	require.NoError(t, err)
	_, ok := resp.(api.DeleteFlow404ApplicationProblemPlusJSONResponse)
	assert.True(t, ok)
}

// TC-HAND-FLOW-12
func TestDeleteFlow_invalidUUID_returns404(t *testing.T) {
	h := newFlowHandler(&mockFlowService{})
	resp, err := h.DeleteFlow(context.Background(), api.DeleteFlowRequestObject{FlowId: "bad-uuid"})
	require.NoError(t, err)
	_, ok := resp.(api.DeleteFlow404ApplicationProblemPlusJSONResponse)
	assert.True(t, ok)
}

// TC-HAND-FLOW-13
func TestGetFlowDescription_returnsDescription(t *testing.T) {
	id := uuid.New()
	srcID := uuid.New()
	svc := &mockFlowService{
		getFlow: func(_ context.Context, _ uuid.UUID, _ bool, _ *timerange.TimeRange) (*metastore.Flow, error) {
			return testFlow(id, srcID), nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.GetFlowDescription(context.Background(), api.GetFlowDescriptionRequestObject{FlowId: id.String()})
	require.NoError(t, err)
	body, ok := resp.(api.GetFlowDescription200JSONResponse)
	require.True(t, ok)
	assert.Equal(t, "test-desc", string(body))
}

// TC-HAND-FLOW-14
func TestHeadFlowDescription_returns200(t *testing.T) {
	id := uuid.New()
	srcID := uuid.New()
	svc := &mockFlowService{
		getFlow: func(_ context.Context, _ uuid.UUID, _ bool, _ *timerange.TimeRange) (*metastore.Flow, error) {
			return testFlow(id, srcID), nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.HeadFlowDescription(context.Background(), api.HeadFlowDescriptionRequestObject{FlowId: id.String()})
	require.NoError(t, err)
	_, ok := resp.(api.HeadFlowDescription200Response)
	assert.True(t, ok)
}

// TC-HAND-FLOW-15
func TestPutFlowDescription_returns204(t *testing.T) {
	id := uuid.New()
	svc := &mockFlowService{
		putFlowDesc: func(_ context.Context, gotID uuid.UUID, desc string) error {
			assert.Equal(t, id, gotID)
			assert.Equal(t, "new-desc", desc)
			return nil
		},
	}
	h := newFlowHandler(svc)
	body := api.PutFlowDescriptionJSONRequestBody("new-desc")
	resp, err := h.PutFlowDescription(context.Background(), api.PutFlowDescriptionRequestObject{
		FlowId: id.String(),
		Body:   &body,
	})
	require.NoError(t, err)
	_, ok := resp.(api.PutFlowDescription204Response)
	assert.True(t, ok)
}

// TC-HAND-FLOW-16
func TestDeleteFlowDescription_returns204(t *testing.T) {
	id := uuid.New()
	svc := &mockFlowService{
		deleteFlowDesc: func(_ context.Context, gotID uuid.UUID) error {
			assert.Equal(t, id, gotID)
			return nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.DeleteFlowDescription(context.Background(), api.DeleteFlowDescriptionRequestObject{FlowId: id.String()})
	require.NoError(t, err)
	_, ok := resp.(api.DeleteFlowDescription204Response)
	assert.True(t, ok)
}

// TC-HAND-FLOW-17
func TestGetFlowLabel_returnsLabel(t *testing.T) {
	id := uuid.New()
	srcID := uuid.New()
	svc := &mockFlowService{
		getFlow: func(_ context.Context, _ uuid.UUID, _ bool, _ *timerange.TimeRange) (*metastore.Flow, error) {
			return testFlow(id, srcID), nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.GetFlowLabel(context.Background(), api.GetFlowLabelRequestObject{FlowId: id.String()})
	require.NoError(t, err)
	body, ok := resp.(api.GetFlowLabel200JSONResponse)
	require.True(t, ok)
	assert.Equal(t, "test-label", string(body))
}

// TC-HAND-FLOW-18
func TestHeadFlowLabel_returns200(t *testing.T) {
	id := uuid.New()
	srcID := uuid.New()
	svc := &mockFlowService{
		getFlow: func(_ context.Context, _ uuid.UUID, _ bool, _ *timerange.TimeRange) (*metastore.Flow, error) {
			return testFlow(id, srcID), nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.HeadFlowLabel(context.Background(), api.HeadFlowLabelRequestObject{FlowId: id.String()})
	require.NoError(t, err)
	_, ok := resp.(api.HeadFlowLabel200Response)
	assert.True(t, ok)
}

// TC-HAND-FLOW-19
func TestPutFlowLabel_returns204(t *testing.T) {
	id := uuid.New()
	svc := &mockFlowService{
		putFlowLabel: func(_ context.Context, gotID uuid.UUID, label string) error {
			assert.Equal(t, id, gotID)
			assert.Equal(t, "new-label", label)
			return nil
		},
	}
	h := newFlowHandler(svc)
	body := api.PutFlowLabelJSONRequestBody("new-label")
	resp, err := h.PutFlowLabel(context.Background(), api.PutFlowLabelRequestObject{
		FlowId: id.String(),
		Body:   &body,
	})
	require.NoError(t, err)
	_, ok := resp.(api.PutFlowLabel204Response)
	assert.True(t, ok)
}

// TC-HAND-FLOW-20
func TestDeleteFlowLabel_returns204(t *testing.T) {
	id := uuid.New()
	svc := &mockFlowService{
		deleteFlowLabel: func(_ context.Context, gotID uuid.UUID) error {
			assert.Equal(t, id, gotID)
			return nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.DeleteFlowLabel(context.Background(), api.DeleteFlowLabelRequestObject{FlowId: id.String()})
	require.NoError(t, err)
	_, ok := resp.(api.DeleteFlowLabel204Response)
	assert.True(t, ok)
}

// TC-HAND-FLOW-21
func TestGetFlowTags_returnsTags(t *testing.T) {
	id := uuid.New()
	srcID := uuid.New()
	svc := &mockFlowService{
		getFlow: func(_ context.Context, _ uuid.UUID, _ bool, _ *timerange.TimeRange) (*metastore.Flow, error) {
			return testFlow(id, srcID), nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.GetFlowTags(context.Background(), api.GetFlowTagsRequestObject{FlowId: id.String()})
	require.NoError(t, err)
	_, ok := resp.(api.GetFlowTags200JSONResponse)
	assert.True(t, ok)
}

// TC-HAND-FLOW-22
func TestHeadFlowTags_returns200(t *testing.T) {
	id := uuid.New()
	srcID := uuid.New()
	svc := &mockFlowService{
		getFlow: func(_ context.Context, _ uuid.UUID, _ bool, _ *timerange.TimeRange) (*metastore.Flow, error) {
			return testFlow(id, srcID), nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.HeadFlowTags(context.Background(), api.HeadFlowTagsRequestObject{FlowId: id.String()})
	require.NoError(t, err)
	_, ok := resp.(api.HeadFlowTags200Response)
	assert.True(t, ok)
}

// TC-HAND-FLOW-23
func TestGetFlowTag_returnsTagValue(t *testing.T) {
	id := uuid.New()
	srcID := uuid.New()
	svc := &mockFlowService{
		getFlow: func(_ context.Context, _ uuid.UUID, _ bool, _ *timerange.TimeRange) (*metastore.Flow, error) {
			return testFlow(id, srcID), nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.GetFlowTag(context.Background(), api.GetFlowTagRequestObject{
		FlowId: id.String(),
		Name:   "env",
	})
	require.NoError(t, err)
	_, ok := resp.(api.GetFlowTag200JSONResponse)
	assert.True(t, ok)
}

// TC-HAND-FLOW-24
func TestHeadFlowTag_returns200(t *testing.T) {
	id := uuid.New()
	srcID := uuid.New()
	svc := &mockFlowService{
		getFlow: func(_ context.Context, _ uuid.UUID, _ bool, _ *timerange.TimeRange) (*metastore.Flow, error) {
			return testFlow(id, srcID), nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.HeadFlowTag(context.Background(), api.HeadFlowTagRequestObject{
		FlowId: id.String(),
		Name:   "env",
	})
	require.NoError(t, err)
	_, ok := resp.(api.HeadFlowTag200Response)
	assert.True(t, ok)
}

// TC-HAND-FLOW-25
func TestPutFlowTag_returns204(t *testing.T) {
	id := uuid.New()
	svc := &mockFlowService{
		putFlowTag: func(_ context.Context, gotID uuid.UUID, name string, val json.RawMessage) error {
			assert.Equal(t, id, gotID)
			assert.Equal(t, "env", name)
			return nil
		},
	}
	h := newFlowHandler(svc)
	jb := api.PutFlowTagJSONBody{}
	require.NoError(t, jb.UnmarshalJSON(json.RawMessage(`"staging"`)))
	body := api.PutFlowTagJSONRequestBody(jb)
	resp, err := h.PutFlowTag(context.Background(), api.PutFlowTagRequestObject{
		FlowId: id.String(),
		Name:   "env",
		Body:   &body,
	})
	require.NoError(t, err)
	_, ok := resp.(api.PutFlowTag204Response)
	assert.True(t, ok)
}

// TC-HAND-FLOW-26
func TestDeleteFlowTag_returns204(t *testing.T) {
	id := uuid.New()
	svc := &mockFlowService{
		deleteFlowTag: func(_ context.Context, gotID uuid.UUID, name string) error {
			assert.Equal(t, id, gotID)
			assert.Equal(t, "env", name)
			return nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.DeleteFlowTag(context.Background(), api.DeleteFlowTagRequestObject{
		FlowId: id.String(),
		Name:   "env",
	})
	require.NoError(t, err)
	_, ok := resp.(api.DeleteFlowTag204Response)
	assert.True(t, ok)
}

// TC-HAND-FLOW-27
func TestGetFlowCollection_returnsCollection(t *testing.T) {
	id := uuid.New()
	srcID := uuid.New()
	collID := uuid.New()
	f := testFlow(id, srcID)
	f.FlowCollection = []metastore.CollectionItem{{ID: collID}}
	svc := &mockFlowService{
		getFlow: func(_ context.Context, _ uuid.UUID, _ bool, _ *timerange.TimeRange) (*metastore.Flow, error) {
			return f, nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.GetFlowCollection(context.Background(), api.GetFlowCollectionRequestObject{FlowId: id.String()})
	require.NoError(t, err)
	body, ok := resp.(api.GetFlowCollection200JSONResponse)
	require.True(t, ok)
	require.Len(t, body, 1)
	assert.Equal(t, collID.String(), body[0].Id)
}

// TC-HAND-FLOW-28
func TestHeadFlowCollection_returns200(t *testing.T) {
	id := uuid.New()
	srcID := uuid.New()
	svc := &mockFlowService{
		getFlow: func(_ context.Context, _ uuid.UUID, _ bool, _ *timerange.TimeRange) (*metastore.Flow, error) {
			return testFlow(id, srcID), nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.HeadFlowCollection(context.Background(), api.HeadFlowCollectionRequestObject{FlowId: id.String()})
	require.NoError(t, err)
	_, ok := resp.(api.HeadFlowCollection200Response)
	assert.True(t, ok)
}

// TC-HAND-FLOW-29
func TestPutFlowCollection_returns204(t *testing.T) {
	id := uuid.New()
	collID := uuid.New()
	svc := &mockFlowService{
		putFlowCollection: func(_ context.Context, gotID uuid.UUID, items []metastore.CollectionItem) error {
			assert.Equal(t, id, gotID)
			require.Len(t, items, 1)
			assert.Equal(t, collID, items[0].ID)
			return nil
		},
	}
	h := newFlowHandler(svc)
	role := "main"
	body := api.PutFlowCollectionJSONRequestBody{
		{Id: collID.String(), Role: role},
	}
	resp, err := h.PutFlowCollection(context.Background(), api.PutFlowCollectionRequestObject{
		FlowId: id.String(),
		Body:   &body,
	})
	require.NoError(t, err)
	_, ok := resp.(api.PutFlowCollection204Response)
	assert.True(t, ok)
}

// TC-HAND-FLOW-30
func TestDeleteFlowCollection_returns204(t *testing.T) {
	id := uuid.New()
	svc := &mockFlowService{
		deleteFlowCollection: func(_ context.Context, gotID uuid.UUID) error {
			assert.Equal(t, id, gotID)
			return nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.DeleteFlowCollection(context.Background(), api.DeleteFlowCollectionRequestObject{FlowId: id.String()})
	require.NoError(t, err)
	_, ok := resp.(api.DeleteFlowCollection204Response)
	assert.True(t, ok)
}

// TC-HAND-FLOW-31
func TestGetFlowAvgBitRate_returnsValue(t *testing.T) {
	id := uuid.New()
	srcID := uuid.New()
	svc := &mockFlowService{
		getFlow: func(_ context.Context, _ uuid.UUID, _ bool, _ *timerange.TimeRange) (*metastore.Flow, error) {
			return testFlow(id, srcID), nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.GetFlowAvgBitRate(context.Background(), api.GetFlowAvgBitRateRequestObject{FlowId: id.String()})
	require.NoError(t, err)
	body, ok := resp.(api.GetFlowAvgBitRate200JSONResponse)
	require.True(t, ok)
	assert.Equal(t, 1000, int(body))
}

// TC-HAND-FLOW-32
func TestHeadFlowAvgBitRate_returns200(t *testing.T) {
	id := uuid.New()
	srcID := uuid.New()
	svc := &mockFlowService{
		getFlow: func(_ context.Context, _ uuid.UUID, _ bool, _ *timerange.TimeRange) (*metastore.Flow, error) {
			return testFlow(id, srcID), nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.HeadFlowAvgBitRate(context.Background(), api.HeadFlowAvgBitRateRequestObject{FlowId: id.String()})
	require.NoError(t, err)
	_, ok := resp.(api.HeadFlowAvgBitRate200Response)
	assert.True(t, ok)
}

// TC-HAND-FLOW-33
func TestPutFlowAvgBitRate_returns204(t *testing.T) {
	id := uuid.New()
	svc := &mockFlowService{
		putFlowAvgBitRate: func(_ context.Context, gotID uuid.UUID, rate int64) error {
			assert.Equal(t, id, gotID)
			assert.Equal(t, int64(500), rate)
			return nil
		},
	}
	h := newFlowHandler(svc)
	body := api.PutFlowAvgBitRateJSONRequestBody(500)
	resp, err := h.PutFlowAvgBitRate(context.Background(), api.PutFlowAvgBitRateRequestObject{
		FlowId: id.String(),
		Body:   &body,
	})
	require.NoError(t, err)
	_, ok := resp.(api.PutFlowAvgBitRate204Response)
	assert.True(t, ok)
}

// TC-HAND-FLOW-34
func TestDeleteFlowAvgBitRate_returns204(t *testing.T) {
	id := uuid.New()
	svc := &mockFlowService{
		deleteFlowAvgBitRate: func(_ context.Context, gotID uuid.UUID) error {
			assert.Equal(t, id, gotID)
			return nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.DeleteFlowAvgBitRate(context.Background(), api.DeleteFlowAvgBitRateRequestObject{FlowId: id.String()})
	require.NoError(t, err)
	_, ok := resp.(api.DeleteFlowAvgBitRate204Response)
	assert.True(t, ok)
}

// TC-HAND-FLOW-35
func TestGetFlowMaxBitRate_returnsValue(t *testing.T) {
	id := uuid.New()
	srcID := uuid.New()
	svc := &mockFlowService{
		getFlow: func(_ context.Context, _ uuid.UUID, _ bool, _ *timerange.TimeRange) (*metastore.Flow, error) {
			return testFlow(id, srcID), nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.GetFlowMaxBitRate(context.Background(), api.GetFlowMaxBitRateRequestObject{FlowId: id.String()})
	require.NoError(t, err)
	body, ok := resp.(api.GetFlowMaxBitRate200JSONResponse)
	require.True(t, ok)
	assert.Equal(t, 2000, int(body))
}

// TC-HAND-FLOW-36
func TestHeadFlowMaxBitRate_returns200(t *testing.T) {
	id := uuid.New()
	srcID := uuid.New()
	svc := &mockFlowService{
		getFlow: func(_ context.Context, _ uuid.UUID, _ bool, _ *timerange.TimeRange) (*metastore.Flow, error) {
			return testFlow(id, srcID), nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.HeadFlowMaxBitRate(context.Background(), api.HeadFlowMaxBitRateRequestObject{FlowId: id.String()})
	require.NoError(t, err)
	_, ok := resp.(api.HeadFlowMaxBitRate200Response)
	assert.True(t, ok)
}

// TC-HAND-FLOW-37
func TestPutFlowMaxBitRate_returns204(t *testing.T) {
	id := uuid.New()
	svc := &mockFlowService{
		putFlowMaxBitRate: func(_ context.Context, gotID uuid.UUID, rate int64) error {
			assert.Equal(t, id, gotID)
			assert.Equal(t, int64(800), rate)
			return nil
		},
	}
	h := newFlowHandler(svc)
	body := api.PutFlowMaxBitRateJSONRequestBody(800)
	resp, err := h.PutFlowMaxBitRate(context.Background(), api.PutFlowMaxBitRateRequestObject{
		FlowId: id.String(),
		Body:   &body,
	})
	require.NoError(t, err)
	_, ok := resp.(api.PutFlowMaxBitRate204Response)
	assert.True(t, ok)
}

// TC-HAND-FLOW-38
func TestDeleteFlowMaxBitRate_returns204(t *testing.T) {
	id := uuid.New()
	svc := &mockFlowService{
		deleteFlowMaxBitRate: func(_ context.Context, gotID uuid.UUID) error {
			assert.Equal(t, id, gotID)
			return nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.DeleteFlowMaxBitRate(context.Background(), api.DeleteFlowMaxBitRateRequestObject{FlowId: id.String()})
	require.NoError(t, err)
	_, ok := resp.(api.DeleteFlowMaxBitRate204Response)
	assert.True(t, ok)
}

// TC-HAND-FLOW-39
func TestGetFlowReadOnly_returnsValue(t *testing.T) {
	id := uuid.New()
	srcID := uuid.New()
	svc := &mockFlowService{
		getFlow: func(_ context.Context, _ uuid.UUID, _ bool, _ *timerange.TimeRange) (*metastore.Flow, error) {
			return testFlow(id, srcID), nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.GetFlowReadOnly(context.Background(), api.GetFlowReadOnlyRequestObject{FlowId: id.String()})
	require.NoError(t, err)
	body, ok := resp.(api.GetFlowReadOnly200JSONResponse)
	require.True(t, ok)
	assert.False(t, bool(body))
}

// TC-HAND-FLOW-40
func TestHeadFlowReadOnly_returns200(t *testing.T) {
	id := uuid.New()
	srcID := uuid.New()
	svc := &mockFlowService{
		getFlow: func(_ context.Context, _ uuid.UUID, _ bool, _ *timerange.TimeRange) (*metastore.Flow, error) {
			return testFlow(id, srcID), nil
		},
	}
	h := newFlowHandler(svc)
	resp, err := h.HeadFlowReadOnly(context.Background(), api.HeadFlowReadOnlyRequestObject{FlowId: id.String()})
	require.NoError(t, err)
	_, ok := resp.(api.HeadFlowReadOnly200Response)
	assert.True(t, ok)
}

// TC-HAND-FLOW-41
func TestPutFlowReadOnly_returns204(t *testing.T) {
	id := uuid.New()
	svc := &mockFlowService{
		putFlowReadOnly: func(_ context.Context, gotID uuid.UUID, ro bool) error {
			assert.Equal(t, id, gotID)
			assert.True(t, ro)
			return nil
		},
	}
	h := newFlowHandler(svc)
	body := api.PutFlowReadOnlyJSONRequestBody(true)
	resp, err := h.PutFlowReadOnly(context.Background(), api.PutFlowReadOnlyRequestObject{
		FlowId: id.String(),
		Body:   &body,
	})
	require.NoError(t, err)
	_, ok := resp.(api.PutFlowReadOnly204Response)
	assert.True(t, ok)
}

// buildDataFlow constructs a minimal api.Flow (data format) for use in PutFlow tests.
func buildDataFlow(t *testing.T, id, srcID uuid.UUID) api.Flow {
	t.Helper()
	var f api.Flow
	require.NoError(t, f.FromFlowData(api.FlowData{
		Id:       id.String(),
		SourceId: srcID.String(),
		Format:   "urn:x-nmos:format:data",
		Codec:    "application/json",
		EssenceParameters: struct {
			DataType *string `json:"data_type,omitempty"`
		}{},
	}))
	return f
}
