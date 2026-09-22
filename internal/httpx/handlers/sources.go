package handlers

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/metastore"
	"github.com/amagioss/opentams/pkg/logger"
)

func (h *Handler) GetSources(ctx context.Context, req api.GetSourcesRequestObject) (api.GetSourcesResponseObject, error) {
	log := logger.FromContext(ctx, h.log)

	p := metastore.ListSourcesParams{Limit: 100}
	if req.Params.Limit != nil {
		p.Limit = *req.Params.Limit
		if p.Limit > 1000 {
			p.Limit = 1000
		}
	}
	if req.Params.Page != nil {
		p.PageFrom = req.Params.Page
	}
	if req.Params.Label != nil {
		s := string(*req.Params.Label)
		p.Label = &s
	}
	if req.Params.Format != nil {
		s := string(*req.Params.Format)
		p.Format = &s
	}

	log.Info("listing sources", zap.Int("limit", p.Limit))

	page, err := h.sources.ListSources(ctx, p)
	if err != nil {
		log.Error("ListSources failed", zap.Error(err))
		return nil, err
	}

	items := make([]api.Source, len(page.Items))
	for i, s := range page.Items {
		items[i] = mapSource(s)
	}

	resp := api.GetSources200JSONResponse{
		Body: items,
		Headers: api.GetSources200ResponseHeaders{
			XPagingLimit: ptr(p.Limit),
		},
	}
	if page.NextCursor != nil {
		resp.Headers.XPagingNextKey = ptr(*page.NextCursor)
		resp.Headers.Link = ptr(`<>; rel="next"; key="` + *page.NextCursor + `"`)
	}
	return resp, nil
}

func (h *Handler) HeadSources(ctx context.Context, req api.HeadSourcesRequestObject) (api.HeadSourcesResponseObject, error) {
	log := logger.FromContext(ctx, h.log)

	p := metastore.ListSourcesParams{Limit: 100}
	if req.Params.Limit != nil {
		p.Limit = *req.Params.Limit
	}
	if req.Params.Page != nil {
		p.PageFrom = req.Params.Page
	}

	page, err := h.sources.ListSources(ctx, p)
	if err != nil {
		log.Error("HeadSources: ListSources failed", zap.Error(err))
		return nil, err
	}

	resp := api.HeadSources200Response{
		Headers: api.PagedListingHeadResponseHeaders{
			XPagingLimit: ptr(p.Limit),
		},
	}
	if page.NextCursor != nil {
		resp.Headers.XPagingNextKey = ptr(*page.NextCursor)
		resp.Headers.Link = ptr(`<>; rel="next"; key="` + *page.NextCursor + `"`)
	}
	return resp, nil
}

func (h *Handler) GetSource(ctx context.Context, req api.GetSourceRequestObject) (api.GetSourceResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("source_id", req.SourceId))

	id, err := parseSourceID(req.SourceId)
	if err != nil {
		log.Warn("invalid source ID")
		return api.GetSource404ApplicationProblemPlusJSONResponse{}, nil
	}
	src, err := h.sources.GetSource(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return api.GetSource404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("GetSource failed", zap.Error(err))
		return nil, err
	}
	return api.GetSource200JSONResponse(mapSource(src)), nil
}

func (h *Handler) HeadSource(ctx context.Context, req api.HeadSourceRequestObject) (api.HeadSourceResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("source_id", req.SourceId))

	id, err := parseSourceID(req.SourceId)
	if err != nil {
		log.Warn("invalid source ID")
		return api.HeadSource404ApplicationProblemPlusJSONResponse{}, nil
	}
	_, err = h.sources.GetSource(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return api.HeadSource404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("HeadSource: GetSource failed", zap.Error(err))
		return nil, err
	}
	return api.HeadSource200Response{}, nil
}

func (h *Handler) GetSourceDescription(ctx context.Context, req api.GetSourceDescriptionRequestObject) (api.GetSourceDescriptionResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("source_id", req.SourceId))

	id, err := parseSourceID(req.SourceId)
	if err != nil {
		log.Warn("invalid source ID")
		return api.GetSourceDescription404ApplicationProblemPlusJSONResponse{}, nil
	}
	src, err := h.sources.GetSource(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return api.GetSourceDescription404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("GetSourceDescription: GetSource failed", zap.Error(err))
		return nil, err
	}
	if src.Description == nil {
		return api.GetSourceDescription404ApplicationProblemPlusJSONResponse{}, nil
	}
	return api.GetSourceDescription200JSONResponse(*src.Description), nil
}

func (h *Handler) HeadSourceDescription(ctx context.Context, req api.HeadSourceDescriptionRequestObject) (api.HeadSourceDescriptionResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("source_id", req.SourceId))

	id, err := parseSourceID(req.SourceId)
	if err != nil {
		log.Warn("invalid source ID")
		return api.HeadSourceDescription404ApplicationProblemPlusJSONResponse{}, nil
	}
	src, err := h.sources.GetSource(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return api.HeadSourceDescription404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("HeadSourceDescription: GetSource failed", zap.Error(err))
		return nil, err
	}
	if src.Description == nil {
		return api.HeadSourceDescription404ApplicationProblemPlusJSONResponse{}, nil
	}
	return api.HeadSourceDescription200Response{}, nil
}

func (h *Handler) PutSourceDescription(ctx context.Context, req api.PutSourceDescriptionRequestObject) (api.PutSourceDescriptionResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("source_id", req.SourceId))

	id, err := parseSourceID(req.SourceId)
	if err != nil {
		log.Warn("invalid source ID")
		return api.PutSourceDescription404ApplicationProblemPlusJSONResponse{}, nil
	}
	if err := h.sources.PutSourceDescription(ctx, id, *req.Body); err != nil {
		if isNotFound(err) {
			return api.PutSourceDescription404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("PutSourceDescription failed", zap.Error(err))
		return nil, err
	}
	log.Info("source description updated")
	return api.PutSourceDescription204Response{}, nil
}

func (h *Handler) DeleteSourceDescription(ctx context.Context, req api.DeleteSourceDescriptionRequestObject) (api.DeleteSourceDescriptionResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("source_id", req.SourceId))

	id, err := parseSourceID(req.SourceId)
	if err != nil {
		log.Warn("invalid source ID")
		return api.DeleteSourceDescription404ApplicationProblemPlusJSONResponse{}, nil
	}
	if err := h.sources.DeleteSourceDescription(ctx, id); err != nil {
		if isNotFound(err) {
			return api.DeleteSourceDescription404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("DeleteSourceDescription failed", zap.Error(err))
		return nil, err
	}
	log.Info("source description deleted")
	return api.DeleteSourceDescription204Response{}, nil
}

func (h *Handler) GetSourceLabel(ctx context.Context, req api.GetSourceLabelRequestObject) (api.GetSourceLabelResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("source_id", req.SourceId))

	id, err := parseSourceID(req.SourceId)
	if err != nil {
		log.Warn("invalid source ID")
		return api.GetSourceLabel404ApplicationProblemPlusJSONResponse{}, nil
	}
	src, err := h.sources.GetSource(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return api.GetSourceLabel404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("GetSourceLabel: GetSource failed", zap.Error(err))
		return nil, err
	}
	if src.Label == nil {
		return api.GetSourceLabel404ApplicationProblemPlusJSONResponse{}, nil
	}
	return api.GetSourceLabel200JSONResponse(*src.Label), nil
}

func (h *Handler) HeadSourceLabel(ctx context.Context, req api.HeadSourceLabelRequestObject) (api.HeadSourceLabelResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("source_id", req.SourceId))

	id, err := parseSourceID(req.SourceId)
	if err != nil {
		log.Warn("invalid source ID")
		return api.HeadSourceLabel404ApplicationProblemPlusJSONResponse{}, nil
	}
	src, err := h.sources.GetSource(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return api.HeadSourceLabel404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("HeadSourceLabel: GetSource failed", zap.Error(err))
		return nil, err
	}
	if src.Label == nil {
		return api.HeadSourceLabel404ApplicationProblemPlusJSONResponse{}, nil
	}
	return api.HeadSourceLabel200Response{}, nil
}

func (h *Handler) PutSourceLabel(ctx context.Context, req api.PutSourceLabelRequestObject) (api.PutSourceLabelResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("source_id", req.SourceId))

	id, err := parseSourceID(req.SourceId)
	if err != nil {
		log.Warn("invalid source ID")
		return api.PutSourceLabel404ApplicationProblemPlusJSONResponse{}, nil
	}
	if err := h.sources.PutSourceLabel(ctx, id, *req.Body); err != nil {
		if isNotFound(err) {
			return api.PutSourceLabel404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("PutSourceLabel failed", zap.Error(err))
		return nil, err
	}
	log.Info("source label updated")
	return api.PutSourceLabel204Response{}, nil
}

func (h *Handler) DeleteSourceLabel(ctx context.Context, req api.DeleteSourceLabelRequestObject) (api.DeleteSourceLabelResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("source_id", req.SourceId))

	id, err := parseSourceID(req.SourceId)
	if err != nil {
		log.Warn("invalid source ID")
		return api.DeleteSourceLabel404ApplicationProblemPlusJSONResponse{}, nil
	}
	if err := h.sources.DeleteSourceLabel(ctx, id); err != nil {
		if isNotFound(err) {
			return api.DeleteSourceLabel404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("DeleteSourceLabel failed", zap.Error(err))
		return nil, err
	}
	log.Info("source label deleted")
	return api.DeleteSourceLabel204Response{}, nil
}

func (h *Handler) GetSourceTags(ctx context.Context, req api.GetSourceTagsRequestObject) (api.GetSourceTagsResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("source_id", req.SourceId))

	id, err := parseSourceID(req.SourceId)
	if err != nil {
		log.Warn("invalid source ID")
		return api.GetSourceTags404ApplicationProblemPlusJSONResponse{}, nil
	}
	src, err := h.sources.GetSource(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return api.GetSourceTags404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("GetSourceTags: GetSource failed", zap.Error(err))
		return nil, err
	}
	tags := make(api.Tags, len(src.Tags))
	for k, v := range src.Tags {
		tags[k] = api.NewTagsAdditionalProperties(v)
	}
	return api.GetSourceTags200JSONResponse(tags), nil
}

func (h *Handler) HeadSourceTags(ctx context.Context, req api.HeadSourceTagsRequestObject) (api.HeadSourceTagsResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("source_id", req.SourceId))

	id, err := parseSourceID(req.SourceId)
	if err != nil {
		log.Warn("invalid source ID")
		return api.HeadSourceTags404ApplicationProblemPlusJSONResponse{}, nil
	}
	_, err = h.sources.GetSource(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return api.HeadSourceTags404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("HeadSourceTags: GetSource failed", zap.Error(err))
		return nil, err
	}
	return api.HeadSourceTags200Response{}, nil
}

func (h *Handler) GetSourceTag(ctx context.Context, req api.GetSourceTagRequestObject) (api.GetSourceTagResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(
		zap.String("source_id", req.SourceId),
		zap.String("tag", req.Name),
	)

	id, err := parseSourceID(req.SourceId)
	if err != nil {
		log.Warn("invalid source ID")
		return api.GetSourceTag404ApplicationProblemPlusJSONResponse{}, nil
	}
	src, err := h.sources.GetSource(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return api.GetSourceTag404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("GetSourceTag: GetSource failed", zap.Error(err))
		return nil, err
	}
	val, ok := src.Tags[req.Name]
	if !ok {
		return api.GetSourceTag404ApplicationProblemPlusJSONResponse{}, nil
	}
	return api.NewGetSourceTag200JSONResponse(val), nil
}

func (h *Handler) HeadSourceTag(ctx context.Context, req api.HeadSourceTagRequestObject) (api.HeadSourceTagResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(
		zap.String("source_id", req.SourceId),
		zap.String("tag", req.Name),
	)

	id, err := parseSourceID(req.SourceId)
	if err != nil {
		log.Warn("invalid source ID")
		return api.HeadSourceTag404ApplicationProblemPlusJSONResponse{}, nil
	}
	src, err := h.sources.GetSource(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return api.HeadSourceTag404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("HeadSourceTag: GetSource failed", zap.Error(err))
		return nil, err
	}
	if _, ok := src.Tags[req.Name]; !ok {
		return api.HeadSourceTag404ApplicationProblemPlusJSONResponse{}, nil
	}
	return api.HeadSourceTag200Response{}, nil
}

func (h *Handler) PutSourceTag(ctx context.Context, req api.PutSourceTagRequestObject) (api.PutSourceTagResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(
		zap.String("source_id", req.SourceId),
		zap.String("tag", req.Name),
	)

	id, err := parseSourceID(req.SourceId)
	if err != nil {
		log.Warn("invalid source ID")
		return api.PutSourceTag404ApplicationProblemPlusJSONResponse{}, nil
	}
	rawVal := api.PutSourceTagJSONBody(*req.Body).RawValue()
	if err := h.sources.PutSourceTag(ctx, id, req.Name, rawVal); err != nil {
		if isNotFound(err) {
			return api.PutSourceTag404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("PutSourceTag failed", zap.Error(err))
		return nil, err
	}
	log.Info("source tag updated")
	return api.PutSourceTag204Response{}, nil
}

func (h *Handler) DeleteSourceTag(ctx context.Context, req api.DeleteSourceTagRequestObject) (api.DeleteSourceTagResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(
		zap.String("source_id", req.SourceId),
		zap.String("tag", req.Name),
	)

	id, err := parseSourceID(req.SourceId)
	if err != nil {
		log.Warn("invalid source ID")
		return api.DeleteSourceTag404ApplicationProblemPlusJSONResponse{}, nil
	}
	if err := h.sources.DeleteSourceTag(ctx, id, req.Name); err != nil {
		if isNotFound(err) {
			return api.DeleteSourceTag404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("DeleteSourceTag failed", zap.Error(err))
		return nil, err
	}
	log.Info("source tag deleted")
	return api.DeleteSourceTag204Response{}, nil
}

func parseSourceID(s string) (uuid.UUID, error) {
	return uuid.Parse(s)
}

func isNotFound(err error) bool {
	var ae *apperror.AppError
	return errors.As(err, &ae) && ae.Code == apperror.ErrNotFound
}

func mapSource(s *metastore.Source) api.Source {
	src := api.Source{
		Id:      s.ID.String(),
		Format:  api.ContentFormat(s.Format),
		Created: &s.Created,
		Updated: &s.Updated,
	}
	if s.Label != nil {
		src.Label = s.Label
	}
	if s.Description != nil {
		src.Description = s.Description
	}
	if len(s.Tags) > 0 {
		tags := make(api.Tags, len(s.Tags))
		for k, v := range s.Tags {
			tags[k] = api.NewTagsAdditionalProperties(v)
		}
		src.Tags = &tags
	}
	return src
}
