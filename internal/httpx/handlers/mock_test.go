package handlers_test

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/amagioss/opentams/internal/httpx/health"
	"github.com/amagioss/opentams/internal/idempotency"
	"github.com/amagioss/opentams/internal/metastore"
	"github.com/amagioss/opentams/internal/objectstore"
	"github.com/amagioss/opentams/internal/timerange"
)

// fakeObjectStore is a hand-rolled stand-in for objectstore.Store, scoped
// to the URL-projection scenarios. Test cases set scripted returns per
// objectID via `download` and assert call counts via the atomic counter.
type fakeObjectStore struct {
	mu                  sync.Mutex
	download            map[string]objectstore.DownloadURLSet
	downloadErr         map[string]error
	generateDownloadCnt atomic.Int32
	signCallCnt         atomic.Int32 // increments only when a signed URL is computed
	lastOpts            map[string]objectstore.DownloadURLOpts
}

func newFakeObjectStore() *fakeObjectStore {
	return &fakeObjectStore{
		download:    map[string]objectstore.DownloadURLSet{},
		downloadErr: map[string]error{},
		lastOpts:    map[string]objectstore.DownloadURLOpts{},
	}
}

func (f *fakeObjectStore) GenerateUploadURL(_ context.Context, _, _ string) (string, error) {
	return "", nil
}

func (f *fakeObjectStore) GenerateDownloadURL(_ context.Context, objectID string, opts objectstore.DownloadURLOpts) (objectstore.DownloadURLSet, error) {
	f.generateDownloadCnt.Add(1)
	f.mu.Lock()
	f.lastOpts[objectID] = opts
	f.mu.Unlock()

	if err, ok := f.downloadErr[objectID]; ok && err != nil {
		return objectstore.DownloadURLSet{}, err
	}
	set := f.download[objectID]
	skipSign := opts.Presigned != nil && !*opts.Presigned
	if !skipSign && set.PresignedURL != "" {
		f.signCallCnt.Add(1)
	}
	if skipSign {
		// Mirror real S3Store: short-circuit clears PresignedURL.
		set.PresignedURL = ""
	}
	return set, nil
}

func (f *fakeObjectStore) DeleteObjects(_ context.Context, ids []string) ([]objectstore.DeleteResult, error) {
	out := make([]objectstore.DeleteResult, len(ids))
	for i, id := range ids {
		out[i] = objectstore.DeleteResult{ID: id}
	}
	return out, nil
}

func (f *fakeObjectStore) HealthCheck(_ context.Context) error { return nil }

// mockChecker — hand-rolled fake for health.Checker, function-field style
// consistent with the other mocks in this file.
type mockChecker struct {
	ready   func(ctx context.Context) bool
	details func(ctx context.Context) health.Details
}

func (m *mockChecker) Ready(ctx context.Context) bool {
	if m.ready == nil {
		return true
	}
	return m.ready(ctx)
}

func (m *mockChecker) Details(ctx context.Context) health.Details {
	if m.details == nil {
		return health.Details{}
	}
	return m.details(ctx)
}

// mockSourceStore

type mockSourceStore struct {
	getSource               func(ctx context.Context, id uuid.UUID) (*metastore.Source, error)
	listSources             func(ctx context.Context, p metastore.ListSourcesParams) (*metastore.SourcePage, error)
	putSourceTag            func(ctx context.Context, id uuid.UUID, name string, value json.RawMessage) error
	deleteSourceTag         func(ctx context.Context, id uuid.UUID, name string) error
	putSourceLabel          func(ctx context.Context, id uuid.UUID, label string) error
	deleteSourceLabel       func(ctx context.Context, id uuid.UUID) error
	putSourceDescription    func(ctx context.Context, id uuid.UUID, desc string) error
	deleteSourceDescription func(ctx context.Context, id uuid.UUID) error
}

func (m *mockSourceStore) GetSource(ctx context.Context, id uuid.UUID) (*metastore.Source, error) {
	return m.getSource(ctx, id)
}
func (m *mockSourceStore) ListSources(ctx context.Context, p metastore.ListSourcesParams) (*metastore.SourcePage, error) {
	return m.listSources(ctx, p)
}
func (m *mockSourceStore) PutSourceTag(ctx context.Context, id uuid.UUID, name string, value json.RawMessage) error {
	return m.putSourceTag(ctx, id, name, value)
}
func (m *mockSourceStore) DeleteSourceTag(ctx context.Context, id uuid.UUID, name string) error {
	return m.deleteSourceTag(ctx, id, name)
}
func (m *mockSourceStore) PutSourceLabel(ctx context.Context, id uuid.UUID, label string) error {
	return m.putSourceLabel(ctx, id, label)
}
func (m *mockSourceStore) DeleteSourceLabel(ctx context.Context, id uuid.UUID) error {
	return m.deleteSourceLabel(ctx, id)
}
func (m *mockSourceStore) PutSourceDescription(ctx context.Context, id uuid.UUID, desc string) error {
	return m.putSourceDescription(ctx, id, desc)
}
func (m *mockSourceStore) DeleteSourceDescription(ctx context.Context, id uuid.UUID) error {
	return m.deleteSourceDescription(ctx, id)
}

// mockFlowService

type mockFlowService struct {
	upsertFlow           func(ctx context.Context, f *metastore.Flow) (bool, error)
	getFlow              func(ctx context.Context, id uuid.UUID, includeTimerange bool, trFilter *timerange.TimeRange) (*metastore.Flow, error)
	listFlows            func(ctx context.Context, p metastore.ListFlowsParams) (*metastore.FlowPage, error)
	deleteFlow           func(ctx context.Context, id uuid.UUID) error
	putFlowTag           func(ctx context.Context, id uuid.UUID, name string, value json.RawMessage) error
	deleteFlowTag        func(ctx context.Context, id uuid.UUID, name string) error
	putFlowLabel         func(ctx context.Context, id uuid.UUID, label string) error
	deleteFlowLabel      func(ctx context.Context, id uuid.UUID) error
	putFlowDesc          func(ctx context.Context, id uuid.UUID, desc string) error
	deleteFlowDesc       func(ctx context.Context, id uuid.UUID) error
	putFlowReadOnly      func(ctx context.Context, id uuid.UUID, readOnly bool) error
	putFlowCollection    func(ctx context.Context, id uuid.UUID, items []metastore.CollectionItem) error
	deleteFlowCollection func(ctx context.Context, id uuid.UUID) error
	putFlowAvgBitRate    func(ctx context.Context, id uuid.UUID, rate int64) error
	deleteFlowAvgBitRate func(ctx context.Context, id uuid.UUID) error
	putFlowMaxBitRate    func(ctx context.Context, id uuid.UUID, rate int64) error
	deleteFlowMaxBitRate func(ctx context.Context, id uuid.UUID) error
}

func (m *mockFlowService) UpsertFlow(ctx context.Context, f *metastore.Flow) (bool, error) {
	return m.upsertFlow(ctx, f)
}
func (m *mockFlowService) GetFlow(ctx context.Context, id uuid.UUID, includeTimerange bool, trFilter *timerange.TimeRange) (*metastore.Flow, error) {
	return m.getFlow(ctx, id, includeTimerange, trFilter)
}
func (m *mockFlowService) ListFlows(ctx context.Context, p metastore.ListFlowsParams) (*metastore.FlowPage, error) {
	return m.listFlows(ctx, p)
}
func (m *mockFlowService) DeleteFlow(ctx context.Context, id uuid.UUID) error {
	return m.deleteFlow(ctx, id)
}
func (m *mockFlowService) PutFlowTag(ctx context.Context, id uuid.UUID, name string, value json.RawMessage) error {
	return m.putFlowTag(ctx, id, name, value)
}
func (m *mockFlowService) DeleteFlowTag(ctx context.Context, id uuid.UUID, name string) error {
	return m.deleteFlowTag(ctx, id, name)
}
func (m *mockFlowService) PutFlowLabel(ctx context.Context, id uuid.UUID, label string) error {
	return m.putFlowLabel(ctx, id, label)
}
func (m *mockFlowService) DeleteFlowLabel(ctx context.Context, id uuid.UUID) error {
	return m.deleteFlowLabel(ctx, id)
}
func (m *mockFlowService) PutFlowDescription(ctx context.Context, id uuid.UUID, desc string) error {
	return m.putFlowDesc(ctx, id, desc)
}
func (m *mockFlowService) DeleteFlowDescription(ctx context.Context, id uuid.UUID) error {
	return m.deleteFlowDesc(ctx, id)
}
func (m *mockFlowService) PutFlowReadOnly(ctx context.Context, id uuid.UUID, readOnly bool) error {
	return m.putFlowReadOnly(ctx, id, readOnly)
}
func (m *mockFlowService) PutFlowCollection(ctx context.Context, id uuid.UUID, items []metastore.CollectionItem) error {
	return m.putFlowCollection(ctx, id, items)
}
func (m *mockFlowService) DeleteFlowCollection(ctx context.Context, id uuid.UUID) error {
	return m.deleteFlowCollection(ctx, id)
}
func (m *mockFlowService) PutFlowAvgBitRate(ctx context.Context, id uuid.UUID, rate int64) error {
	return m.putFlowAvgBitRate(ctx, id, rate)
}
func (m *mockFlowService) DeleteFlowAvgBitRate(ctx context.Context, id uuid.UUID) error {
	return m.deleteFlowAvgBitRate(ctx, id)
}
func (m *mockFlowService) PutFlowMaxBitRate(ctx context.Context, id uuid.UUID, rate int64) error {
	return m.putFlowMaxBitRate(ctx, id, rate)
}
func (m *mockFlowService) DeleteFlowMaxBitRate(ctx context.Context, id uuid.UUID) error {
	return m.deleteFlowMaxBitRate(ctx, id)
}

// mockIdempotencyStore

type mockIdempotencyStore struct {
	acquire  func(ctx context.Context, key, bodyHash string, ttl time.Duration) (idempotency.AcquireResult, error)
	complete func(ctx context.Context, key string, statusCode int, body json.RawMessage) error
	release  func(ctx context.Context, key string) error
}

func (m *mockIdempotencyStore) Acquire(ctx context.Context, key, bodyHash string, ttl time.Duration) (idempotency.AcquireResult, error) {
	return m.acquire(ctx, key, bodyHash, ttl)
}
func (m *mockIdempotencyStore) Complete(ctx context.Context, key string, statusCode int, body json.RawMessage) error {
	if m.complete == nil {
		return nil
	}
	return m.complete(ctx, key, statusCode, body)
}
func (m *mockIdempotencyStore) Release(ctx context.Context, key string) error {
	if m.release == nil {
		return nil
	}
	return m.release(ctx, key)
}
