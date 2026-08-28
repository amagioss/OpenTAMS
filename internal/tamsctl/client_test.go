package tamsctl

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturedReq struct {
	method    string
	path      string
	query     string
	auth      string
	body      string
	requestID string
	idemKey   string
}

// recordingServer returns a server that records the last request and replies
// with status/respBody.
func recordingServer(t *testing.T, status int, respBody string, capture *capturedReq) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*capture = capturedReq{
			method:    r.Method,
			path:      r.URL.Path,
			query:     r.URL.RawQuery,
			auth:      r.Header.Get("Authorization"),
			body:      string(b),
			requestID: r.Header.Get("X-Request-ID"),
			idemKey:   r.Header.Get("X-Idempotency-Key"),
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGetFlow_MethodPathAuth(t *testing.T) {
	var got capturedReq
	srv := recordingServer(t, http.StatusOK, `{"id":"flow-1"}`, &got)
	c := NewClient(srv.URL, "tok-abc")

	raw, err := c.GetFlow(context.Background(), "flow-1")

	require.NoError(t, err)
	assert.Equal(t, http.MethodGet, got.method)
	assert.Equal(t, "/flows/flow-1", got.path)
	assert.Equal(t, "Bearer tok-abc", got.auth)
	assert.JSONEq(t, `{"id":"flow-1"}`, string(raw))
}

func TestCreateFlow_BuildsVideoPayload(t *testing.T) {
	var got capturedReq
	srv := recordingServer(t, http.StatusCreated, `{}`, &got)
	c := NewClient(srv.URL, "t")

	_, err := c.CreateFlow(context.Background(), FlowCreateParams{
		ID:          "flow-1",
		SourceID:    "src-1",
		Codec:       "video/mp4",
		Format:      "urn:x-nmos:format:video",
		FrameWidth:  1920,
		FrameHeight: 1080,
		FrameRate:   &FrameRate{Numerator: 30000, Denominator: 1001},
	})

	require.NoError(t, err)
	assert.Equal(t, http.MethodPut, got.method)
	assert.Equal(t, "/flows/flow-1", got.path)

	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(got.body), &body))
	assert.Equal(t, "src-1", body["source_id"])
	assert.Equal(t, "video/mp4", body["codec"])
	assert.Equal(t, "urn:x-nmos:format:video", body["format"])
	ep, ok := body["essence_parameters"].(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, 1920, ep["frame_width"])
	assert.EqualValues(t, 1080, ep["frame_height"])
	fr, ok := ep["frame_rate"].(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, 30000, fr["numerator"])
	assert.EqualValues(t, 1001, fr["denominator"])
}

func TestCreateFlow_VFROmitsFrameRate(t *testing.T) {
	var got capturedReq
	srv := recordingServer(t, http.StatusCreated, `{}`, &got)
	c := NewClient(srv.URL, "t")

	_, err := c.CreateFlow(context.Background(), FlowCreateParams{
		ID: "flow-1", SourceID: "src-1", Codec: "video/mp4",
		Format: "urn:x-nmos:format:video", FrameWidth: 1920, FrameHeight: 1080,
		VFR: true,
	})

	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(got.body), &body))
	ep := body["essence_parameters"].(map[string]any)
	assert.Equal(t, true, ep["vfr"])
	assert.NotContains(t, ep, "frame_rate")
}

func TestEveryRequest_HasGeneratedRequestID(t *testing.T) {
	var got capturedReq
	srv := recordingServer(t, http.StatusOK, `{}`, &got)
	c := NewClient(srv.URL, "t")

	_, err := c.GetFlow(context.Background(), "flow-1")

	require.NoError(t, err)
	_, perr := uuid.Parse(got.requestID)
	assert.NoError(t, perr, "X-Request-ID should be a generated UUID, got %q", got.requestID)
}

func TestRegisterSegments_SendsIdempotencyKey(t *testing.T) {
	var got capturedReq
	srv := recordingServer(t, http.StatusOK, `{}`, &got)
	c := NewClient(srv.URL, "t")

	_, err := c.RegisterSegments(context.Background(), "flow-1", []SegmentRegisterParams{
		{ObjectID: "o1", Timerange: "[0:0_6:0)"},
	})

	require.NoError(t, err)
	_, perr := uuid.Parse(got.idemKey)
	assert.NoError(t, perr, "X-Idempotency-Key should be a generated UUID, got %q", got.idemKey)
}

func TestListSegments_NoIdempotencyKey(t *testing.T) {
	var got capturedReq
	srv := recordingServer(t, http.StatusOK, `[]`, &got)
	c := NewClient(srv.URL, "t")

	_, err := c.ListSegments(context.Background(), "flow-1", SegmentGetParams{})

	require.NoError(t, err)
	assert.Empty(t, got.idemKey, "GET must not carry an idempotency key")
}

func TestAllocateStorage_LimitBody(t *testing.T) {
	var got capturedReq
	srv := recordingServer(t, http.StatusCreated, `{}`, &got)
	c := NewClient(srv.URL, "t")

	_, err := c.AllocateStorage(context.Background(), "flow-1", 10, nil)

	require.NoError(t, err)
	assert.Equal(t, http.MethodPost, got.method)
	assert.Equal(t, "/flows/flow-1/storage", got.path)
	assert.JSONEq(t, `{"limit":10}`, got.body)
}

func TestAllocateStorage_ObjectIDsBody(t *testing.T) {
	var got capturedReq
	srv := recordingServer(t, http.StatusCreated, `{}`, &got)
	c := NewClient(srv.URL, "t")

	_, err := c.AllocateStorage(context.Background(), "flow-1", 0, []string{"o1", "o2"})

	require.NoError(t, err)
	assert.JSONEq(t, `{"object_ids":["o1","o2"]}`, got.body)
}

func TestListSegments_Query(t *testing.T) {
	var got capturedReq
	srv := recordingServer(t, http.StatusOK, `[]`, &got)
	c := NewClient(srv.URL, "t")

	_, err := c.ListSegments(context.Background(), "flow-1", SegmentGetParams{
		Timerange: "[0:0_10:0)",
		Limit:     50,
	})

	require.NoError(t, err)
	assert.Equal(t, "/flows/flow-1/segments", got.path)
	assert.Contains(t, got.query, "timerange=")
	assert.Contains(t, got.query, "limit=50")
}

func TestListSegments_CapturesNextPageToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Paging-NextKey", "cursor-42")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"object_id":"o1"}]`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "t")

	page, err := c.ListSegments(context.Background(), "flow-1", SegmentGetParams{})

	require.NoError(t, err)
	assert.Equal(t, "cursor-42", page.NextToken)
	assert.JSONEq(t, `[{"object_id":"o1"}]`, string(page.Items))
}

func TestListSegments_NoNextPageTokenOnLastPage(t *testing.T) {
	var got capturedReq
	srv := recordingServer(t, http.StatusOK, `[]`, &got)
	c := NewClient(srv.URL, "t")

	page, err := c.ListSegments(context.Background(), "flow-1", SegmentGetParams{})

	require.NoError(t, err)
	assert.Empty(t, page.NextToken)
}

func TestListSources_Query(t *testing.T) {
	var got capturedReq
	srv := recordingServer(t, http.StatusOK, `[]`, &got)
	c := NewClient(srv.URL, "t")

	_, err := c.ListSources(context.Background(), SourceGetParams{
		Label:     "cam1",
		Tags:      map[string]string{"location": "studio"},
		TagsExist: []string{"archived"},
		Limit:     100,
	})

	require.NoError(t, err)
	assert.Equal(t, "/sources", got.path)
	assert.Contains(t, got.query, "label=cam1")
	assert.Contains(t, got.query, "tag.location=studio")
	assert.Contains(t, got.query, "tag_exists.archived=true")
}

func TestUnauthorized_GivesRefreshHint(t *testing.T) {
	var got capturedReq
	srv := recordingServer(t, http.StatusUnauthorized, `unauthorized`, &got)
	c := NewClient(srv.URL, "t")

	_, err := c.GetFlow(context.Background(), "flow-1")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnauthorized)
}

func TestExpiredToken_FailsBeforeRequest(t *testing.T) {
	var got capturedReq
	srv := recordingServer(t, http.StatusOK, `{}`, &got)

	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	expired := makeJWT(t, map[string]any{"exp": now.Add(-time.Hour).Unix()})
	c := NewClient(srv.URL, expired)
	c.now = func() time.Time { return now }

	_, err := c.GetFlow(context.Background(), "flow-1")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTokenExpired)
	assert.Empty(t, got.method, "no request should have been sent")
}

func TestAPIError_CarriesStatusAndBody(t *testing.T) {
	var got capturedReq
	srv := recordingServer(t, http.StatusNotFound, `{"type":"about:blank","status":404}`, &got)
	c := NewClient(srv.URL, "t")

	_, err := c.GetFlow(context.Background(), "missing")

	require.Error(t, err)
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotFound, apiErr.Status)
	assert.Contains(t, apiErr.Body, "404")
}
