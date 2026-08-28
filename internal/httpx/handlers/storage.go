package handlers

import (
	"context"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/internal/service/storage"
	"github.com/amagioss/opentams/pkg/logger"
)

func (h *Handler) PostFlowStorage(ctx context.Context, req api.PostFlowStorageRequestObject) (api.PostFlowStorageResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	flowID, err := uuid.Parse(req.FlowId)
	if err != nil {
		log.Warn("invalid flow ID")
		return api.PostFlowStorage404ApplicationProblemPlusJSONResponse{}, nil
	}

	if req.Body == nil {
		return api.PostFlowStorage400ApplicationProblemPlusJSONResponse{}, nil
	}

	// `not: allOf` in the OpenAPI spec is not enforced by the decoder, so
	// enforce mutual exclusivity here: exactly one of limit or object_ids.
	hasLimit := req.Body.Limit != nil
	hasIDs := req.Body.ObjectIds != nil && len(*req.Body.ObjectIds) > 0
	if hasLimit == hasIDs {
		return api.PostFlowStorage400ApplicationProblemPlusJSONResponse{}, nil
	}

	allocReq := storage.AllocateRequest{Limit: req.Body.Limit}
	if hasIDs {
		allocReq.ObjectIDs = *req.Body.ObjectIds
	}

	objs, err := h.storage.AllocateStorage(ctx, flowID, allocReq)
	if err != nil {
		if isNotFound(err) {
			return api.PostFlowStorage404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("AllocateStorage failed", zap.Error(err))
		return nil, err
	}

	media := make([]struct {
		ObjectId string          `json:"object_id"`
		PutUrl   api.HttpRequest `json:"put_url"`
	}, len(objs))
	for i, o := range objs {
		media[i].ObjectId = o.ObjectID
		media[i].PutUrl = api.HttpRequest{Url: o.PutURL}
		if o.ContentType != "" {
			ct := o.ContentType
			media[i].PutUrl.ContentType = &ct
		}
	}

	log.Info("storage allocated", zap.Int("count", len(objs)))
	return api.PostFlowStorage201JSONResponse{MediaObjects: &media}, nil
}
