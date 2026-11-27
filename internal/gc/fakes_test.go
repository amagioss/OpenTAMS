package gc

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/amagioss/opentams/internal/objectstore"
	"github.com/amagioss/opentams/pkg/metrics"
)

const (
	callClaimReleased      = "ClaimReleasedObjects"
	callReclaimObjects     = "ReclaimRetryObjects"
	callDeleteObjects      = "DeleteClaimedObjects"
	callReleasedBacklog    = "ReleasedBacklog"
	callClaimAllocations   = "ClaimExpiredAllocations"
	callReclaimAllocations = "ReclaimRetryAllocations"
	callDeleteAllocations  = "DeleteClaimedAllocations"
	callAllocationBacklog  = "AllocationBacklog"
)

type storeCall struct {
	name       string
	storageID  string
	ids        []string
	lease      time.Duration
	cycleStart time.Time
	limit      int
}

type fakeStore struct {
	mu    sync.Mutex
	calls []storeCall

	claimReleased      func(ctx context.Context, storageID string, limit int) (ClaimResult, error)
	reclaimObjects     func(ctx context.Context, storageID string, lease time.Duration, cycleStart time.Time, limit int) ([]string, error)
	deleteObjects      func(ctx context.Context, ids []string) ([]string, error)
	releasedBacklog    func(ctx context.Context, storageID string) (Backlog, error)
	claimAllocations   func(ctx context.Context, storageID string, grace time.Duration, limit int) (ClaimResult, error)
	reclaimAllocations func(ctx context.Context, storageID string, lease time.Duration, cycleStart time.Time, limit int) ([]string, error)
	deleteAllocations  func(ctx context.Context, ids []string) ([]string, error)
	allocationBacklog  func(ctx context.Context, storageID string, grace time.Duration) (Backlog, error)
}

func (f *fakeStore) record(c storeCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c.ids = slices.Clone(c.ids)
	f.calls = append(f.calls, c)
}

func (f *fakeStore) callsNamed(name string) []storeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []storeCall
	for _, c := range f.calls {
		if c.name == name {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeStore) indexOf(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.IndexFunc(f.calls, func(c storeCall) bool { return c.name == name })
}

func (f *fakeStore) resetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

func (f *fakeStore) ClaimReleasedObjects(ctx context.Context, storageID string, limit int) (ClaimResult, error) {
	f.record(storeCall{name: callClaimReleased, storageID: storageID, limit: limit})
	if f.claimReleased == nil {
		return ClaimResult{}, nil
	}
	return f.claimReleased(ctx, storageID, limit)
}

func (f *fakeStore) ReclaimRetryObjects(ctx context.Context, storageID string, lease time.Duration, cycleStart time.Time, limit int) ([]string, error) {
	f.record(storeCall{name: callReclaimObjects, storageID: storageID, lease: lease, cycleStart: cycleStart, limit: limit})
	if f.reclaimObjects == nil {
		return nil, nil
	}
	return f.reclaimObjects(ctx, storageID, lease, cycleStart, limit)
}

func (f *fakeStore) DeleteClaimedObjects(ctx context.Context, ids []string) ([]string, error) {
	f.record(storeCall{name: callDeleteObjects, ids: ids})
	if f.deleteObjects == nil {
		return slices.Clone(ids), nil
	}
	return f.deleteObjects(ctx, ids)
}

func (f *fakeStore) ReleasedBacklog(ctx context.Context, storageID string) (Backlog, error) {
	f.record(storeCall{name: callReleasedBacklog, storageID: storageID})
	if f.releasedBacklog == nil {
		return Backlog{}, nil
	}
	return f.releasedBacklog(ctx, storageID)
}

func (f *fakeStore) ClaimExpiredAllocations(ctx context.Context, storageID string, grace time.Duration, limit int) (ClaimResult, error) {
	f.record(storeCall{name: callClaimAllocations, storageID: storageID, limit: limit})
	if f.claimAllocations == nil {
		return ClaimResult{}, nil
	}
	return f.claimAllocations(ctx, storageID, grace, limit)
}

func (f *fakeStore) ReclaimRetryAllocations(ctx context.Context, storageID string, lease time.Duration, cycleStart time.Time, limit int) ([]string, error) {
	f.record(storeCall{name: callReclaimAllocations, storageID: storageID, lease: lease, cycleStart: cycleStart, limit: limit})
	if f.reclaimAllocations == nil {
		return nil, nil
	}
	return f.reclaimAllocations(ctx, storageID, lease, cycleStart, limit)
}

func (f *fakeStore) DeleteClaimedAllocations(ctx context.Context, ids []string) ([]string, error) {
	f.record(storeCall{name: callDeleteAllocations, ids: ids})
	if f.deleteAllocations == nil {
		return slices.Clone(ids), nil
	}
	return f.deleteAllocations(ctx, ids)
}

func (f *fakeStore) AllocationBacklog(ctx context.Context, storageID string, grace time.Duration) (Backlog, error) {
	f.record(storeCall{name: callAllocationBacklog, storageID: storageID})
	if f.allocationBacklog == nil {
		return Backlog{}, nil
	}
	return f.allocationBacklog(ctx, storageID, grace)
}

type fakeDeleter struct {
	mu    sync.Mutex
	calls [][]string

	deleteObjects func(ctx context.Context, ids []string) ([]objectstore.DeleteResult, error)
}

func (f *fakeDeleter) DeleteObjects(ctx context.Context, ids []string) ([]objectstore.DeleteResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, slices.Clone(ids))
	f.mu.Unlock()
	if f.deleteObjects == nil {
		return succeedAll(ids), nil
	}
	return f.deleteObjects(ctx, ids)
}

func (f *fakeDeleter) callIDs() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func succeedAll(ids []string) []objectstore.DeleteResult {
	out := make([]objectstore.DeleteResult, 0, len(ids))
	for _, id := range ids {
		out = append(out, objectstore.DeleteResult{ID: id})
	}
	return out
}

func failIDs(sentinel error, failing ...string) func(context.Context, []string) ([]objectstore.DeleteResult, error) {
	return func(_ context.Context, ids []string) ([]objectstore.DeleteResult, error) {
		out := succeedAll(ids)
		for i := range out {
			if slices.Contains(failing, out[i].ID) {
				out[i].Error = fmt.Errorf("delete %s: %w", out[i].ID, sentinel)
			}
		}
		return out, nil
	}
}

func claimed(ids ...string) func(context.Context, string, int) (ClaimResult, error) {
	return func(context.Context, string, int) (ClaimResult, error) {
		return ClaimResult{Selected: len(ids), Claimed: ids}, nil
	}
}

type fakeClock struct {
	mu    sync.Mutex
	t     time.Time
	queue []time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.queue) > 0 {
		c.t = c.queue[0]
		c.queue = c.queue[1:]
	}
	return c.t
}

func (c *fakeClock) thenReturn(ts ...time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queue = append(c.queue, ts...)
}

var clockStart = time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)

type harness struct {
	store   *fakeStore
	deleter *fakeDeleter
	clock   *fakeClock
	logs    *observer.ObservedLogs
	gather  prometheus.Gatherer
	metrics *Metrics
	logger  *zap.Logger
	w       *Worker

	hookMu sync.Mutex
	onLog  func(zapcore.Entry)
}

func testConfig() Config {
	return Config{
		PollInterval:    time.Hour,
		BatchSize:       1000,
		AllocationGrace: 5 * time.Minute,
		StorageID:       "default",
	}
}

func newHarness(t *testing.T, mutate ...func(*Config)) *harness {
	t.Helper()
	cfg := testConfig()
	for _, m := range mutate {
		m(&cfg)
	}

	reg, err := metrics.New(metrics.WithNamespace("opentams"), metrics.WithoutGoCollector(), metrics.WithoutProcessCollector())
	require.NoError(t, err)
	m, err := NewMetrics(reg.NamespacedRegisterer())
	require.NoError(t, err)

	h := &harness{
		store:   &fakeStore{},
		deleter: &fakeDeleter{},
		clock:   &fakeClock{t: clockStart},
		gather:  reg.Gatherer(),
		metrics: m,
	}
	core, logs := observer.New(zapcore.DebugLevel)
	h.logs = logs
	h.logger = zap.New(zapcore.RegisterHooks(core, func(e zapcore.Entry) error {
		h.hookMu.Lock()
		hook := h.onLog
		h.hookMu.Unlock()
		if hook != nil {
			hook(e)
		}
		return nil
	}))

	w, err := New(h.store, h.deleter, cfg, m, h.logger)
	require.NoError(t, err)
	w.now = h.clock.now
	h.w = w
	return h
}

func (h *harness) setLogHook(fn func(zapcore.Entry)) {
	h.hookMu.Lock()
	defer h.hookMu.Unlock()
	h.onLog = fn
}

// firstCycleCompleted closes when the first "gc cycle completed" line is
// written. A fake that blocks one sweep can wait on it to know that the
// other sweep no longer reads the clock.
func (h *harness) firstCycleCompleted() <-chan struct{} {
	ch := make(chan struct{})
	var once sync.Once
	h.setLogHook(func(e zapcore.Entry) {
		if e.Message == "gc cycle completed" {
			once.Do(func() { close(ch) })
		}
	})
	return ch
}

var errWaitTimedOut = errors.New("test fake: timed out waiting")

func waitInFake(ch <-chan struct{}) error {
	select {
	case <-ch:
		return nil
	case <-time.After(waitLimit):
		return errWaitTimedOut
	}
}

func (h *harness) family(t *testing.T, name string) *dto.MetricFamily {
	t.Helper()
	families, err := h.gather.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() == name {
			return f
		}
	}
	return nil
}

func (h *harness) find(t *testing.T, name string, labelPairs ...string) *dto.Metric {
	t.Helper()
	require.Zero(t, len(labelPairs)%2, "label pairs must be key, value")
	f := h.family(t, name)
	if f == nil {
		return nil
	}
	for _, m := range f.GetMetric() {
		if labelsEqual(m.GetLabel(), labelPairs) {
			return m
		}
	}
	return nil
}

func labelsEqual(got []*dto.LabelPair, want []string) bool {
	if len(got) != len(want)/2 {
		return false
	}
	for i := 0; i < len(want); i += 2 {
		if !slices.ContainsFunc(got, func(l *dto.LabelPair) bool {
			return l.GetName() == want[i] && l.GetValue() == want[i+1]
		}) {
			return false
		}
	}
	return true
}

func (h *harness) value(t *testing.T, name string, labelPairs ...string) float64 {
	t.Helper()
	m := h.find(t, name, labelPairs...)
	if m == nil {
		return 0
	}
	switch {
	case m.Counter != nil:
		return m.GetCounter().GetValue()
	case m.Gauge != nil:
		return m.GetGauge().GetValue()
	}
	t.Fatalf("metric %s is neither counter nor gauge", name)
	return 0
}

func (h *harness) histogramCount(t *testing.T, name string, labelPairs ...string) uint64 {
	t.Helper()
	m := h.find(t, name, labelPairs...)
	if m == nil {
		return 0
	}
	return m.GetHistogram().GetSampleCount()
}

func (h *harness) linesFor(message, sweepName string) []observer.LoggedEntry {
	filtered := h.logs.FilterMessage(message)
	if sweepName == "" {
		return filtered.All()
	}
	return filtered.FilterField(zap.String("sweep", sweepName)).All()
}

func (h *harness) runInBackground(ctx context.Context, stop <-chan struct{}) <-chan error {
	done := make(chan error, 1)
	go func() { done <- h.w.Run(ctx, stop) }()
	return done
}

const waitLimit = 5 * time.Second

func waitFor[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(waitLimit):
		t.Fatalf("timed out waiting for %s", what)
	}
	var zero T
	return zero
}
