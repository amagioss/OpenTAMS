package gc

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/amagioss/opentams/internal/objectstore"
)

// TC-GC-115: New validates its configuration.
func TestNew_RejectsConfigurationOutsideTheValidRanges(t *testing.T) {
	m, err := NewMetrics(prometheus.NewRegistry())
	require.NoError(t, err)

	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"batch size 0", func(c *Config) { c.BatchSize = 0 }, true},
		{"batch size 1001", func(c *Config) { c.BatchSize = 1001 }, true},
		{"poll interval 0", func(c *Config) { c.PollInterval = 0 }, true},
		{"negative poll interval", func(c *Config) { c.PollInterval = -time.Second }, true},
		{"allocation grace -1s", func(c *Config) { c.AllocationGrace = -time.Second }, true},
		{"empty storage ID", func(c *Config) { c.StorageID = "" }, true},
		{"batch size 1", func(c *Config) { c.BatchSize = 1 }, false},
		{"batch size 1000", func(c *Config) { c.BatchSize = 1000 }, false},
		{"allocation grace 0", func(c *Config) { c.AllocationGrace = 0 }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig()
			tc.mutate(&cfg)
			w, err := New(&fakeStore{}, &fakeDeleter{}, cfg, m, zap.NewNop())
			if tc.wantErr {
				assert.Error(t, err)
				assert.Nil(t, w)
				return
			}
			assert.NoError(t, err)
			assert.NotNil(t, w)
		})
	}
}

func TestNew_SetsTheTimeoutsAndTheLeaseFromTheConstants(t *testing.T) {
	m, err := NewMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	w, err := New(&fakeStore{}, &fakeDeleter{}, testConfig(), m, zap.NewNop())
	require.NoError(t, err)

	assert.Equal(t, 30*time.Second, w.dbTimeout)
	assert.Equal(t, 60*time.Second, w.s3Timeout)
	assert.Equal(t, 5*time.Minute, w.claimLease)
	assert.WithinDuration(t, time.Now(), w.now(), time.Minute)
}

func closeOnce(ch chan struct{}) func() {
	var once sync.Once
	return func() { once.Do(func() { close(ch) }) }
}

// TC-GC-110: The two sweeps are independent.
func TestRun_ABlockedSweepDoesNotDelayTheOtherSweep(t *testing.T) {
	h := newHarness(t)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	h.store.claimReleased = func(context.Context, string, int) (ClaimResult, error) {
		entered <- struct{}{}
		<-release
		return ClaimResult{}, nil
	}
	h.store.allocationBacklog = func(context.Context, string, time.Duration) (Backlog, error) {
		return Backlog{Pending: 9}, nil
	}
	completed := make(chan struct{}, 1)
	h.setLogHook(func(e zapcore.Entry) {
		if e.Message == "gc cycle completed" {
			select {
			case completed <- struct{}{}:
			default:
			}
		}
	})
	stop := make(chan struct{})

	done := h.runInBackground(context.Background(), stop)
	waitFor(t, entered, "the sweep 1 claim")
	waitFor(t, completed, "the sweep 2 cycle")

	assert.Equal(t, 9.0, h.value(t, "opentams_gc_backlog", "sweep", SweepAllocation, "state", "pending"))
	assert.Len(t, h.linesFor("gc cycle completed", SweepAllocation), 1)
	assert.Empty(t, h.linesFor("gc cycle completed", SweepReleased))

	close(stop)
	close(release)
	assert.NoError(t, waitFor(t, done, "Run to return"))
}

// TC-GC-112: Shutdown lets the current batch complete.
func TestRun_ShutdownLetsTheCurrentBatchCompleteAndClaimsNoMore(t *testing.T) {
	h := newHarness(t)
	h.store.claimReleased = func(context.Context, string, int) (ClaimResult, error) {
		return ClaimResult{Selected: 1000, Claimed: []string{"a"}}, nil
	}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	h.deleter.deleteObjects = func(_ context.Context, ids []string) ([]objectstore.DeleteResult, error) {
		entered <- struct{}{}
		<-release
		return succeedAll(ids), nil
	}
	stop := make(chan struct{})

	done := h.runInBackground(context.Background(), stop)
	waitFor(t, entered, "the deleter call")
	close(stop)
	close(release)

	assert.NoError(t, waitFor(t, done, "Run to return"))
	rowDeletes := h.store.callsNamed(callDeleteObjects)
	require.Len(t, rowDeletes, 1)
	assert.Equal(t, []string{"a"}, rowDeletes[0].ids)
	assert.Len(t, h.store.callsNamed(callClaimReleased), 1)
}

// TC-GC-113: The hard limit cancels the batch.
func TestRun_TheHardLimitCancelsTheCurrentBatch(t *testing.T) {
	h := newHarness(t)
	h.store.claimReleased = claimed("a")
	entered := make(chan struct{}, 1)
	h.deleter.deleteObjects = func(ctx context.Context, _ []string) ([]objectstore.DeleteResult, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := make(chan struct{})

	done := h.runInBackground(ctx, stop)
	waitFor(t, entered, "the deleter call")
	close(stop)
	cancel()

	assert.NoError(t, waitFor(t, done, "Run to return"))
	assert.Empty(t, h.store.callsNamed(callDeleteObjects))
	assert.Equal(t, "shutdown", endedBy(t, h, SweepReleased))
}

func TestRun_AClosedStopStartsNoCycle(t *testing.T) {
	h := newHarness(t)
	stop := make(chan struct{})
	close(stop)

	assert.NoError(t, waitFor(t, h.runInBackground(context.Background(), stop), "Run to return"))
	assert.Empty(t, h.store.callsNamed(callReclaimObjects))
	assert.Empty(t, h.store.callsNamed(callReclaimAllocations))
}

func TestRun_TheFirstCycleStartsImmediately(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.PollInterval = 24 * time.Hour })
	started := make(chan struct{})
	h.store.reclaimObjects = func(context.Context, string, time.Duration, time.Time, int) ([]string, error) {
		close(started)
		return nil, nil
	}
	stop := make(chan struct{})

	done := h.runInBackground(context.Background(), stop)
	waitFor(t, started, "the first cycle")
	close(stop)

	assert.NoError(t, waitFor(t, done, "Run to return"))
}

func TestRun_TheLoopStartsTheNextCycleAfterThePollInterval(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.PollInterval = time.Millisecond })
	stop := make(chan struct{})
	closeStop := closeOnce(stop)
	var mu sync.Mutex
	cycles := 0
	h.store.reclaimObjects = func(context.Context, string, time.Duration, time.Time, int) ([]string, error) {
		mu.Lock()
		defer mu.Unlock()
		cycles++
		if cycles == 2 {
			closeStop()
		}
		return nil, nil
	}

	assert.NoError(t, waitFor(t, h.runInBackground(context.Background(), stop), "Run to return"))
	assert.Len(t, h.store.callsNamed(callReclaimObjects), 2)
}

func TestRun_ABatchErrorDoesNotStopTheLoop(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.PollInterval = time.Millisecond })
	stop := make(chan struct{})
	closeStop := closeOnce(stop)
	var mu sync.Mutex
	claims := 0
	h.store.claimReleased = func(context.Context, string, int) (ClaimResult, error) {
		mu.Lock()
		defer mu.Unlock()
		claims++
		if claims == 2 {
			closeStop()
			return ClaimResult{}, nil
		}
		return ClaimResult{}, errors.New("database down")
	}

	assert.NoError(t, waitFor(t, h.runInBackground(context.Background(), stop), "Run to return"))
	assert.Len(t, h.store.callsNamed(callClaimReleased), 2)
}

func endedBy(t *testing.T, h *harness, sweepName string) any {
	t.Helper()
	lines := h.linesFor("gc cycle completed", sweepName)
	require.Len(t, lines, 1)
	return lines[0].ContextMap()["ended_by"]
}

// TC-GC-163: The cycle summary.
func TestCycle_TheSummaryLineNamesWhyTheCycleEnded(t *testing.T) {
	t.Run("batch error updates the gauges", func(t *testing.T) {
		h := newHarness(t)
		h.store.claimReleased = func(context.Context, string, int) (ClaimResult, error) {
			return ClaimResult{}, errors.New("database down")
		}
		h.store.releasedBacklog = func(context.Context, string) (Backlog, error) {
			return Backlog{Pending: 3}, nil
		}

		runOnce(t, h)

		assert.Equal(t, "batch_error", endedBy(t, h, SweepReleased))
		assert.Equal(t, 3.0, h.value(t, "opentams_gc_backlog", "sweep", SweepReleased, "state", "pending"))
	})

	t.Run("normal cycle", func(t *testing.T) {
		h := newHarness(t)
		h.store.claimReleased = func(context.Context, string, int) (ClaimResult, error) {
			return ClaimResult{
				Selected: 4,
				Claimed:  []string{"a", "b"},
				RowOnly:  []string{"c"},
				Drift:    []DriftRecord{{ObjectID: "d", Actual: 1}},
			}, nil
		}
		h.deleter.deleteObjects = failIDs(objectstore.ErrTransient, "b")

		runOnce(t, h)

		lines := h.linesFor("gc cycle completed", SweepReleased)
		require.Len(t, lines, 1)
		fields := lines[0].ContextMap()
		assert.Equal(t, "empty", fields["ended_by"])
		assert.EqualValues(t, 1, fields["deleted"])
		assert.EqualValues(t, 1, fields["row_only"])
		assert.EqualValues(t, 1, fields["failed"])
		assert.EqualValues(t, 1, fields["drift"])
		assert.Contains(t, fields, "duration_ms")
		assert.Equal(t, "empty", endedBy(t, h, SweepAllocation))
	})

	t.Run("duration comes from the worker clock", func(t *testing.T) {
		h := newHarness(t)
		h.store.releasedBacklog = func(context.Context, string) (Backlog, error) {
			h.clock.thenReturn(clockStart.Add(1500 * time.Millisecond))
			return Backlog{}, nil
		}

		runOnce(t, h)

		lines := h.linesFor("gc cycle completed", SweepReleased)
		require.Len(t, lines, 1)
		assert.EqualValues(t, 1500, lines[0].ContextMap()["duration_ms"])
	})

	t.Run("shutdown", func(t *testing.T) {
		h := newHarness(t)
		stop := make(chan struct{})
		closeStop := closeOnce(stop)
		h.store.claimReleased = func(context.Context, string, int) (ClaimResult, error) {
			closeStop()
			return ClaimResult{Selected: 1000}, nil
		}

		assert.NoError(t, waitFor(t, h.runInBackground(context.Background(), stop), "Run to return"))

		assert.Equal(t, "shutdown", endedBy(t, h, SweepReleased))
		assert.Len(t, h.store.callsNamed(callClaimReleased), 1)
		assert.Len(t, h.store.callsNamed(callReleasedBacklog), 1)
	})

	t.Run("cancelled context during a failing batch", func(t *testing.T) {
		h := newHarness(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		h.store.claimReleased = func(ctx context.Context, _ string, _ int) (ClaimResult, error) {
			cancel()
			<-ctx.Done()
			return ClaimResult{}, ctx.Err()
		}

		report, err := h.w.RunOnce(ctx, nil)
		require.NoError(t, err)

		assert.Equal(t, "shutdown", endedBy(t, h, SweepReleased))
		assert.False(t, report.Released.BatchError)
		assert.Empty(t, h.store.callsNamed(callReleasedBacklog))
	})

	t.Run("stop between two reclaims", func(t *testing.T) {
		h := newHarness(t, func(c *Config) { c.BatchSize = 1 })
		stop := make(chan struct{})
		closeStop := closeOnce(stop)
		h.store.reclaimObjects = func(context.Context, string, time.Duration, time.Time, int) ([]string, error) {
			closeStop()
			return []string{"a"}, nil
		}

		assert.NoError(t, waitFor(t, h.runInBackground(context.Background(), stop), "Run to return"))

		assert.Len(t, h.store.callsNamed(callReclaimObjects), 1)
		assert.Empty(t, h.store.callsNamed(callClaimReleased))
		assert.Equal(t, [][]string{{"a"}}, h.deleter.callIDs())
		assert.Equal(t, "shutdown", endedBy(t, h, SweepReleased))
	})
}

// TC-GC-194: RunOnce stops claiming when stop closes.
func TestRunOnce_StopDuringTheFirstBatchPreventsASecondClaim(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.BatchSize = 1 })
	stop := make(chan struct{})
	closeStop := closeOnce(stop)
	var claims atomic.Int32
	h.store.claimReleased = func(context.Context, string, int) (ClaimResult, error) {
		if claims.Add(1) > 1 {
			return ClaimResult{}, nil
		}
		return ClaimResult{Selected: 1, Claimed: []string{"a"}}, nil
	}
	h.deleter.deleteObjects = func(_ context.Context, ids []string) ([]objectstore.DeleteResult, error) {
		closeStop()
		return succeedAll(ids), nil
	}

	report, err := h.w.RunOnce(context.Background(), stop)
	require.NoError(t, err)

	assert.Len(t, h.store.callsNamed(callClaimReleased), 1)
	assert.Equal(t, "shutdown", endedBy(t, h, SweepReleased))
	assert.Equal(t, 1, report.Released.Deleted)
	assert.False(t, report.Released.BatchError)
}
