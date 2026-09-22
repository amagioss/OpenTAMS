package handlers

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/internal/metastore"
	"github.com/amagioss/opentams/internal/timerange"
	"github.com/amagioss/opentams/pkg/logger"
)

// flowCommonJSON is the shared JSON shape for all Flow variants.
type flowCommonJSON struct {
	ID                string                     `json:"id"`
	SourceID          string                     `json:"source_id"`
	Format            string                     `json:"format"`
	Codec             *string                    `json:"codec,omitempty"`
	Container         *string                    `json:"container,omitempty"`
	Label             *string                    `json:"label,omitempty"`
	Description       *string                    `json:"description,omitempty"`
	Tags              map[string]json.RawMessage `json:"tags,omitempty"`
	EssenceParameters json.RawMessage            `json:"essence_parameters,omitempty"`
	ContainerMapping  json.RawMessage            `json:"container_mapping,omitempty"`
	FlowCollection    []collItemJSON             `json:"flow_collection,omitempty"`
	AvgBitRate        *int64                     `json:"avg_bit_rate,omitempty"`
	MaxBitRate        *int64                     `json:"max_bit_rate,omitempty"`
	Generation        *int64                     `json:"generation,omitempty"`
	MetadataVersion   *string                    `json:"metadata_version,omitempty"`
	ReadOnly          bool                       `json:"read_only"`
	Created           time.Time                  `json:"created"`
	MetadataUpdated   time.Time                  `json:"metadata_updated,omitempty"`
	SegmentsUpdated   *time.Time                 `json:"segments_updated,omitempty"`
	Timerange         *string                    `json:"timerange,omitempty"`
}

type collItemJSON struct {
	ID   string          `json:"id"`
	Role string          `json:"role,omitempty"`
	CM   json.RawMessage `json:"container_mapping,omitempty"`
}

func mapFlowToAPI(f *metastore.Flow) (api.Flow, error) {
	j := flowCommonJSON{
		ID:                f.ID.String(),
		SourceID:          f.SourceID.String(),
		Format:            f.Format,
		Codec:             f.Codec,
		Container:         f.Container,
		Label:             f.Label,
		Description:       f.Description,
		EssenceParameters: f.EssenceParameters,
		ContainerMapping:  f.ContainerMapping,
		ReadOnly:          f.ReadOnly,
		Created:           f.Created,
		MetadataUpdated:   f.MetadataUpdated,
		SegmentsUpdated:   f.SegmentsUpdated,
		Timerange:         f.Timerange,
	}
	if len(f.Tags) > 0 {
		j.Tags = f.Tags
	}
	if f.AvgBitRate != nil {
		j.AvgBitRate = f.AvgBitRate
	}
	if f.MaxBitRate != nil {
		j.MaxBitRate = f.MaxBitRate
	}
	if f.Generation != nil {
		j.Generation = f.Generation
	}
	if f.MetadataVersion != nil {
		s := strconv.FormatInt(*f.MetadataVersion, 10)
		j.MetadataVersion = &s
	}
	if len(f.FlowCollection) > 0 {
		j.FlowCollection = make([]collItemJSON, len(f.FlowCollection))
		for i, ci := range f.FlowCollection {
			j.FlowCollection[i] = collItemJSON{ID: ci.ID.String(), CM: ci.ContainerMapping}
			if ci.Role != nil {
				j.FlowCollection[i].Role = *ci.Role
			}
		}
	}
	raw, err := json.Marshal(j)
	if err != nil {
		return api.Flow{}, err
	}
	var result api.Flow
	if err := result.UnmarshalJSON(raw); err != nil {
		return api.Flow{}, err
	}
	return result, nil
}

// apiFlowBase is used to decode the common fields from an api.Flow union.
type apiFlowBase struct {
	ID                uuid.UUID                  `json:"id"`
	SourceID          uuid.UUID                  `json:"source_id"`
	Format            string                     `json:"format"`
	Codec             *string                    `json:"codec"`
	Container         *string                    `json:"container"`
	Label             *string                    `json:"label"`
	Description       *string                    `json:"description"`
	Tags              map[string]json.RawMessage `json:"tags"`
	EssenceParameters json.RawMessage            `json:"essence_parameters"`
	ContainerMapping  json.RawMessage            `json:"container_mapping"`
	FlowCollection    []struct {
		ID   uuid.UUID       `json:"id"`
		Role *string         `json:"role"`
		CM   json.RawMessage `json:"container_mapping"`
	} `json:"flow_collection"`
	AvgBitRate      *int    `json:"avg_bit_rate"`
	MaxBitRate      *int    `json:"max_bit_rate"`
	Generation      *int    `json:"generation"`
	MetadataVersion *string `json:"metadata_version"`
	ReadOnly        *bool   `json:"read_only"`
}

func parseFlowBody(body *api.Flow) (apiFlowBase, error) {
	raw, err := body.MarshalJSON()
	if err != nil {
		return apiFlowBase{}, err
	}
	var base apiFlowBase
	if err := json.Unmarshal(raw, &base); err != nil {
		return apiFlowBase{}, err
	}
	return base, nil
}

func apiFlowToMetastore(flowID uuid.UUID, base apiFlowBase) *metastore.Flow {
	f := &metastore.Flow{
		ID:                flowID,
		SourceID:          base.SourceID,
		Format:            base.Format,
		Codec:             base.Codec,
		Container:         base.Container,
		Label:             base.Label,
		Description:       base.Description,
		Tags:              base.Tags,
		EssenceParameters: base.EssenceParameters,
		ContainerMapping:  base.ContainerMapping,
	}
	if base.AvgBitRate != nil {
		v := int64(*base.AvgBitRate)
		f.AvgBitRate = &v
	}
	if base.MaxBitRate != nil {
		v := int64(*base.MaxBitRate)
		f.MaxBitRate = &v
	}
	if base.Generation != nil {
		v := int64(*base.Generation)
		f.Generation = &v
	}
	if base.MetadataVersion != nil {
		if v, err := strconv.ParseInt(*base.MetadataVersion, 10, 64); err == nil {
			f.MetadataVersion = &v
		}
	}
	if base.ReadOnly != nil {
		f.ReadOnly = *base.ReadOnly
	}
	if len(base.FlowCollection) > 0 {
		f.FlowCollection = make([]metastore.CollectionItem, len(base.FlowCollection))
		for i, ci := range base.FlowCollection {
			f.FlowCollection[i] = metastore.CollectionItem{
				ID:               ci.ID,
				Role:             ci.Role,
				ContainerMapping: ci.CM,
			}
		}
	}
	return f
}

func parseGetFlowsParams(params api.GetFlowsParams) metastore.ListFlowsParams {
	p := metastore.ListFlowsParams{Limit: 100}
	if params.Limit != nil {
		p.Limit = *params.Limit
		if p.Limit > 1000 {
			p.Limit = 1000
		}
	}
	if params.Page != nil {
		s := string(*params.Page)
		p.PageFrom = &s
	}
	if params.Format != nil {
		s := string(*params.Format)
		p.Format = &s
	}
	if params.Codec != nil {
		s := string(*params.Codec)
		p.Codec = &s
	}
	if params.Label != nil {
		s := string(*params.Label)
		p.Label = &s
	}
	if params.FrameWidth != nil {
		v := *params.FrameWidth
		p.FrameWidth = &v
	}
	if params.FrameHeight != nil {
		v := *params.FrameHeight
		p.FrameHeight = &v
	}
	if params.SourceId != nil {
		if sid, err := uuid.Parse(string(*params.SourceId)); err == nil {
			p.SourceID = &sid
		}
	}
	if params.Timerange != nil {
		if parsed, err := timerange.Parse(string(*params.Timerange)); err == nil {
			p.Timerange = &parsed
		}
	}
	return p
}

func parseHeadFlowsParams(params api.HeadFlowsParams) metastore.ListFlowsParams {
	p := metastore.ListFlowsParams{Limit: 100}
	if params.Limit != nil {
		p.Limit = *params.Limit
		if p.Limit > 1000 {
			p.Limit = 1000
		}
	}
	if params.Page != nil {
		s := string(*params.Page)
		p.PageFrom = &s
	}
	if params.Format != nil {
		s := string(*params.Format)
		p.Format = &s
	}
	if params.Codec != nil {
		s := string(*params.Codec)
		p.Codec = &s
	}
	if params.Label != nil {
		s := string(*params.Label)
		p.Label = &s
	}
	if params.FrameWidth != nil {
		v := *params.FrameWidth
		p.FrameWidth = &v
	}
	if params.FrameHeight != nil {
		v := *params.FrameHeight
		p.FrameHeight = &v
	}
	if params.SourceId != nil {
		if sid, err := uuid.Parse(string(*params.SourceId)); err == nil {
			p.SourceID = &sid
		}
	}
	if params.Timerange != nil {
		if parsed, err := timerange.Parse(string(*params.Timerange)); err == nil {
			p.Timerange = &parsed
		}
	}
	return p
}

func (h *Handler) GetFlows(ctx context.Context, req api.GetFlowsRequestObject) (api.GetFlowsResponseObject, error) {
	log := logger.FromContext(ctx, h.log)

	p := parseGetFlowsParams(req.Params)

	log.Info("listing flows", zap.Int("limit", p.Limit))

	page, err := h.flows.ListFlows(ctx, p)
	if err != nil {
		log.Error("ListFlows failed", zap.Error(err))
		return nil, err
	}

	items := make([]api.Flow, 0, len(page.Items))
	for _, f := range page.Items {
		af, err := mapFlowToAPI(f)
		if err != nil {
			log.Error("mapFlowToAPI failed", zap.Error(err))
			return nil, err
		}
		items = append(items, af)
	}

	resp := api.GetFlows200JSONResponse{
		Body:    items,
		Headers: api.GetFlows200ResponseHeaders{XPagingLimit: ptr(p.Limit)},
	}
	if page.NextCursor != nil {
		resp.Headers.XPagingNextKey = ptr(*page.NextCursor)
		resp.Headers.Link = ptr(`<>; rel="next"; key="` + *page.NextCursor + `"`)
	}
	return resp, nil
}

func (h *Handler) HeadFlows(ctx context.Context, req api.HeadFlowsRequestObject) (api.HeadFlowsResponseObject, error) {
	log := logger.FromContext(ctx, h.log)

	p := parseHeadFlowsParams(req.Params)

	page, err := h.flows.ListFlows(ctx, p)
	if err != nil {
		log.Error("HeadFlows: ListFlows failed", zap.Error(err))
		return nil, err
	}

	resp := api.HeadFlows200Response{
		Headers: api.PagedListingHeadResponseHeaders{XPagingLimit: ptr(p.Limit)},
	}
	if page.NextCursor != nil {
		resp.Headers.XPagingNextKey = ptr(*page.NextCursor)
		resp.Headers.Link = ptr(`<>; rel="next"; key="` + *page.NextCursor + `"`)
	}
	return resp, nil
}

func (h *Handler) GetFlow(ctx context.Context, req api.GetFlowRequestObject) (api.GetFlowResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		log.Warn("invalid flow ID")
		return api.GetFlow404ApplicationProblemPlusJSONResponse{}, nil
	}

	includeTimerange := req.Params.IncludeTimerange != nil && *req.Params.IncludeTimerange
	var trFilter *timerange.TimeRange
	if req.Params.Timerange != nil {
		if tr, err := timerange.Parse(string(*req.Params.Timerange)); err == nil {
			trFilter = &tr
		}
	}

	f, err := h.flows.GetFlow(ctx, id, includeTimerange, trFilter)
	if err != nil {
		if isNotFound(err) {
			return api.GetFlow404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("GetFlow failed", zap.Error(err))
		return nil, err
	}

	af, err := mapFlowToAPI(f)
	if err != nil {
		log.Error("mapFlowToAPI failed", zap.Error(err))
		return nil, err
	}
	return api.GetFlow200JSONResponse(af), nil
}

func (h *Handler) HeadFlow(ctx context.Context, req api.HeadFlowRequestObject) (api.HeadFlowResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		log.Warn("invalid flow ID")
		return api.HeadFlow404ApplicationProblemPlusJSONResponse{}, nil
	}
	_, err = h.flows.GetFlow(ctx, id, false, nil)
	if err != nil {
		if isNotFound(err) {
			return api.HeadFlow404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("HeadFlow: GetFlow failed", zap.Error(err))
		return nil, err
	}
	return api.HeadFlow200Response{}, nil
}

func (h *Handler) PutFlow(ctx context.Context, req api.PutFlowRequestObject) (api.PutFlowResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	flowID, err := uuid.Parse(req.FlowId)
	if err != nil {
		log.Warn("invalid flow ID")
		return api.PutFlow400ApplicationProblemPlusJSONResponse{}, nil
	}

	base, err := parseFlowBody(req.Body)
	if err != nil {
		log.Warn("invalid flow body", zap.Error(err))
		return api.PutFlow400ApplicationProblemPlusJSONResponse{}, nil
	}

	f := apiFlowToMetastore(flowID, base)
	created, err := h.flows.UpsertFlow(ctx, f)
	if err != nil {
		log.Error("UpsertFlow failed", zap.Error(err))
		return nil, err
	}

	if !created {
		log.Info("flow updated")
		return api.PutFlow204Response{}, nil
	}

	log.Info("flow created")
	stored, err := h.flows.GetFlow(ctx, flowID, false, nil)
	if err != nil {
		log.Error("PutFlow: GetFlow after create failed", zap.Error(err))
		return nil, err
	}
	af, err := mapFlowToAPI(stored)
	if err != nil {
		log.Error("mapFlowToAPI failed", zap.Error(err))
		return nil, err
	}
	return api.PutFlow201JSONResponse(af), nil
}

func (h *Handler) DeleteFlow(ctx context.Context, req api.DeleteFlowRequestObject) (api.DeleteFlowResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		log.Warn("invalid flow ID")
		return api.DeleteFlow404ApplicationProblemPlusJSONResponse{}, nil
	}
	if err := h.flows.DeleteFlow(ctx, id); err != nil {
		if isNotFound(err) {
			return api.DeleteFlow404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("DeleteFlow failed", zap.Error(err))
		return nil, err
	}
	log.Info("flow deleted")
	return api.DeleteFlow204Response{}, nil
}

// --- Description ---

func (h *Handler) GetFlowDescription(ctx context.Context, req api.GetFlowDescriptionRequestObject) (api.GetFlowDescriptionResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.GetFlowDescription404ApplicationProblemPlusJSONResponse{}, nil
	}
	f, err := h.flows.GetFlow(ctx, id, false, nil)
	if err != nil {
		if isNotFound(err) {
			return api.GetFlowDescription404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("GetFlowDescription: GetFlow failed", zap.Error(err))
		return nil, err
	}
	if f.Description == nil {
		return api.GetFlowDescription404ApplicationProblemPlusJSONResponse{}, nil
	}
	return api.GetFlowDescription200JSONResponse(*f.Description), nil
}

func (h *Handler) HeadFlowDescription(ctx context.Context, req api.HeadFlowDescriptionRequestObject) (api.HeadFlowDescriptionResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.HeadFlowDescription404ApplicationProblemPlusJSONResponse{}, nil
	}
	f, err := h.flows.GetFlow(ctx, id, false, nil)
	if err != nil {
		if isNotFound(err) {
			return api.HeadFlowDescription404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("HeadFlowDescription: GetFlow failed", zap.Error(err))
		return nil, err
	}
	if f.Description == nil {
		return api.HeadFlowDescription404ApplicationProblemPlusJSONResponse{}, nil
	}
	return api.HeadFlowDescription200Response{}, nil
}

func (h *Handler) PutFlowDescription(ctx context.Context, req api.PutFlowDescriptionRequestObject) (api.PutFlowDescriptionResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.PutFlowDescription404ApplicationProblemPlusJSONResponse{}, nil
	}
	if err := h.flows.PutFlowDescription(ctx, id, *req.Body); err != nil {
		if isNotFound(err) {
			return api.PutFlowDescription404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("PutFlowDescription failed", zap.Error(err))
		return nil, err
	}
	log.Info("flow description updated")
	return api.PutFlowDescription204Response{}, nil
}

func (h *Handler) DeleteFlowDescription(ctx context.Context, req api.DeleteFlowDescriptionRequestObject) (api.DeleteFlowDescriptionResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.DeleteFlowDescription404ApplicationProblemPlusJSONResponse{}, nil
	}
	if err := h.flows.DeleteFlowDescription(ctx, id); err != nil {
		if isNotFound(err) {
			return api.DeleteFlowDescription404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("DeleteFlowDescription failed", zap.Error(err))
		return nil, err
	}
	log.Info("flow description deleted")
	return api.DeleteFlowDescription204Response{}, nil
}

// --- Label ---

func (h *Handler) GetFlowLabel(ctx context.Context, req api.GetFlowLabelRequestObject) (api.GetFlowLabelResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.GetFlowLabel404ApplicationProblemPlusJSONResponse{}, nil
	}
	f, err := h.flows.GetFlow(ctx, id, false, nil)
	if err != nil {
		if isNotFound(err) {
			return api.GetFlowLabel404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("GetFlowLabel: GetFlow failed", zap.Error(err))
		return nil, err
	}
	if f.Label == nil {
		return api.GetFlowLabel404ApplicationProblemPlusJSONResponse{}, nil
	}
	return api.GetFlowLabel200JSONResponse(*f.Label), nil
}

func (h *Handler) HeadFlowLabel(ctx context.Context, req api.HeadFlowLabelRequestObject) (api.HeadFlowLabelResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.HeadFlowLabel404ApplicationProblemPlusJSONResponse{}, nil
	}
	f, err := h.flows.GetFlow(ctx, id, false, nil)
	if err != nil {
		if isNotFound(err) {
			return api.HeadFlowLabel404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("HeadFlowLabel: GetFlow failed", zap.Error(err))
		return nil, err
	}
	if f.Label == nil {
		return api.HeadFlowLabel404ApplicationProblemPlusJSONResponse{}, nil
	}
	return api.HeadFlowLabel200Response{}, nil
}

func (h *Handler) PutFlowLabel(ctx context.Context, req api.PutFlowLabelRequestObject) (api.PutFlowLabelResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.PutFlowLabel404ApplicationProblemPlusJSONResponse{}, nil
	}
	if err := h.flows.PutFlowLabel(ctx, id, *req.Body); err != nil {
		if isNotFound(err) {
			return api.PutFlowLabel404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("PutFlowLabel failed", zap.Error(err))
		return nil, err
	}
	log.Info("flow label updated")
	return api.PutFlowLabel204Response{}, nil
}

func (h *Handler) DeleteFlowLabel(ctx context.Context, req api.DeleteFlowLabelRequestObject) (api.DeleteFlowLabelResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.DeleteFlowLabel404ApplicationProblemPlusJSONResponse{}, nil
	}
	if err := h.flows.DeleteFlowLabel(ctx, id); err != nil {
		if isNotFound(err) {
			return api.DeleteFlowLabel404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("DeleteFlowLabel failed", zap.Error(err))
		return nil, err
	}
	log.Info("flow label deleted")
	return api.DeleteFlowLabel204Response{}, nil
}

// --- Tags ---

func (h *Handler) GetFlowTags(ctx context.Context, req api.GetFlowTagsRequestObject) (api.GetFlowTagsResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.GetFlowTags404ApplicationProblemPlusJSONResponse{}, nil
	}
	f, err := h.flows.GetFlow(ctx, id, false, nil)
	if err != nil {
		if isNotFound(err) {
			return api.GetFlowTags404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("GetFlowTags: GetFlow failed", zap.Error(err))
		return nil, err
	}
	tags := make(api.Tags, len(f.Tags))
	for k, v := range f.Tags {
		tags[k] = api.NewTagsAdditionalProperties(v)
	}
	return api.GetFlowTags200JSONResponse(tags), nil
}

func (h *Handler) HeadFlowTags(ctx context.Context, req api.HeadFlowTagsRequestObject) (api.HeadFlowTagsResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.HeadFlowTags404ApplicationProblemPlusJSONResponse{}, nil
	}
	_, err = h.flows.GetFlow(ctx, id, false, nil)
	if err != nil {
		if isNotFound(err) {
			return api.HeadFlowTags404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("HeadFlowTags: GetFlow failed", zap.Error(err))
		return nil, err
	}
	return api.HeadFlowTags200Response{}, nil
}

func (h *Handler) GetFlowTag(ctx context.Context, req api.GetFlowTagRequestObject) (api.GetFlowTagResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId), zap.String("tag", req.Name))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.GetFlowTag404ApplicationProblemPlusJSONResponse{}, nil
	}
	f, err := h.flows.GetFlow(ctx, id, false, nil)
	if err != nil {
		if isNotFound(err) {
			return api.GetFlowTag404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("GetFlowTag: GetFlow failed", zap.Error(err))
		return nil, err
	}
	val, ok := f.Tags[req.Name]
	if !ok {
		return api.GetFlowTag404ApplicationProblemPlusJSONResponse{}, nil
	}
	return api.NewGetFlowTag200JSONResponse(val), nil
}

func (h *Handler) HeadFlowTag(ctx context.Context, req api.HeadFlowTagRequestObject) (api.HeadFlowTagResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId), zap.String("tag", req.Name))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.HeadFlowTag404ApplicationProblemPlusJSONResponse{}, nil
	}
	f, err := h.flows.GetFlow(ctx, id, false, nil)
	if err != nil {
		if isNotFound(err) {
			return api.HeadFlowTag404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("HeadFlowTag: GetFlow failed", zap.Error(err))
		return nil, err
	}
	if _, ok := f.Tags[req.Name]; !ok {
		return api.HeadFlowTag404ApplicationProblemPlusJSONResponse{}, nil
	}
	return api.HeadFlowTag200Response{}, nil
}

func (h *Handler) PutFlowTag(ctx context.Context, req api.PutFlowTagRequestObject) (api.PutFlowTagResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId), zap.String("tag", req.Name))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.PutFlowTag404ApplicationProblemPlusJSONResponse{}, nil
	}
	rawVal := api.PutFlowTagJSONBody(*req.Body).RawValue()
	if err := h.flows.PutFlowTag(ctx, id, req.Name, rawVal); err != nil {
		if isNotFound(err) {
			return api.PutFlowTag404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("PutFlowTag failed", zap.Error(err))
		return nil, err
	}
	log.Info("flow tag updated")
	return api.PutFlowTag204Response{}, nil
}

func (h *Handler) DeleteFlowTag(ctx context.Context, req api.DeleteFlowTagRequestObject) (api.DeleteFlowTagResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId), zap.String("tag", req.Name))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.DeleteFlowTag404ApplicationProblemPlusJSONResponse{}, nil
	}
	if err := h.flows.DeleteFlowTag(ctx, id, req.Name); err != nil {
		if isNotFound(err) {
			return api.DeleteFlowTag404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("DeleteFlowTag failed", zap.Error(err))
		return nil, err
	}
	log.Info("flow tag deleted")
	return api.DeleteFlowTag204Response{}, nil
}

// --- Collection ---

func (h *Handler) GetFlowCollection(ctx context.Context, req api.GetFlowCollectionRequestObject) (api.GetFlowCollectionResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.GetFlowCollection404ApplicationProblemPlusJSONResponse{}, nil
	}
	f, err := h.flows.GetFlow(ctx, id, false, nil)
	if err != nil {
		if isNotFound(err) {
			return api.GetFlowCollection404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("GetFlowCollection: GetFlow failed", zap.Error(err))
		return nil, err
	}
	items := make(api.FlowCollection, len(f.FlowCollection))
	for i, ci := range f.FlowCollection {
		items[i].Id = ci.ID.String()
		if ci.Role != nil {
			items[i].Role = *ci.Role
		}
	}
	return api.GetFlowCollection200JSONResponse(items), nil
}

func (h *Handler) HeadFlowCollection(ctx context.Context, req api.HeadFlowCollectionRequestObject) (api.HeadFlowCollectionResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.HeadFlowCollection404ApplicationProblemPlusJSONResponse{}, nil
	}
	_, err = h.flows.GetFlow(ctx, id, false, nil)
	if err != nil {
		if isNotFound(err) {
			return api.HeadFlowCollection404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("HeadFlowCollection: GetFlow failed", zap.Error(err))
		return nil, err
	}
	return api.HeadFlowCollection200Response{}, nil
}

func (h *Handler) PutFlowCollection(ctx context.Context, req api.PutFlowCollectionRequestObject) (api.PutFlowCollectionResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.PutFlowCollection404ApplicationProblemPlusJSONResponse{}, nil
	}
	items := make([]metastore.CollectionItem, len(*req.Body))
	for i, ci := range *req.Body {
		cid, err := uuid.Parse(ci.Id)
		if err != nil {
			return api.PutFlowCollection400ApplicationProblemPlusJSONResponse{}, nil
		}
		items[i] = metastore.CollectionItem{ID: cid, Role: &ci.Role}
	}
	if err := h.flows.PutFlowCollection(ctx, id, items); err != nil {
		if isNotFound(err) {
			return api.PutFlowCollection404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("PutFlowCollection failed", zap.Error(err))
		return nil, err
	}
	log.Info("flow collection updated")
	return api.PutFlowCollection204Response{}, nil
}

func (h *Handler) DeleteFlowCollection(ctx context.Context, req api.DeleteFlowCollectionRequestObject) (api.DeleteFlowCollectionResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.DeleteFlowCollection404ApplicationProblemPlusJSONResponse{}, nil
	}
	if err := h.flows.DeleteFlowCollection(ctx, id); err != nil {
		if isNotFound(err) {
			return api.DeleteFlowCollection404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("DeleteFlowCollection failed", zap.Error(err))
		return nil, err
	}
	log.Info("flow collection deleted")
	return api.DeleteFlowCollection204Response{}, nil
}

// --- AvgBitRate ---

func (h *Handler) GetFlowAvgBitRate(ctx context.Context, req api.GetFlowAvgBitRateRequestObject) (api.GetFlowAvgBitRateResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.GetFlowAvgBitRate404ApplicationProblemPlusJSONResponse{}, nil
	}
	f, err := h.flows.GetFlow(ctx, id, false, nil)
	if err != nil {
		if isNotFound(err) {
			return api.GetFlowAvgBitRate404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("GetFlowAvgBitRate: GetFlow failed", zap.Error(err))
		return nil, err
	}
	if f.AvgBitRate == nil {
		return api.GetFlowAvgBitRate404ApplicationProblemPlusJSONResponse{}, nil
	}
	return api.GetFlowAvgBitRate200JSONResponse(int(*f.AvgBitRate)), nil
}

func (h *Handler) HeadFlowAvgBitRate(ctx context.Context, req api.HeadFlowAvgBitRateRequestObject) (api.HeadFlowAvgBitRateResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.HeadFlowAvgBitRate404ApplicationProblemPlusJSONResponse{}, nil
	}
	f, err := h.flows.GetFlow(ctx, id, false, nil)
	if err != nil {
		if isNotFound(err) {
			return api.HeadFlowAvgBitRate404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("HeadFlowAvgBitRate: GetFlow failed", zap.Error(err))
		return nil, err
	}
	if f.AvgBitRate == nil {
		return api.HeadFlowAvgBitRate404ApplicationProblemPlusJSONResponse{}, nil
	}
	return api.HeadFlowAvgBitRate200Response{}, nil
}

func (h *Handler) PutFlowAvgBitRate(ctx context.Context, req api.PutFlowAvgBitRateRequestObject) (api.PutFlowAvgBitRateResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.PutFlowAvgBitRate404ApplicationProblemPlusJSONResponse{}, nil
	}
	if err := h.flows.PutFlowAvgBitRate(ctx, id, int64(*req.Body)); err != nil {
		if isNotFound(err) {
			return api.PutFlowAvgBitRate404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("PutFlowAvgBitRate failed", zap.Error(err))
		return nil, err
	}
	log.Info("flow avg_bit_rate updated")
	return api.PutFlowAvgBitRate204Response{}, nil
}

func (h *Handler) DeleteFlowAvgBitRate(ctx context.Context, req api.DeleteFlowAvgBitRateRequestObject) (api.DeleteFlowAvgBitRateResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.DeleteFlowAvgBitRate404ApplicationProblemPlusJSONResponse{}, nil
	}
	if err := h.flows.DeleteFlowAvgBitRate(ctx, id); err != nil {
		if isNotFound(err) {
			return api.DeleteFlowAvgBitRate404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("DeleteFlowAvgBitRate failed", zap.Error(err))
		return nil, err
	}
	log.Info("flow avg_bit_rate deleted")
	return api.DeleteFlowAvgBitRate204Response{}, nil
}

// --- MaxBitRate ---

func (h *Handler) GetFlowMaxBitRate(ctx context.Context, req api.GetFlowMaxBitRateRequestObject) (api.GetFlowMaxBitRateResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.GetFlowMaxBitRate404ApplicationProblemPlusJSONResponse{}, nil
	}
	f, err := h.flows.GetFlow(ctx, id, false, nil)
	if err != nil {
		if isNotFound(err) {
			return api.GetFlowMaxBitRate404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("GetFlowMaxBitRate: GetFlow failed", zap.Error(err))
		return nil, err
	}
	if f.MaxBitRate == nil {
		return api.GetFlowMaxBitRate404ApplicationProblemPlusJSONResponse{}, nil
	}
	return api.GetFlowMaxBitRate200JSONResponse(int(*f.MaxBitRate)), nil
}

func (h *Handler) HeadFlowMaxBitRate(ctx context.Context, req api.HeadFlowMaxBitRateRequestObject) (api.HeadFlowMaxBitRateResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.HeadFlowMaxBitRate404ApplicationProblemPlusJSONResponse{}, nil
	}
	f, err := h.flows.GetFlow(ctx, id, false, nil)
	if err != nil {
		if isNotFound(err) {
			return api.HeadFlowMaxBitRate404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("HeadFlowMaxBitRate: GetFlow failed", zap.Error(err))
		return nil, err
	}
	if f.MaxBitRate == nil {
		return api.HeadFlowMaxBitRate404ApplicationProblemPlusJSONResponse{}, nil
	}
	return api.HeadFlowMaxBitRate200Response{}, nil
}

func (h *Handler) PutFlowMaxBitRate(ctx context.Context, req api.PutFlowMaxBitRateRequestObject) (api.PutFlowMaxBitRateResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.PutFlowMaxBitRate404ApplicationProblemPlusJSONResponse{}, nil
	}
	if err := h.flows.PutFlowMaxBitRate(ctx, id, int64(*req.Body)); err != nil {
		if isNotFound(err) {
			return api.PutFlowMaxBitRate404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("PutFlowMaxBitRate failed", zap.Error(err))
		return nil, err
	}
	log.Info("flow max_bit_rate updated")
	return api.PutFlowMaxBitRate204Response{}, nil
}

func (h *Handler) DeleteFlowMaxBitRate(ctx context.Context, req api.DeleteFlowMaxBitRateRequestObject) (api.DeleteFlowMaxBitRateResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.DeleteFlowMaxBitRate404ApplicationProblemPlusJSONResponse{}, nil
	}
	if err := h.flows.DeleteFlowMaxBitRate(ctx, id); err != nil {
		if isNotFound(err) {
			return api.DeleteFlowMaxBitRate404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("DeleteFlowMaxBitRate failed", zap.Error(err))
		return nil, err
	}
	log.Info("flow max_bit_rate deleted")
	return api.DeleteFlowMaxBitRate204Response{}, nil
}

// --- ReadOnly ---

func (h *Handler) GetFlowReadOnly(ctx context.Context, req api.GetFlowReadOnlyRequestObject) (api.GetFlowReadOnlyResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.GetFlowReadOnly404ApplicationProblemPlusJSONResponse{}, nil
	}
	f, err := h.flows.GetFlow(ctx, id, false, nil)
	if err != nil {
		if isNotFound(err) {
			return api.GetFlowReadOnly404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("GetFlowReadOnly: GetFlow failed", zap.Error(err))
		return nil, err
	}
	return api.GetFlowReadOnly200JSONResponse(f.ReadOnly), nil
}

func (h *Handler) HeadFlowReadOnly(ctx context.Context, req api.HeadFlowReadOnlyRequestObject) (api.HeadFlowReadOnlyResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.HeadFlowReadOnly404ApplicationProblemPlusJSONResponse{}, nil
	}
	_, err = h.flows.GetFlow(ctx, id, false, nil)
	if err != nil {
		if isNotFound(err) {
			return api.HeadFlowReadOnly404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("HeadFlowReadOnly: GetFlow failed", zap.Error(err))
		return nil, err
	}
	return api.HeadFlowReadOnly200Response{}, nil
}

func (h *Handler) PutFlowReadOnly(ctx context.Context, req api.PutFlowReadOnlyRequestObject) (api.PutFlowReadOnlyResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	id, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.PutFlowReadOnly404ApplicationProblemPlusJSONResponse{}, nil
	}
	if err := h.flows.PutFlowReadOnly(ctx, id, bool(*req.Body)); err != nil {
		if isNotFound(err) {
			return api.PutFlowReadOnly404ApplicationProblemPlusJSONResponse{}, nil
		}
		log.Error("PutFlowReadOnly failed", zap.Error(err))
		return nil, err
	}
	log.Info("flow read_only updated")
	return api.PutFlowReadOnly204Response{}, nil
}
