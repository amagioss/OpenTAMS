package main

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/amagioss/opentams/tools/scaletest/dataset"
)

// LoadOptions controls the bulk-load behaviour. Zero values are
// production-safe defaults.
type LoadOptions struct {
	// ChunkSize is the maximum rows per CopyFrom call. <= 0 → 1_000_000.
	// Smaller chunks bound WAL growth per transaction (important for
	// Multi-AZ commit latency and disk headroom on 500M runs) and give
	// progress visibility; per-chunk protocol overhead is negligible
	// against the cost of the data itself.
	ChunkSize int64

	// ProgressFn, if non-nil, is called after each chunk commits with
	// the table label, rows loaded so far, and the planned total. nil
	// → silent.
	//
	// Concurrency: when Workers > 1, the parallel segments COPY invokes
	// ProgressFn from multiple goroutines (one per worker, after each
	// chunk commit). Implementations must be safe to call concurrently;
	// the `done` value is the monotonically-increasing global total
	// across all workers (not a per-worker count) but successive calls
	// may not be strictly ordered by value. The sequential path
	// (Workers <= 1) calls ProgressFn only from the caller's goroutine.
	ProgressFn func(table string, done, total int64)

	// Workers parallelises the segments COPY across N goroutines,
	// each holding its own connection and writing a disjoint
	// flow_id-partitioned slice. <= 1 → sequential (default). The
	// pgxpool's MaxConns must be >= Workers + 1 to avoid starvation.
	// Only `segments` is parallelised; the other tables are small
	// enough that the goroutine plumbing would cost more than it saves.
	Workers int64
}

// defaultChunkSize is what bounds WAL accumulation per transaction. At
// 1M segments/chunk for the 500M run, the loader produces ~500 chunked
// transactions; each commits quickly and Multi-AZ replication keeps pace.
const defaultChunkSize int64 = 1_000_000

// Load runs the full bulk-load flow against an already-migrated database:
//
//  1. DROP everything on `segments` that costs per-row work during the
//     COPY: the GiST EXCLUDE (overlap probe), both FKs (lookup into
//     flows / objects), and the local btree index.
//  2. COPY rows in FK order: sources → flows → flow_collection → objects → segments.
//     Each table loads via repeated chunked CopyFrom calls; one
//     connection is held per table across all its chunks.
//  3. Rebuild the dropped objects:
//     - the btree (single-pass build, much cheaper than incremental);
//     - the GiST EXCLUDE (ADD CONSTRAINT validates non-overlap — a
//     generator bug surfaces as a constraint violation here);
//     - both FKs as NOT VALID + VALIDATE CONSTRAINT, so FK integrity is
//     checked via the parent-table PK index in one sweep rather than
//     per-row during the COPY.
//  4. ANALYZE so planner statistics reflect the new volumes.
//
// The schema after Load is byte-identical to a fresh `migrate up`: same
// constraint names, same definitions, same indexes. This is a one-shot
// bulk-load pattern, not a runtime behaviour — production / serve never
// drops constraints.
//
// Cancellation honours ctx: an interrupted run leaves the chunks already
// committed in place, so a resume can skip them by re-creating the
// generator with the same seed and a higher start offset (not yet
// implemented; tracked as a follow-up).
func Load(ctx context.Context, pool *pgxpool.Pool, plan dataset.Plan, seed uint64, opts LoadOptions) error {
	if opts.ChunkSize <= 0 {
		opts.ChunkSize = defaultChunkSize
	}
	if opts.ProgressFn == nil {
		opts.ProgressFn = func(string, int64, int64) {}
	}

	g := dataset.NewGen(plan, seed)

	if err := dropSegmentsConstraintsAndIndex(ctx, pool); err != nil {
		return err
	}

	if err := copySources(ctx, pool, g, opts); err != nil {
		return err
	}
	if err := copyFlows(ctx, pool, g, opts); err != nil {
		return err
	}
	if err := copyFlowCollection(ctx, pool, g, opts); err != nil {
		return err
	}
	if err := copyObjects(ctx, pool, g, opts); err != nil {
		return err
	}
	if err := copySegmentsParallel(ctx, pool, g, opts); err != nil {
		return err
	}

	if err := rebuildSegmentsConstraintsAndIndex(ctx, pool); err != nil {
		return err
	}

	if _, err := pool.Exec(ctx, `ANALYZE`); err != nil {
		return fmt.Errorf("analyze: %w", err)
	}
	return nil
}

// dropSegmentsConstraintsAndIndex strips per-row work from the COPY hot
// path: GiST EXCLUDE (overlap probe), FKs (parent-table lookups), and
// the local btree (page updates). The COPY then streams into a bare heap.
// Order doesn't matter for the drops — all four are independent.
// Reset truncates every table the loader writes — sources, flows,
// flow_collection, objects, segments, plus the two tag tables. The
// BIGSERIAL on segments.id restarts at 1 (RESTART IDENTITY) and any
// FK-dependent rows are removed by CASCADE.
//
// `idempotency_keys` and `schema_migrations` are deliberately untouched:
// the loader doesn't write them, and truncating `schema_migrations`
// would lie to `opentams migrate version`.
//
// Reset is destructive. Callers gate it behind an explicit user choice
// (the CLI's `--reset` flag); Load itself never calls it.
func Reset(ctx context.Context, pool *pgxpool.Pool) error {
	const stmt = `TRUNCATE TABLE
        sources, flows, flow_collection, objects, segments, source_tags, flow_tags
        RESTART IDENTITY CASCADE`
	if _, err := pool.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("reset: truncate: %w", err)
	}
	return nil
}

func dropSegmentsConstraintsAndIndex(ctx context.Context, pool *pgxpool.Pool) error {
	stmts := []string{
		`ALTER TABLE segments DROP CONSTRAINT IF EXISTS no_segment_overlap`,
		`ALTER TABLE segments DROP CONSTRAINT IF EXISTS segments_flow_id_fkey`,
		`ALTER TABLE segments DROP CONSTRAINT IF EXISTS segments_object_id_fkey`,
		`DROP INDEX IF EXISTS segments_flow_lower`,
	}
	for _, s := range stmts {
		if _, err := pool.Exec(ctx, s); err != nil {
			return fmt.Errorf("pre-load drop: %w (stmt: %s)", err, s)
		}
	}
	return nil
}

// rebuildSegmentsConstraintsAndIndex re-creates everything dropped, in
// an order tuned for end-of-load cost:
//
//  1. btree first — small, cheap, and used by FK VALIDATE below to seek
//     into segments by (flow_id, lower_ns).
//  2. GiST EXCLUDE — validates non-overlap on the loaded data; a
//     generator bug surfaces as a constraint-violation error pointing
//     at the offending pair of rows.
//  3. FKs added as NOT VALID first (instant, no per-row check) then
//     VALIDATE CONSTRAINT — does the integrity sweep in one pass using
//     the parent-table primary key indexes, and does NOT take the
//     strong lock that a plain ADD CONSTRAINT would.
//
// The recreated objects' definitions match migrations/000001_init.up.sql
// exactly — the scale test would be measuring a different schema
// otherwise.
func rebuildSegmentsConstraintsAndIndex(ctx context.Context, pool *pgxpool.Pool) error {
	stmts := []string{
		`CREATE INDEX segments_flow_lower ON segments (flow_id, lower_ns)`,
		`ALTER TABLE segments ADD CONSTRAINT no_segment_overlap
            EXCLUDE USING gist (
                flow_id WITH =,
                int8range(lower_ns, COALESCE(upper_ns, 9223372036854775807)) WITH &&
            )`,
		`ALTER TABLE segments ADD CONSTRAINT segments_flow_id_fkey
            FOREIGN KEY (flow_id) REFERENCES flows(id) ON DELETE CASCADE NOT VALID`,
		`ALTER TABLE segments ADD CONSTRAINT segments_object_id_fkey
            FOREIGN KEY (object_id) REFERENCES objects(id) NOT VALID`,
		`ALTER TABLE segments VALIDATE CONSTRAINT segments_flow_id_fkey`,
		`ALTER TABLE segments VALIDATE CONSTRAINT segments_object_id_fkey`,
	}
	for _, s := range stmts {
		if _, err := pool.Exec(ctx, s); err != nil {
			return fmt.Errorf("post-load rebuild: %w (stmt: %s)", err, s)
		}
	}
	return nil
}

// Per-table copy helpers ----------------------------------------------
//
// Each acquires one connection and loops chunked CopyFrom calls until
// the underlying iterator reports exhaustion. The repetition between
// helpers is deliberate — the iterator and column-list types differ
// per table, and a generic abstraction would obscure more than it saves.

func copySources(ctx context.Context, pool *pgxpool.Pool, g *dataset.Gen, opts LoadOptions) error {
	if g.Plan().SourcesCount == 0 {
		return nil
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("sources: acquire: %w", err)
	}
	defer conn.Release()

	cols := []string{"id", "format", "label", "description"}
	it := g.Sources()
	var done int64
	for {
		src := &sourceCopySource{it: it, limit: opts.ChunkSize}
		n, err := conn.Conn().CopyFrom(ctx, pgx.Identifier{"sources"}, cols, src)
		if err != nil {
			return fmt.Errorf("sources: copy: %w", err)
		}
		done += n
		if n > 0 {
			opts.ProgressFn("sources", done, g.Plan().SourcesCount)
		}
		if src.exhausted {
			break
		}
	}
	if done != g.Plan().SourcesCount {
		return fmt.Errorf("sources: copied %d, expected %d", done, g.Plan().SourcesCount)
	}
	return nil
}

func copyFlows(ctx context.Context, pool *pgxpool.Pool, g *dataset.Gen, opts LoadOptions) error {
	if g.Plan().TotalFlows == 0 {
		return nil
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("flows: acquire: %w", err)
	}
	defer conn.Release()

	cols := []string{"id", "source_id", "format", "codec", "container", "label",
		"segment_duration", "read_only", "timerange"}
	it := g.Flows()
	var done int64
	for {
		src := &flowCopySource{it: it, limit: opts.ChunkSize}
		n, err := conn.Conn().CopyFrom(ctx, pgx.Identifier{"flows"}, cols, src)
		if err != nil {
			return fmt.Errorf("flows: copy: %w", err)
		}
		done += n
		if n > 0 {
			opts.ProgressFn("flows", done, g.Plan().TotalFlows)
		}
		if src.exhausted {
			break
		}
	}
	if done != g.Plan().TotalFlows {
		return fmt.Errorf("flows: copied %d, expected %d", done, g.Plan().TotalFlows)
	}
	return nil
}

func copyFlowCollection(ctx context.Context, pool *pgxpool.Pool, g *dataset.Gen, opts LoadOptions) error {
	if g.Plan().FlowCollectionRows == 0 {
		return nil
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("flow_collection: acquire: %w", err)
	}
	defer conn.Release()

	cols := []string{"flow_id", "item_id", "sort_order"}
	it := g.FlowCollection()
	var done int64
	for {
		src := &flowCollectionCopySource{it: it, limit: opts.ChunkSize}
		n, err := conn.Conn().CopyFrom(ctx, pgx.Identifier{"flow_collection"}, cols, src)
		if err != nil {
			return fmt.Errorf("flow_collection: copy: %w", err)
		}
		done += n
		if n > 0 {
			opts.ProgressFn("flow_collection", done, g.Plan().FlowCollectionRows)
		}
		if src.exhausted {
			break
		}
	}
	if done != g.Plan().FlowCollectionRows {
		return fmt.Errorf("flow_collection: copied %d, expected %d", done, g.Plan().FlowCollectionRows)
	}
	return nil
}

func copyObjects(ctx context.Context, pool *pgxpool.Pool, g *dataset.Gen, opts LoadOptions) error {
	if g.Plan().ObjectsCount == 0 {
		return nil
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("objects: acquire: %w", err)
	}
	defer conn.Release()

	cols := []string{"id", "ref_count", "reaping"}
	it := g.Objects()
	var done int64
	for {
		src := &objectCopySource{it: it, limit: opts.ChunkSize}
		n, err := conn.Conn().CopyFrom(ctx, pgx.Identifier{"objects"}, cols, src)
		if err != nil {
			return fmt.Errorf("objects: copy: %w", err)
		}
		done += n
		if n > 0 {
			opts.ProgressFn("objects", done, g.Plan().ObjectsCount)
		}
		if src.exhausted {
			break
		}
	}
	if done != g.Plan().ObjectsCount {
		return fmt.Errorf("objects: copied %d, expected %d", done, g.Plan().ObjectsCount)
	}
	return nil
}

func copySegments(ctx context.Context, pool *pgxpool.Pool, g *dataset.Gen, opts LoadOptions) error {
	if g.Plan().TotalSegments == 0 {
		return nil
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("segments: acquire: %w", err)
	}
	defer conn.Release()

	cols := []string{"flow_id", "object_id", "timerange", "lower_ns", "upper_ns", "ts_offset"}
	it := g.Segments()
	var done int64
	for {
		src := &segmentCopySource{it: it, limit: opts.ChunkSize}
		n, err := conn.Conn().CopyFrom(ctx, pgx.Identifier{"segments"}, cols, src)
		if err != nil {
			return fmt.Errorf("segments: copy: %w", err)
		}
		done += n
		if n > 0 {
			opts.ProgressFn("segments", done, g.Plan().TotalSegments)
		}
		if src.exhausted {
			break
		}
	}
	if done != g.Plan().TotalSegments {
		return fmt.Errorf("segments: copied %d, expected %d", done, g.Plan().TotalSegments)
	}
	return nil
}

// copySegmentsParallel splits the segments COPY across opts.Workers
// goroutines. Each worker holds its own pool connection and writes a
// disjoint slice of flow_ids via PartitionedSegmentIter — by
// construction the slices share no flow_id, so per-flow constraints
// (GiST, btree, FKs) cannot contend across workers. The constraints
// are dropped during the load anyway, but the partitioning keeps the
// approach correctness-preserving even if a future variant runs with
// constraints enabled.
//
// Workers <= 1 falls back to the sequential copySegments path.
// Pool MaxConns must be >= Workers + 1 (the +1 leaves headroom for
// the DDL drop/rebuild and ANALYZE that bracket the COPY phase).
func copySegmentsParallel(ctx context.Context, pool *pgxpool.Pool, g *dataset.Gen, opts LoadOptions) error {
	if g.Plan().TotalSegments == 0 {
		return nil
	}
	if opts.Workers <= 1 {
		return copySegments(ctx, pool, g, opts)
	}

	cols := []string{"flow_id", "object_id", "timerange", "lower_ns", "upper_ns", "ts_offset"}
	var doneTotal atomic.Int64
	grp, gctx := errgroup.WithContext(ctx)

	for w := range opts.Workers {
		grp.Go(func() error {
			conn, err := pool.Acquire(gctx)
			if err != nil {
				return fmt.Errorf("segments worker %d: acquire: %w", w, err)
			}
			defer conn.Release()

			it := g.PartitionedSegments(w, opts.Workers)
			for {
				src := &segmentCopySource{it: it, limit: opts.ChunkSize}
				n, err := conn.Conn().CopyFrom(gctx, pgx.Identifier{"segments"}, cols, src)
				if err != nil {
					return fmt.Errorf("segments worker %d: copy: %w", w, err)
				}
				if n > 0 {
					// Per-chunk progress; doneTotal is shared across workers
					// so the user sees a single monotonically increasing %.
					total := doneTotal.Add(n)
					opts.ProgressFn("segments", total, g.Plan().TotalSegments)
				}
				if src.exhausted {
					return nil
				}
			}
		})
	}

	if err := grp.Wait(); err != nil {
		return err
	}
	if got := doneTotal.Load(); got != g.Plan().TotalSegments {
		return fmt.Errorf("segments parallel: copied %d, expected %d", got, g.Plan().TotalSegments)
	}
	return nil
}

// pgx.CopyFromSource adapters -----------------------------------------
//
// Each wraps one *Iter with a per-chunk row counter. Next() returns
// false once the chunk limit is hit OR the underlying iterator is
// exhausted; the `exhausted` flag tells the caller which happened so
// the outer loop can decide whether to start another chunk.

type sourceCopySource struct {
	it        *dataset.SourceIter
	n         int64
	limit     int64
	exhausted bool
}

func (s *sourceCopySource) Next() bool {
	if s.limit > 0 && s.n >= s.limit {
		return false
	}
	if !s.it.Next() {
		s.exhausted = true
		return false
	}
	s.n++
	return true
}
func (s *sourceCopySource) Values() ([]any, error) {
	r := s.it.Row()
	return []any{r.ID, r.Format, r.Label, r.Description}, nil
}
func (s *sourceCopySource) Err() error { return nil }

type flowCopySource struct {
	it        *dataset.FlowIter
	n         int64
	limit     int64
	exhausted bool
}

func (s *flowCopySource) Next() bool {
	if s.limit > 0 && s.n >= s.limit {
		return false
	}
	if !s.it.Next() {
		s.exhausted = true
		return false
	}
	s.n++
	return true
}
func (s *flowCopySource) Values() ([]any, error) {
	r := s.it.Row()
	return []any{r.ID, r.SourceID, r.Format, r.Codec, r.Container, r.Label,
		r.SegmentDuration, r.ReadOnly, r.Timerange}, nil
}
func (s *flowCopySource) Err() error { return nil }

type flowCollectionCopySource struct {
	it        *dataset.FlowCollectionIter
	n         int64
	limit     int64
	exhausted bool
}

func (s *flowCollectionCopySource) Next() bool {
	if s.limit > 0 && s.n >= s.limit {
		return false
	}
	if !s.it.Next() {
		s.exhausted = true
		return false
	}
	s.n++
	return true
}
func (s *flowCollectionCopySource) Values() ([]any, error) {
	r := s.it.Row()
	return []any{r.FlowID, r.ItemID, r.SortOrder}, nil
}
func (s *flowCollectionCopySource) Err() error { return nil }

type objectCopySource struct {
	it        *dataset.ObjectIter
	n         int64
	limit     int64
	exhausted bool
}

func (s *objectCopySource) Next() bool {
	if s.limit > 0 && s.n >= s.limit {
		return false
	}
	if !s.it.Next() {
		s.exhausted = true
		return false
	}
	s.n++
	return true
}
func (s *objectCopySource) Values() ([]any, error) {
	r := s.it.Row()
	return []any{r.ID, r.RefCount, r.Reaping}, nil
}
func (s *objectCopySource) Err() error { return nil }

// segmentRowIter is the contract segmentCopySource consumes — either
// the sequential SegmentIter or the per-worker PartitionedSegmentIter.
// Both satisfy via *SegmentIter / *PartitionedSegmentIter methods.
type segmentRowIter interface {
	Next() bool
	Row() dataset.SegmentRow
}

type segmentCopySource struct {
	it        segmentRowIter
	n         int64
	limit     int64
	exhausted bool
}

func (s *segmentCopySource) Next() bool {
	if s.limit > 0 && s.n >= s.limit {
		return false
	}
	if !s.it.Next() {
		s.exhausted = true
		return false
	}
	s.n++
	return true
}
func (s *segmentCopySource) Values() ([]any, error) {
	r := s.it.Row()
	return []any{r.FlowID, r.ObjectID, r.Timerange, r.LowerNs, r.UpperNs, r.TsOffset}, nil
}
func (s *segmentCopySource) Err() error { return nil }
