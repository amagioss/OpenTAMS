package handlers

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/amagioss/opentams/gen/api"
)

const (
	tamsAPIVersion  = "8.0"
	tamsServiceType = "urn:x-tams:service:tams"
)

var storageBackendNS = uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8")

func (h *Handler) GetRoot(ctx context.Context, req api.GetRootRequestObject) (api.GetRootResponseObject, error) {
	return api.GetRoot200JSONResponse([]string{"service", "flows", "sources", "flow-delete-requests"}), nil
}

func (h *Handler) HeadRoot(ctx context.Context, req api.HeadRootRequestObject) (api.HeadRootResponseObject, error) {
	return api.HeadRoot200Response{}, nil
}

func (h *Handler) GetService(ctx context.Context, req api.GetServiceRequestObject) (api.GetServiceResponseObject, error) {
	return api.GetService200JSONResponse(api.Service{
		ApiVersion:       tamsAPIVersion,
		Type:             tamsServiceType,
		MinObjectTimeout: durationToTAI(h.cfg.ObjectStorePresignExpiry),
	}), nil
}

func (h *Handler) HeadService(ctx context.Context, req api.HeadServiceRequestObject) (api.HeadServiceResponseObject, error) {
	return api.HeadService200Response{}, nil
}

func (h *Handler) GetStorageBackends(ctx context.Context, req api.GetStorageBackendsRequestObject) (api.GetStorageBackendsResponseObject, error) {
	id := uuid.NewSHA1(storageBackendNS, []byte(h.cfg.StorageBackendProvider+"/"+h.cfg.ObjectStoreRegion+"/"+h.cfg.ObjectStoreBucket))
	region := h.cfg.ObjectStoreRegion
	backend := struct {
		AvailabilityZone *string                          `json:"availability_zone,omitempty"`
		DefaultStorage   *bool                            `json:"default_storage,omitempty"`
		Id               api.Uuid                         `json:"id"`
		Label            *string                          `json:"label,omitempty"`
		Provider         string                           `json:"provider"`
		Region           *string                          `json:"region,omitempty"`
		StoreProduct     string                           `json:"store_product"`
		StoreType        api.StorageBackendsListStoreType `json:"store_type"`
	}{
		Id:           id.String(),
		Provider:     h.cfg.StorageBackendProvider,
		StoreProduct: h.cfg.StorageBackendProduct,
		StoreType:    api.StorageBackendsListStoreTypeHttpObjectStore,
		Region:       &region,
	}
	return api.GetStorageBackends200JSONResponse(api.StorageBackendsList{backend}), nil
}

func (h *Handler) HeadStorageBackends(ctx context.Context, req api.HeadStorageBackendsRequestObject) (api.HeadStorageBackendsResponseObject, error) {
	return api.HeadStorageBackends200Response{}, nil
}

func durationToTAI(d time.Duration) string {
	secs := int64(d) / 1_000_000_000
	ns := int64(d) % 1_000_000_000
	return fmt.Sprintf("%d:%d", secs, ns)
}
