package storage

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/metastore"
	"github.com/amagioss/opentams/internal/service"
)

// FlowReader is the narrow subset of metastore behaviour the storage
// service depends on. It is satisfied by *metastore.PostgresStore and
// any test fake. Defined locally so the service does not pull in the
// full metastore.Store interface.
type FlowReader interface {
	GetFlow(ctx context.Context, id uuid.UUID) (*metastore.Flow, error)
	IsObjectRegistered(ctx context.Context, objectID string) (bool, error)
}

// ObjectStore is the narrow subset of objectstore behaviour the
// storage service depends on (presigned-PUT URL generation only). It
// is satisfied by *objectstore.S3Store.
type ObjectStore interface {
	GenerateUploadURL(ctx context.Context, objectID, contentType string) (string, error)
}

// AllocateRequest is the storage-allocation request body. Limit is
// the number of new object IDs the server should mint
// (server-generated mode); ObjectIDs is the explicit list the client
// wants presigned URLs for (caller-generated mode). Exactly one of
// the two is set per the OpenAPI spec.
type AllocateRequest struct {
	Limit     *int
	ObjectIDs []string
}

// MediaObject is the storage-allocation response item — a single
// object ID with the presigned PUT URL the client should upload to
// and the Content-Type that URL was signed for.
type MediaObject struct {
	ObjectID    string
	PutURL      string
	ContentType string
}

// StorageService is the public surface of the storage service. It
// owns the AllocateStorage operation that backs
// `POST /flows/{id}/storage`.
type StorageService interface {
	AllocateStorage(ctx context.Context, flowID uuid.UUID, req AllocateRequest) ([]MediaObject, error)
}

type storageService struct {
	store FlowReader
	obj   ObjectStore
	dur   *prometheus.HistogramVec
	errs  *prometheus.CounterVec
}

// New constructs a StorageService backed by the supplied metastore
// reader and object store, instrumented with the shared
// AppServiceMetrics. The returned implementation is safe for
// concurrent use.
func New(store FlowReader, obj ObjectStore, m *service.AppServiceMetrics) StorageService {
	return &storageService{store: store, obj: obj, dur: m.Duration, errs: m.Errors}
}

func (s *storageService) observe(op string, d time.Duration, err error) {
	s.dur.WithLabelValues("storage", op).Observe(d.Seconds())
	if err != nil {
		code := "internal"
		var ae *apperror.AppError
		if errors.As(err, &ae) {
			code = string(ae.Code)
		}
		s.errs.WithLabelValues("storage", op, code).Inc()
	}
}

func (s *storageService) AllocateStorage(ctx context.Context, flowID uuid.UUID, req AllocateRequest) ([]MediaObject, error) {
	start := time.Now()

	flow, err := s.store.GetFlow(ctx, flowID)
	if err != nil {
		s.observe("allocate", time.Since(start), err)
		return nil, err
	}

	if flow.ReadOnly {
		err := apperror.New(apperror.ErrReadOnly, "flow is read-only")
		s.observe("allocate", time.Since(start), err)
		return nil, err
	}

	contentType := "application/octet-stream"
	if flow.Codec != nil {
		contentType = *flow.Codec
	}

	if req.Limit != nil {
		result := make([]MediaObject, 0, *req.Limit)
		for range *req.Limit {
			objectID := uuid.New().String()
			putURL, err := s.obj.GenerateUploadURL(ctx, objectID, contentType)
			if err != nil {
				s.observe("allocate", time.Since(start), err)
				return nil, err
			}
			result = append(result, MediaObject{ObjectID: objectID, PutURL: putURL, ContentType: contentType})
		}
		s.observe("allocate", time.Since(start), nil)
		return result, nil
	}

	result := make([]MediaObject, 0, len(req.ObjectIDs))
	for _, objectID := range req.ObjectIDs {
		registered, err := s.store.IsObjectRegistered(ctx, objectID)
		if err != nil {
			s.observe("allocate", time.Since(start), err)
			return nil, err
		}
		if registered {
			err := apperror.New(apperror.ErrObjectIDExists, "object_id already registered as a segment: "+objectID)
			s.observe("allocate", time.Since(start), err)
			return nil, err
		}
		putURL, err := s.obj.GenerateUploadURL(ctx, objectID, contentType)
		if err != nil {
			s.observe("allocate", time.Since(start), err)
			return nil, err
		}
		result = append(result, MediaObject{ObjectID: objectID, PutURL: putURL, ContentType: contentType})
	}
	s.observe("allocate", time.Since(start), nil)
	return result, nil
}
