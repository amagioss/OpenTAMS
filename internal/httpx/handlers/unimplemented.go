package handlers

import (
	"context"

	"github.com/amagioss/opentams/gen/api"
)

// Health endpoints (GetHealthz / GetReadyz / GetHealthDetails) are implemented
// in health.go and are NOT stubs here. The flow-delete-request endpoints below
// are M16 (GC) surface and remain stubbed until that module lands.

func (h *Handler) GetFlowDeleteRequests(ctx context.Context, req api.GetFlowDeleteRequestsRequestObject) (api.GetFlowDeleteRequestsResponseObject, error) {
	return api.GetFlowDeleteRequests200JSONResponse{}, nil
}

func (h *Handler) HeadFlowDeleteRequests(ctx context.Context, req api.HeadFlowDeleteRequestsRequestObject) (api.HeadFlowDeleteRequestsResponseObject, error) {
	return api.HeadFlowDeleteRequests200Response{}, nil
}

func (h *Handler) GetFlowDeleteRequest(ctx context.Context, req api.GetFlowDeleteRequestRequestObject) (api.GetFlowDeleteRequestResponseObject, error) {
	return api.GetFlowDeleteRequest404ApplicationProblemPlusJSONResponse{}, nil
}

func (h *Handler) HeadFlowDeleteRequest(ctx context.Context, req api.HeadFlowDeleteRequestRequestObject) (api.HeadFlowDeleteRequestResponseObject, error) {
	return api.HeadFlowDeleteRequest404ApplicationProblemPlusJSONResponse{}, nil
}
