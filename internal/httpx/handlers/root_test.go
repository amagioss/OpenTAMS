package handlers_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/internal/config"
	"github.com/amagioss/opentams/internal/httpx/handlers"
)

func testConfig() *config.Config {
	return &config.Config{
		ObjectStoreRegion:        "us-east-1",
		ObjectStoreEndpoint:      "https://s3.amazonaws.com",
		ObjectStorePresignExpiry: time.Hour,
		StorageBackendProvider:   "aws",
		StorageBackendProduct:    "s3",
	}
}

func newHandler(cfg *config.Config) *handlers.Handler {
	return handlers.New(nil, cfg, nil, nil, nil, nil, nil, nil, nil)
}

func TestGetRoot_returnsEndpointList(t *testing.T) {
	h := newHandler(testConfig())
	resp, err := h.GetRoot(context.Background(), api.GetRootRequestObject{})
	require.NoError(t, err)
	body, ok := resp.(api.GetRoot200JSONResponse)
	require.True(t, ok)
	assert.ElementsMatch(t,
		[]string{"service", "flows", "sources", "flow-delete-requests"},
		[]string(body),
	)
}

func TestHeadRoot_returns200(t *testing.T) {
	h := newHandler(testConfig())
	resp, err := h.HeadRoot(context.Background(), api.HeadRootRequestObject{})
	require.NoError(t, err)
	_, ok := resp.(api.HeadRoot200Response)
	assert.True(t, ok)
}

func TestGetService_returnsRequiredFields(t *testing.T) {
	h := newHandler(testConfig())
	resp, err := h.GetService(context.Background(), api.GetServiceRequestObject{})
	require.NoError(t, err)
	body, ok := resp.(api.GetService200JSONResponse)
	require.True(t, ok)
	svc := api.Service(body)
	assert.Equal(t, "8.0", svc.ApiVersion)
	assert.Equal(t, "urn:x-tams:service:tams", svc.Type)
	assert.Equal(t, "3600:0", svc.MinObjectTimeout)
}

func TestHeadService_returns200(t *testing.T) {
	h := newHandler(testConfig())
	resp, err := h.HeadService(context.Background(), api.HeadServiceRequestObject{})
	require.NoError(t, err)
	_, ok := resp.(api.HeadService200Response)
	assert.True(t, ok)
}

func TestGetStorageBackends_returnsBackendFromConfig(t *testing.T) {
	cfg := testConfig()
	h := newHandler(cfg)
	resp, err := h.GetStorageBackends(context.Background(), api.GetStorageBackendsRequestObject{})
	require.NoError(t, err)
	body, ok := resp.(api.GetStorageBackends200JSONResponse)
	require.True(t, ok)
	list := api.StorageBackendsList(body)
	require.Len(t, list, 1)
	backend := list[0]
	assert.Equal(t, cfg.StorageBackendProvider, backend.Provider)
	assert.Equal(t, cfg.StorageBackendProduct, backend.StoreProduct)
	assert.Equal(t, api.StorageBackendsListStoreTypeHttpObjectStore, backend.StoreType)
	assert.Equal(t, cfg.ObjectStoreRegion, *backend.Region)
	assert.NotEmpty(t, backend.Id)
}

func TestHeadStorageBackends_returns200(t *testing.T) {
	h := newHandler(testConfig())
	resp, err := h.HeadStorageBackends(context.Background(), api.HeadStorageBackendsRequestObject{})
	require.NoError(t, err)
	_, ok := resp.(api.HeadStorageBackends200Response)
	assert.True(t, ok)
}
