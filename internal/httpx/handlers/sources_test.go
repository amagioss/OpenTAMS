package handlers_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/httpx/handlers"
	"github.com/amagioss/opentams/internal/metastore"
)

func newSourceHandler(src *mockSourceStore) *handlers.Handler {
	return handlers.New(nil, testConfig(), src, nil, nil, nil, nil, nil, nil)
}

// TC-HAND-SRC-01: GetSources returns items translated from store.
func TestGetSources_returnsItems(t *testing.T) {
	id := uuid.New()
	src := &mockSourceStore{
		listSources: func(_ context.Context, p metastore.ListSourcesParams) (*metastore.SourcePage, error) {
			return &metastore.SourcePage{
				Items: []*metastore.Source{{ID: id, Format: "urn:x-nmos:format:video"}},
			}, nil
		},
	}
	resp, err := newSourceHandler(src).GetSources(context.Background(), api.GetSourcesRequestObject{})
	require.NoError(t, err)
	body, ok := resp.(api.GetSources200JSONResponse)
	require.True(t, ok)
	require.Len(t, body.Body, 1)
	assert.Equal(t, id.String(), body.Body[0].Id)
	assert.Equal(t, api.ContentFormat("urn:x-nmos:format:video"), body.Body[0].Format)
}

// TC-HAND-SRC-02: GetSources sets X-Paging-NextKey when store provides cursor.
func TestGetSources_paginationCursor(t *testing.T) {
	cursor := "opaquecursor"
	src := &mockSourceStore{
		listSources: func(_ context.Context, p metastore.ListSourcesParams) (*metastore.SourcePage, error) {
			return &metastore.SourcePage{NextCursor: &cursor}, nil
		},
	}
	resp, err := newSourceHandler(src).GetSources(context.Background(), api.GetSourcesRequestObject{})
	require.NoError(t, err)
	body, ok := resp.(api.GetSources200JSONResponse)
	require.True(t, ok)
	assert.Equal(t, cursor, body.Headers.XPagingNextKey)
}

// TC-HAND-SRC-03: HeadSources returns 200.
func TestHeadSources_returns200(t *testing.T) {
	src := &mockSourceStore{
		listSources: func(_ context.Context, p metastore.ListSourcesParams) (*metastore.SourcePage, error) {
			return &metastore.SourcePage{}, nil
		},
	}
	resp, err := newSourceHandler(src).HeadSources(context.Background(), api.HeadSourcesRequestObject{})
	require.NoError(t, err)
	_, ok := resp.(api.HeadSources200Response)
	assert.True(t, ok)
}

// TC-HAND-SRC-04: GetSource returns 200 with correct fields.
func TestGetSource_found(t *testing.T) {
	id := uuid.New()
	lbl := "my-label"
	src := &mockSourceStore{
		getSource: func(_ context.Context, got uuid.UUID) (*metastore.Source, error) {
			assert.Equal(t, id, got)
			return &metastore.Source{ID: id, Format: "urn:x-nmos:format:audio", Label: &lbl}, nil
		},
	}
	resp, err := newSourceHandler(src).GetSource(context.Background(), api.GetSourceRequestObject{SourceId: id.String()})
	require.NoError(t, err)
	body, ok := resp.(api.GetSource200JSONResponse)
	require.True(t, ok)
	assert.Equal(t, id.String(), body.Id)
	assert.Equal(t, api.ContentFormat("urn:x-nmos:format:audio"), body.Format)
	require.NotNil(t, body.Label)
	assert.Equal(t, lbl, *body.Label)
}

// TC-HAND-SRC-05: GetSource returns 404 when store returns ErrNotFound.
func TestGetSource_notFound(t *testing.T) {
	src := &mockSourceStore{
		getSource: func(_ context.Context, _ uuid.UUID) (*metastore.Source, error) {
			return nil, apperror.New(apperror.ErrNotFound, "source not found")
		},
	}
	resp, err := newSourceHandler(src).GetSource(context.Background(), api.GetSourceRequestObject{SourceId: uuid.New().String()})
	require.NoError(t, err)
	_, ok := resp.(api.GetSource404ApplicationProblemPlusJSONResponse)
	assert.True(t, ok)
}

// TC-HAND-SRC-06: HeadSource returns 200 when source exists.
func TestHeadSource_found(t *testing.T) {
	id := uuid.New()
	src := &mockSourceStore{
		getSource: func(_ context.Context, _ uuid.UUID) (*metastore.Source, error) {
			return &metastore.Source{ID: id, Format: "urn:x-nmos:format:video"}, nil
		},
	}
	resp, err := newSourceHandler(src).HeadSource(context.Background(), api.HeadSourceRequestObject{SourceId: id.String()})
	require.NoError(t, err)
	_, ok := resp.(api.HeadSource200Response)
	assert.True(t, ok)
}

// TC-HAND-SRC-07: HeadSource returns 404 when source not found.
func TestHeadSource_notFound(t *testing.T) {
	src := &mockSourceStore{
		getSource: func(_ context.Context, _ uuid.UUID) (*metastore.Source, error) {
			return nil, apperror.New(apperror.ErrNotFound, "source not found")
		},
	}
	resp, err := newSourceHandler(src).HeadSource(context.Background(), api.HeadSourceRequestObject{SourceId: uuid.New().String()})
	require.NoError(t, err)
	_, ok := resp.(api.HeadSource404ApplicationProblemPlusJSONResponse)
	assert.True(t, ok)
}

// TC-HAND-SRC-08: GetSourceDescription returns description string.
func TestGetSourceDescription_found(t *testing.T) {
	id := uuid.New()
	desc := "some description"
	src := &mockSourceStore{
		getSource: func(_ context.Context, _ uuid.UUID) (*metastore.Source, error) {
			return &metastore.Source{ID: id, Format: "urn:x-nmos:format:video", Description: &desc}, nil
		},
	}
	resp, err := newSourceHandler(src).GetSourceDescription(context.Background(), api.GetSourceDescriptionRequestObject{SourceId: id.String()})
	require.NoError(t, err)
	body, ok := resp.(api.GetSourceDescription200JSONResponse)
	require.True(t, ok)
	assert.Equal(t, desc, string(body))
}

// TC-HAND-SRC-09: GetSourceDescription returns 404 when description is absent.
func TestGetSourceDescription_absent(t *testing.T) {
	src := &mockSourceStore{
		getSource: func(_ context.Context, _ uuid.UUID) (*metastore.Source, error) {
			return &metastore.Source{Format: "urn:x-nmos:format:video"}, nil
		},
	}
	resp, err := newSourceHandler(src).GetSourceDescription(context.Background(), api.GetSourceDescriptionRequestObject{SourceId: uuid.New().String()})
	require.NoError(t, err)
	_, ok := resp.(api.GetSourceDescription404ApplicationProblemPlusJSONResponse)
	assert.True(t, ok)
}

// TC-HAND-SRC-10: GetSourceDescription returns 404 when source not found.
func TestGetSourceDescription_sourceNotFound(t *testing.T) {
	src := &mockSourceStore{
		getSource: func(_ context.Context, _ uuid.UUID) (*metastore.Source, error) {
			return nil, apperror.New(apperror.ErrNotFound, "source not found")
		},
	}
	resp, err := newSourceHandler(src).GetSourceDescription(context.Background(), api.GetSourceDescriptionRequestObject{SourceId: uuid.New().String()})
	require.NoError(t, err)
	_, ok := resp.(api.GetSourceDescription404ApplicationProblemPlusJSONResponse)
	assert.True(t, ok)
}

// TC-HAND-SRC-11: HeadSourceDescription returns 200 when description exists.
func TestHeadSourceDescription_found(t *testing.T) {
	desc := "desc"
	src := &mockSourceStore{
		getSource: func(_ context.Context, _ uuid.UUID) (*metastore.Source, error) {
			return &metastore.Source{Format: "urn:x-nmos:format:video", Description: &desc}, nil
		},
	}
	resp, err := newSourceHandler(src).HeadSourceDescription(context.Background(), api.HeadSourceDescriptionRequestObject{SourceId: uuid.New().String()})
	require.NoError(t, err)
	_, ok := resp.(api.HeadSourceDescription200Response)
	assert.True(t, ok)
}

// TC-HAND-SRC-12: HeadSourceDescription returns 404 when description absent.
func TestHeadSourceDescription_absent(t *testing.T) {
	src := &mockSourceStore{
		getSource: func(_ context.Context, _ uuid.UUID) (*metastore.Source, error) {
			return &metastore.Source{Format: "urn:x-nmos:format:video"}, nil
		},
	}
	resp, err := newSourceHandler(src).HeadSourceDescription(context.Background(), api.HeadSourceDescriptionRequestObject{SourceId: uuid.New().String()})
	require.NoError(t, err)
	_, ok := resp.(api.HeadSourceDescription404ApplicationProblemPlusJSONResponse)
	assert.True(t, ok)
}

// TC-HAND-SRC-13: PutSourceDescription calls store and returns 204.
func TestPutSourceDescription_success(t *testing.T) {
	id := uuid.New()
	var gotID uuid.UUID
	var gotDesc string
	desc := "new description"
	src := &mockSourceStore{
		putSourceDescription: func(_ context.Context, got uuid.UUID, d string) error {
			gotID = got
			gotDesc = d
			return nil
		},
	}
	resp, err := newSourceHandler(src).PutSourceDescription(context.Background(), api.PutSourceDescriptionRequestObject{
		SourceId: id.String(),
		Body:     &desc,
	})
	require.NoError(t, err)
	_, ok := resp.(api.PutSourceDescription204Response)
	assert.True(t, ok)
	assert.Equal(t, id, gotID)
	assert.Equal(t, desc, gotDesc)
}

// TC-HAND-SRC-14: PutSourceDescription returns 404 when source not found.
func TestPutSourceDescription_notFound(t *testing.T) {
	desc := "desc"
	src := &mockSourceStore{
		putSourceDescription: func(_ context.Context, _ uuid.UUID, _ string) error {
			return apperror.New(apperror.ErrNotFound, "source not found")
		},
	}
	resp, err := newSourceHandler(src).PutSourceDescription(context.Background(), api.PutSourceDescriptionRequestObject{
		SourceId: uuid.New().String(),
		Body:     &desc,
	})
	require.NoError(t, err)
	_, ok := resp.(api.PutSourceDescription404ApplicationProblemPlusJSONResponse)
	assert.True(t, ok)
}

// TC-HAND-SRC-15: DeleteSourceDescription calls store and returns 204.
func TestDeleteSourceDescription_success(t *testing.T) {
	id := uuid.New()
	called := false
	src := &mockSourceStore{
		deleteSourceDescription: func(_ context.Context, got uuid.UUID) error {
			called = true
			assert.Equal(t, id, got)
			return nil
		},
	}
	resp, err := newSourceHandler(src).DeleteSourceDescription(context.Background(), api.DeleteSourceDescriptionRequestObject{SourceId: id.String()})
	require.NoError(t, err)
	_, ok := resp.(api.DeleteSourceDescription204Response)
	assert.True(t, ok)
	assert.True(t, called)
}

// TC-HAND-SRC-16: GetSourceLabel returns label string.
func TestGetSourceLabel_found(t *testing.T) {
	lbl := "my-label"
	src := &mockSourceStore{
		getSource: func(_ context.Context, _ uuid.UUID) (*metastore.Source, error) {
			return &metastore.Source{Format: "urn:x-nmos:format:video", Label: &lbl}, nil
		},
	}
	resp, err := newSourceHandler(src).GetSourceLabel(context.Background(), api.GetSourceLabelRequestObject{SourceId: uuid.New().String()})
	require.NoError(t, err)
	body, ok := resp.(api.GetSourceLabel200JSONResponse)
	require.True(t, ok)
	assert.Equal(t, lbl, string(body))
}

// TC-HAND-SRC-17: GetSourceLabel returns 404 when label absent.
func TestGetSourceLabel_absent(t *testing.T) {
	src := &mockSourceStore{
		getSource: func(_ context.Context, _ uuid.UUID) (*metastore.Source, error) {
			return &metastore.Source{Format: "urn:x-nmos:format:video"}, nil
		},
	}
	resp, err := newSourceHandler(src).GetSourceLabel(context.Background(), api.GetSourceLabelRequestObject{SourceId: uuid.New().String()})
	require.NoError(t, err)
	_, ok := resp.(api.GetSourceLabel404ApplicationProblemPlusJSONResponse)
	assert.True(t, ok)
}

// TC-HAND-SRC-18: HeadSourceLabel returns 200 when label present.
func TestHeadSourceLabel_found(t *testing.T) {
	lbl := "lbl"
	src := &mockSourceStore{
		getSource: func(_ context.Context, _ uuid.UUID) (*metastore.Source, error) {
			return &metastore.Source{Format: "urn:x-nmos:format:video", Label: &lbl}, nil
		},
	}
	resp, err := newSourceHandler(src).HeadSourceLabel(context.Background(), api.HeadSourceLabelRequestObject{SourceId: uuid.New().String()})
	require.NoError(t, err)
	_, ok := resp.(api.HeadSourceLabel200Response)
	assert.True(t, ok)
}

// TC-HAND-SRC-19: HeadSourceLabel returns 404 when label absent.
func TestHeadSourceLabel_absent(t *testing.T) {
	src := &mockSourceStore{
		getSource: func(_ context.Context, _ uuid.UUID) (*metastore.Source, error) {
			return &metastore.Source{Format: "urn:x-nmos:format:video"}, nil
		},
	}
	resp, err := newSourceHandler(src).HeadSourceLabel(context.Background(), api.HeadSourceLabelRequestObject{SourceId: uuid.New().String()})
	require.NoError(t, err)
	_, ok := resp.(api.HeadSourceLabel404ApplicationProblemPlusJSONResponse)
	assert.True(t, ok)
}

// TC-HAND-SRC-20: PutSourceLabel calls store and returns 204.
func TestPutSourceLabel_success(t *testing.T) {
	id := uuid.New()
	lbl := "new-label"
	var gotID uuid.UUID
	var gotLabel string
	src := &mockSourceStore{
		putSourceLabel: func(_ context.Context, got uuid.UUID, l string) error {
			gotID = got
			gotLabel = l
			return nil
		},
	}
	resp, err := newSourceHandler(src).PutSourceLabel(context.Background(), api.PutSourceLabelRequestObject{
		SourceId: id.String(),
		Body:     &lbl,
	})
	require.NoError(t, err)
	_, ok := resp.(api.PutSourceLabel204Response)
	assert.True(t, ok)
	assert.Equal(t, id, gotID)
	assert.Equal(t, lbl, gotLabel)
}

// TC-HAND-SRC-21: PutSourceLabel returns 404 when source not found.
func TestPutSourceLabel_notFound(t *testing.T) {
	lbl := "lbl"
	src := &mockSourceStore{
		putSourceLabel: func(_ context.Context, _ uuid.UUID, _ string) error {
			return apperror.New(apperror.ErrNotFound, "source not found")
		},
	}
	resp, err := newSourceHandler(src).PutSourceLabel(context.Background(), api.PutSourceLabelRequestObject{
		SourceId: uuid.New().String(),
		Body:     &lbl,
	})
	require.NoError(t, err)
	_, ok := resp.(api.PutSourceLabel404ApplicationProblemPlusJSONResponse)
	assert.True(t, ok)
}

// TC-HAND-SRC-22: DeleteSourceLabel calls store and returns 204.
func TestDeleteSourceLabel_success(t *testing.T) {
	id := uuid.New()
	called := false
	src := &mockSourceStore{
		deleteSourceLabel: func(_ context.Context, got uuid.UUID) error {
			called = true
			assert.Equal(t, id, got)
			return nil
		},
	}
	resp, err := newSourceHandler(src).DeleteSourceLabel(context.Background(), api.DeleteSourceLabelRequestObject{SourceId: id.String()})
	require.NoError(t, err)
	_, ok := resp.(api.DeleteSourceLabel204Response)
	assert.True(t, ok)
	assert.True(t, called)
}

// TC-HAND-SRC-23: GetSourceTags returns tags map.
func TestGetSourceTags_found(t *testing.T) {
	id := uuid.New()
	src := &mockSourceStore{
		getSource: func(_ context.Context, _ uuid.UUID) (*metastore.Source, error) {
			return &metastore.Source{
				ID:     id,
				Format: "urn:x-nmos:format:video",
				Tags:   map[string]json.RawMessage{"env": json.RawMessage(`"prod"`)},
			}, nil
		},
	}
	resp, err := newSourceHandler(src).GetSourceTags(context.Background(), api.GetSourceTagsRequestObject{SourceId: id.String()})
	require.NoError(t, err)
	body, ok := resp.(api.GetSourceTags200JSONResponse)
	require.True(t, ok)
	tags := api.Tags(body)
	assert.Len(t, tags, 1)
	_, hasEnv := tags["env"]
	assert.True(t, hasEnv)
}

// TC-HAND-SRC-24: GetSourceTags returns 404 when source not found.
func TestGetSourceTags_notFound(t *testing.T) {
	src := &mockSourceStore{
		getSource: func(_ context.Context, _ uuid.UUID) (*metastore.Source, error) {
			return nil, apperror.New(apperror.ErrNotFound, "source not found")
		},
	}
	resp, err := newSourceHandler(src).GetSourceTags(context.Background(), api.GetSourceTagsRequestObject{SourceId: uuid.New().String()})
	require.NoError(t, err)
	_, ok := resp.(api.GetSourceTags404ApplicationProblemPlusJSONResponse)
	assert.True(t, ok)
}

// TC-HAND-SRC-25: HeadSourceTags returns 200 when source found.
func TestHeadSourceTags_found(t *testing.T) {
	src := &mockSourceStore{
		getSource: func(_ context.Context, _ uuid.UUID) (*metastore.Source, error) {
			return &metastore.Source{Format: "urn:x-nmos:format:video"}, nil
		},
	}
	resp, err := newSourceHandler(src).HeadSourceTags(context.Background(), api.HeadSourceTagsRequestObject{SourceId: uuid.New().String()})
	require.NoError(t, err)
	_, ok := resp.(api.HeadSourceTags200Response)
	assert.True(t, ok)
}

// TC-HAND-SRC-26: GetSourceTag returns the tag value.
func TestGetSourceTag_found(t *testing.T) {
	id := uuid.New()
	raw := json.RawMessage(`"eu-west"`)
	src := &mockSourceStore{
		getSource: func(_ context.Context, _ uuid.UUID) (*metastore.Source, error) {
			return &metastore.Source{
				ID:     id,
				Format: "urn:x-nmos:format:video",
				Tags:   map[string]json.RawMessage{"region": raw},
			}, nil
		},
	}
	resp, err := newSourceHandler(src).GetSourceTag(context.Background(), api.GetSourceTagRequestObject{
		SourceId: id.String(),
		Name:     "region",
	})
	require.NoError(t, err)
	_, ok := resp.(api.GetSourceTag200JSONResponse)
	assert.True(t, ok)
}

// TC-HAND-SRC-27: GetSourceTag returns 404 when tag absent.
func TestGetSourceTag_absent(t *testing.T) {
	src := &mockSourceStore{
		getSource: func(_ context.Context, _ uuid.UUID) (*metastore.Source, error) {
			return &metastore.Source{
				Format: "urn:x-nmos:format:video",
				Tags:   map[string]json.RawMessage{},
			}, nil
		},
	}
	resp, err := newSourceHandler(src).GetSourceTag(context.Background(), api.GetSourceTagRequestObject{
		SourceId: uuid.New().String(),
		Name:     "missing",
	})
	require.NoError(t, err)
	_, ok := resp.(api.GetSourceTag404ApplicationProblemPlusJSONResponse)
	assert.True(t, ok)
}

// TC-HAND-SRC-28: HeadSourceTag returns 200 when tag present.
func TestHeadSourceTag_found(t *testing.T) {
	src := &mockSourceStore{
		getSource: func(_ context.Context, _ uuid.UUID) (*metastore.Source, error) {
			return &metastore.Source{
				Format: "urn:x-nmos:format:video",
				Tags:   map[string]json.RawMessage{"k": json.RawMessage(`"v"`)},
			}, nil
		},
	}
	resp, err := newSourceHandler(src).HeadSourceTag(context.Background(), api.HeadSourceTagRequestObject{
		SourceId: uuid.New().String(),
		Name:     "k",
	})
	require.NoError(t, err)
	_, ok := resp.(api.HeadSourceTag200Response)
	assert.True(t, ok)
}

// TC-HAND-SRC-29: HeadSourceTag returns 404 when tag absent.
func TestHeadSourceTag_absent(t *testing.T) {
	src := &mockSourceStore{
		getSource: func(_ context.Context, _ uuid.UUID) (*metastore.Source, error) {
			return &metastore.Source{
				Format: "urn:x-nmos:format:video",
				Tags:   map[string]json.RawMessage{},
			}, nil
		},
	}
	resp, err := newSourceHandler(src).HeadSourceTag(context.Background(), api.HeadSourceTagRequestObject{
		SourceId: uuid.New().String(),
		Name:     "missing",
	})
	require.NoError(t, err)
	_, ok := resp.(api.HeadSourceTag404ApplicationProblemPlusJSONResponse)
	assert.True(t, ok)
}

// TC-HAND-SRC-30: PutSourceTag calls store with name and raw value, returns 204.
func TestPutSourceTag_success(t *testing.T) {
	id := uuid.New()
	var gotName string
	var gotVal json.RawMessage
	jb := api.PutSourceTagJSONBody{}
	_ = jb.UnmarshalJSON(json.RawMessage(`"eu-west"`))
	body := api.PutSourceTagJSONRequestBody(jb)
	src := &mockSourceStore{
		putSourceTag: func(_ context.Context, _ uuid.UUID, name string, val json.RawMessage) error {
			gotName = name
			gotVal = val
			return nil
		},
	}
	resp, err := newSourceHandler(src).PutSourceTag(context.Background(), api.PutSourceTagRequestObject{
		SourceId: id.String(),
		Name:     "region",
		Body:     &body,
	})
	require.NoError(t, err)
	_, ok := resp.(api.PutSourceTag204Response)
	assert.True(t, ok)
	assert.Equal(t, "region", gotName)
	assert.Equal(t, json.RawMessage(`"eu-west"`), gotVal)
}

// TC-HAND-SRC-31: PutSourceTag returns 404 when source not found.
func TestPutSourceTag_notFound(t *testing.T) {
	jb2 := api.PutSourceTagJSONBody{}
	_ = jb2.UnmarshalJSON(json.RawMessage(`"v"`))
	body := api.PutSourceTagJSONRequestBody(jb2)
	src := &mockSourceStore{
		putSourceTag: func(_ context.Context, _ uuid.UUID, _ string, _ json.RawMessage) error {
			return apperror.New(apperror.ErrNotFound, "source not found")
		},
	}
	resp, err := newSourceHandler(src).PutSourceTag(context.Background(), api.PutSourceTagRequestObject{
		SourceId: uuid.New().String(),
		Name:     "k",
		Body:     &body,
	})
	require.NoError(t, err)
	_, ok := resp.(api.PutSourceTag404ApplicationProblemPlusJSONResponse)
	assert.True(t, ok)
}

// TC-HAND-SRC-32: DeleteSourceTag calls store and returns 204.
func TestDeleteSourceTag_success(t *testing.T) {
	id := uuid.New()
	var gotName string
	src := &mockSourceStore{
		deleteSourceTag: func(_ context.Context, _ uuid.UUID, name string) error {
			gotName = name
			return nil
		},
	}
	resp, err := newSourceHandler(src).DeleteSourceTag(context.Background(), api.DeleteSourceTagRequestObject{
		SourceId: id.String(),
		Name:     "region",
	})
	require.NoError(t, err)
	_, ok := resp.(api.DeleteSourceTag204Response)
	assert.True(t, ok)
	assert.Equal(t, "region", gotName)
}
