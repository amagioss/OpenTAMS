//go:build integration

package metastore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	gcFlowF = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	gcFlowG = uuid.MustParse("22222222-2222-2222-2222-222222222222")
)

const gcTolerance = 5.0

func gcExec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func gcSeedFlow(t *testing.T, flowID uuid.UUID) {
	t.Helper()
	sourceID := uuid.New()
	gcExec(t, `INSERT INTO sources (id, format) VALUES ($1, 'urn:x-nmos:format:video')`, sourceID)
	gcExec(t, `INSERT INTO flows (id, source_id, format, codec, read_only)
		VALUES ($1, $2, 'urn:x-nmos:format:video', 'video/h264', false)`, flowID, sourceID)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testPool.Exec(ctx, `DELETE FROM flows WHERE id = $1`, flowID)
		_, _ = testPool.Exec(ctx, `DELETE FROM sources WHERE id = $1`, sourceID)
	})
}

// gcSecondsFromNow returns the distance in seconds between the expression
// expr of the row id in table and the database now() plus offset.
func gcSecondsFromNow(t *testing.T, table, expr, id string, offset time.Duration) float64 {
	t.Helper()
	var d float64
	err := testPool.QueryRow(context.Background(),
		`SELECT abs(EXTRACT(EPOCH FROM (`+expr+` - (now() + make_interval(secs => $2))))) FROM `+table+` WHERE id = $1`,
		id, offset.Seconds()).Scan(&d)
	if err != nil {
		t.Fatalf("read %s of %s %q: %v", expr, table, id, err)
	}
	return d
}

func gcCount(t *testing.T, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := testPool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

// TC-GC-001: Mode limit writes one allocation row for each ID.
func TestAllocateObjects_LimitMode_WritesOneRowPerID(t *testing.T) {
	resetGCTables(t)
	gcSeedFlow(t, gcFlowF)
	s := New(testPool)

	err := s.AllocateObjects(context.Background(), AllocateParams{
		FlowID: gcFlowF, StorageID: "default", TTL: time.Hour, IDs: []string{"b-id", "a-id"}, Requested: false,
	})
	if err != nil {
		t.Fatalf("AllocateObjects: %v", err)
	}

	rows, err := testPool.Query(context.Background(),
		`SELECT id, flow_id, storage_id, deleting FROM allocations ORDER BY id COLLATE "C"`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var (
			id, storageID string
			flowID        uuid.UUID
			deleting      bool
		)
		if err := rows.Scan(&id, &flowID, &storageID, &deleting); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if flowID != gcFlowF || storageID != "default" || deleting {
			t.Errorf("row %q: flow_id=%s storage_id=%q deleting=%v", id, flowID, storageID, deleting)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(ids) != 2 || ids[0] != "a-id" || ids[1] != "b-id" {
		t.Fatalf("ids: got %v, want [a-id b-id]", ids)
	}
	for _, id := range ids {
		if d := gcSecondsFromNow(t, "allocations", "expires_at", id, time.Hour); d > gcTolerance {
			t.Errorf("expires_at of %q: %.1f s from now()+1h", id, d)
		}
	}
}

func gcAllocate(flowID uuid.UUID, ids ...string) error {
	return New(testPool).AllocateObjects(context.Background(), AllocateParams{
		FlowID: flowID, StorageID: "default", TTL: time.Hour, IDs: ids, Requested: true,
	})
}

func gcRequireObjectError(t *testing.T, err error, id string, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error: got %v, want %v", err, want)
	}
	var oe *ObjectError
	if !errors.As(err, &oe) || oe.ID != id {
		t.Fatalf("error %v: want *ObjectError with ID %q", err, id)
	}
	if !strings.Contains(err.Error(), id) {
		t.Errorf("error text %q does not contain %q", err.Error(), id)
	}
}

// TC-GC-004: Mode object_ids writes new rows.
func TestAllocateObjects_RequestedNewID_WritesRow(t *testing.T) {
	resetGCTables(t)
	gcSeedFlow(t, gcFlowF)

	if err := gcAllocate(gcFlowF, "x1"); err != nil {
		t.Fatalf("AllocateObjects: %v", err)
	}
	if n := gcCount(t, `SELECT count(*) FROM allocations WHERE id = 'x1' AND flow_id = $1`, gcFlowF); n != 1 {
		t.Fatalf("allocation rows x1 on flow F: got %d, want 1", n)
	}
}

// TC-GC-005: An ID with an object row gives object-exists.
func TestAllocateObjects_RequestedIDWithObjectRow_ReturnsObjectExists(t *testing.T) {
	for _, refCount := range []int{1, 0} {
		t.Run(fmt.Sprintf("ref_count=%d", refCount), func(t *testing.T) {
			resetGCTables(t)
			gcSeedFlow(t, gcFlowF)
			gcExec(t, `INSERT INTO objects (id, ref_count, storage_id, deleting) VALUES ('x1', $1, 'default', false)`, refCount)

			err := gcAllocate(gcFlowF, "x1")

			gcRequireObjectError(t, err, "x1", ErrObjectExists)
			if n := gcCount(t, `SELECT count(*) FROM allocations WHERE id = 'x1'`); n != 0 {
				t.Errorf("allocation rows x1: got %d, want 0", n)
			}
		})
	}
}

// TC-GC-006: A second request on the same flow gives a new expiry.
func TestAllocateObjects_RequestedSameFlowAgain_ExtendsExpiry(t *testing.T) {
	resetGCTables(t)
	gcSeedFlow(t, gcFlowF)
	gcExec(t, `INSERT INTO allocations (id, flow_id, storage_id, expires_at, created_at)
		VALUES ('x1', $1, 'default', now() + interval '10 minutes', now() - interval '1 minute')`, gcFlowF)
	var createdBefore time.Time
	if err := testPool.QueryRow(context.Background(),
		`SELECT created_at FROM allocations WHERE id = 'x1'`).Scan(&createdBefore); err != nil {
		t.Fatalf("read created_at: %v", err)
	}

	if err := gcAllocate(gcFlowF, "x1"); err != nil {
		t.Fatalf("AllocateObjects: %v", err)
	}

	if d := gcSecondsFromNow(t, "allocations", "expires_at", "x1", time.Hour); d > gcTolerance {
		t.Errorf("expires_at: %.1f s from now()+1h", d)
	}
	var (
		flowID       uuid.UUID
		createdAfter time.Time
	)
	if err := testPool.QueryRow(context.Background(),
		`SELECT flow_id, created_at FROM allocations WHERE id = 'x1'`).Scan(&flowID, &createdAfter); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if flowID != gcFlowF {
		t.Errorf("flow_id: got %s, want %s", flowID, gcFlowF)
	}
	if !createdAfter.Equal(createdBefore) {
		t.Errorf("created_at changed: %v -> %v", createdBefore, createdAfter)
	}
}

// TC-GC-007: An allocation for a different flow gives object-wrong-flow.
func TestAllocateObjects_RequestedIDOnOtherFlow_ReturnsWrongFlow(t *testing.T) {
	resetGCTables(t)
	gcSeedFlow(t, gcFlowF)
	gcExec(t, `INSERT INTO allocations (id, flow_id, storage_id, expires_at)
		VALUES ('x1', $1, 'default', now() + interval '1 hour')`, gcFlowG)
	var expiresBefore time.Time
	if err := testPool.QueryRow(context.Background(),
		`SELECT expires_at FROM allocations WHERE id = 'x1'`).Scan(&expiresBefore); err != nil {
		t.Fatalf("read expires_at: %v", err)
	}

	err := gcAllocate(gcFlowF, "x1")

	gcRequireObjectError(t, err, "x1", ErrObjectWrongFlow)
	if strings.Contains(err.Error(), gcFlowG.String()) {
		t.Errorf("error text %q contains flow G", err.Error())
	}
	var (
		flowID       uuid.UUID
		expiresAfter time.Time
	)
	if err := testPool.QueryRow(context.Background(),
		`SELECT flow_id, expires_at FROM allocations WHERE id = 'x1'`).Scan(&flowID, &expiresAfter); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if flowID != gcFlowG || !expiresAfter.Equal(expiresBefore) {
		t.Errorf("row changed: flow_id=%s expires_at=%v, want %s and %v", flowID, expiresAfter, gcFlowG, expiresBefore)
	}
}

// TC-GC-008: An object row with deleting = true gives object-deleting.
func TestAllocateObjects_RequestedIDWithDeletingObject_ReturnsDeleting(t *testing.T) {
	resetGCTables(t)
	gcSeedFlow(t, gcFlowF)
	gcExec(t, `INSERT INTO objects (id, ref_count, storage_id, deleting, claimed_at)
		VALUES ('x1', 0, 'default', true, now())`)

	err := gcAllocate(gcFlowF, "x1")

	gcRequireObjectError(t, err, "x1", ErrObjectDeleting)
}

// TC-GC-009: An allocation row with deleting = true gives object-deleting.
func TestAllocateObjects_RequestedIDWithDeletingAllocation_ReturnsDeleting(t *testing.T) {
	resetGCTables(t)
	gcSeedFlow(t, gcFlowF)
	gcExec(t, `INSERT INTO allocations (id, flow_id, storage_id, expires_at, deleting, claimed_at)
		VALUES ('x1', $1, 'default', now() + interval '1 hour', true, now())`, gcFlowF)

	err := gcAllocate(gcFlowF, "x1")

	gcRequireObjectError(t, err, "x1", ErrObjectDeleting)
	if n := gcCount(t, `SELECT count(*) FROM allocations WHERE id = 'x1' AND deleting = true`); n != 1 {
		t.Errorf("allocation x1 with deleting = true: got %d rows, want 1", n)
	}
}

// TC-GC-010: One rejected ID rejects the full request.
func TestAllocateObjects_OneRejectedID_RejectsFullRequest(t *testing.T) {
	resetGCTables(t)
	gcSeedFlow(t, gcFlowF)
	gcExec(t, `INSERT INTO objects (id, ref_count, storage_id, deleting) VALUES ('b1', 1, 'default', false)`)

	err := gcAllocate(gcFlowF, "a1", "b1")

	gcRequireObjectError(t, err, "b1", ErrObjectExists)
	if n := gcCount(t, `SELECT count(*) FROM allocations WHERE id = 'a1'`); n != 0 {
		t.Errorf("allocation rows a1: got %d, want 0", n)
	}
}

// TC-GC-013: Two requests at the same time for the same ID on two flows.
func TestAllocateObjects_ConcurrentSameIDTwoFlows_OneWinsWithoutDeadlock(t *testing.T) {
	resetGCTables(t)
	gcSeedFlow(t, gcFlowF)
	gcSeedFlow(t, gcFlowG)

	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, flowID := range []uuid.UUID{gcFlowF, gcFlowG} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = gcAllocate(flowID, "x1")
		}()
	}
	close(start)
	wg.Wait()

	var ok, wrongFlow int
	for _, err := range errs {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "40P01" {
			t.Fatalf("deadlock: %v", err)
		}
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrObjectWrongFlow):
			wrongFlow++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 || wrongFlow != 1 {
		t.Errorf("results: %d nil and %d wrong-flow, want 1 and 1 (errors %v)", ok, wrongFlow, errs)
	}
	if n := gcCount(t, `SELECT count(*) FROM allocations WHERE id = 'x1'`); n != 1 {
		t.Errorf("allocation rows x1: got %d, want 1", n)
	}
}
