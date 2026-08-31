package handlers

import (
	"context"
	"encoding/json"
	"time"

	"go.uber.org/zap"

	"github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/internal/config"
	"github.com/amagioss/opentams/internal/httpx/health"
	"github.com/amagioss/opentams/internal/idempotency"
	"github.com/amagioss/opentams/internal/metastore"
	"github.com/amagioss/opentams/internal/objectstore"
	"github.com/amagioss/opentams/internal/service/flow"
	"github.com/amagioss/opentams/internal/service/segment"
	"github.com/amagioss/opentams/internal/service/storage"
)

type idempotencyStore interface {
	Acquire(ctx context.Context, key, bodyHash string, ttl time.Duration) (idempotency.AcquireResult, error)
	Complete(ctx context.Context, key string, statusCode int, body json.RawMessage) error
	Release(ctx context.Context, key string) error
}

// Handler implements api.StrictServerInterface.
type Handler struct {
	log         *zap.Logger
	cfg         *config.Config
	sources     metastore.SourceStore
	flows       flow.FlowService
	segments    segment.Service
	storage     storage.StorageService
	idempotency idempotencyStore
	health      health.Checker
	objects     objectstore.Store
}

func New(
	log *zap.Logger,
	cfg *config.Config,
	sources metastore.SourceStore,
	flows flow.FlowService,
	segments segment.Service,
	stor storage.StorageService,
	idem idempotencyStore,
	hc health.Checker,
	objects objectstore.Store,
) *Handler {
	if log == nil {
		log = zap.NewNop()
	}
	return &Handler{
		log:         log,
		cfg:         cfg,
		sources:     sources,
		flows:       flows,
		segments:    segments,
		storage:     stor,
		idempotency: idem,
		health:      hc,
		objects:     objects,
	}
}

var _ api.StrictServerInterface = (*Handler)(nil)
