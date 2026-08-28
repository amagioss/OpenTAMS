// Package report is the shared, serialization-only contract for the
// OpenTAMS scale-test harness output. Both measurement tiers — the
// DB-direct bench (tools/scaletest/bench) and the HTTP load generator
// (tools/scaletest/loadgen) — produce the same Report shape so a single
// dashboard / diff tool renders either tier and so runs are comparable
// across schema variants (docs/scale-test-plan.md §5).
//
// The package owns its JSON wire format explicitly (millisecond floats,
// stable field names) rather than leaking an internal type's layout. It
// depends only on internal/perftest for the nearest-rank latency
// summary; it imports no database or HTTP code, keeping it cheap for any
// tier to pull in.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/amagioss/opentams/internal/perftest"
)

// Op classifies a bucket so reports can group read vs write vs delete
// workloads (the per-bucket split is a first-class plan requirement).
type Op string

// Workload operation classes for grouping buckets in reports.
const (
	OpRead   Op = "read"
	OpWrite  Op = "write"
	OpDelete Op = "delete"
)

// LatencyMs is the per-bucket latency summary in milliseconds. Floats
// (not time.Duration) so the JSON is human-readable and unit-explicit;
// percentiles use the nearest-rank method via perftest.Summarize.
type LatencyMs struct {
	Min float64 `json:"min_ms"`
	P50 float64 `json:"p50_ms"`
	P95 float64 `json:"p95_ms"`
	P99 float64 `json:"p99_ms"`
	Max float64 `json:"max_ms"`
}

// BucketResult is one workload bucket's measured result.
type BucketResult struct {
	Name             string    `json:"name"`
	Op               Op        `json:"op"`
	Samples          int       `json:"samples"`
	Errors           int       `json:"errors"`
	ErrorRate        float64   `json:"error_rate"`
	ThroughputPerSec float64   `json:"throughput_per_sec"`
	ElapsedMs        float64   `json:"elapsed_ms"`
	Latency          LatencyMs `json:"latency"`
}

// NewBucketResult builds a BucketResult from the raw measurement: the
// per-call latency samples of the SUCCESSFUL calls, the count of failed
// calls, and the wall-clock elapsed for the whole bucket.
//
// Throughput counts only successful calls (errors did no useful work);
// error rate is errors / total attempts (successes + errors), guarded
// against division by zero. Latency percentiles come from the success
// samples only — a rejected/failed call's timing is not a latency SLO
// data point.
//
// Note on op-semantics: for buckets where the measured "success" is not
// useful work — overlap-rejection (a rejection is the success) and
// delete buckets that may match zero rows — ThroughputPerSec is the rate
// of that operation, not of useful work done. Read it alongside the
// bucket's name/op, not as a standalone work-rate.
func NewBucketResult(name string, op Op, latencySamples []time.Duration, errCount int, elapsed time.Duration) BucketResult {
	s := perftest.Summarize(latencySamples)
	successes := len(latencySamples)

	var throughput float64
	if elapsed > 0 {
		throughput = float64(successes) / elapsed.Seconds()
	}
	var errRate float64
	if total := successes + errCount; total > 0 {
		errRate = float64(errCount) / float64(total)
	}

	return BucketResult{
		Name:             name,
		Op:               op,
		Samples:          successes,
		Errors:           errCount,
		ErrorRate:        errRate,
		ThroughputPerSec: throughput,
		ElapsedMs:        ms(elapsed),
		Latency: LatencyMs{
			Min: ms(s.Min),
			P50: ms(s.P50),
			P95: ms(s.P95),
			P99: ms(s.P99),
			Max: ms(s.Max),
		},
	}
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// humanBytes renders a byte count in the largest unit that keeps the
// value readable (used only in the markdown summary).
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// DatasetInfo records the shape of the data a run measured against, so a
// report is interpretable without the invoking command line.
type DatasetInfo struct {
	Preset           string `json:"preset"`
	Seed             uint64 `json:"seed"`
	ChunkDurationSec int    `json:"chunk_duration_sec"`
	TotalSegments    int64  `json:"total_segments"`
	TotalFlows       int64  `json:"total_flows"`
	SourcesCount     int64  `json:"sources_count"`
	ObjectsCount     int64  `json:"objects_count"`
	DeepFlowCount    int64  `json:"deep_flow_count"`
	DeepDepth        int64  `json:"deep_depth"`
	ShallowDepth     int64  `json:"shallow_depth"`
}

// DBInfo captures the database under test: version string and the
// scale-relevant settings (work_mem, maintenance_work_mem, …) so two
// reports can be compared with their tuning differences visible. Stats,
// when present, holds the before→after `pg_stat_*` deltas measured over
// the run (D5b resource capture).
type DBInfo struct {
	Version  string            `json:"version"`
	Settings map[string]string `json:"settings"`
	Stats    *DBStatsDelta     `json:"stats,omitempty"`
}

// DBStatsDelta is the change in the database's cumulative counters over a
// run, plus current per-table state. It is the portable DB-side signal
// (standard catalog views, identical on RDS / Cloud SQL) that explains a
// latency result: cache misses, WAL pressure, checkpoint storms,
// autovacuum lag, and table/index bloat. Zero-valued fields mean the
// underlying view/column wasn't available (captured best-effort across
// Postgres versions and extension availability).
type DBStatsDelta struct {
	Commits       int64             `json:"commits"`
	Rollbacks     int64             `json:"rollbacks"`
	BlksHit       int64             `json:"blks_hit"`
	BlksRead      int64             `json:"blks_read"`
	CacheHitRatio float64           `json:"cache_hit_ratio"` // hit/(hit+read) over the run
	Deadlocks     int64             `json:"deadlocks"`
	TempFiles     int64             `json:"temp_files"`
	TempBytes     int64             `json:"temp_bytes"`
	WALBytes      int64             `json:"wal_bytes"`
	Checkpoints   int64             `json:"checkpoints"`
	Tables        []TableStatsDelta `json:"tables,omitempty"`
	TopQueries    []QueryStat       `json:"top_queries,omitempty"`
}

// TableStatsDelta is per-table change over a run. Tuple counts are the
// current ("after") snapshot; sizes are captured both ends so growth is
// visible.
type TableStatsDelta struct {
	Name            string `json:"name"`
	LiveTuples      int64  `json:"live_tuples"`
	DeadTuples      int64  `json:"dead_tuples"`
	Autovacuums     int64  `json:"autovacuums"`
	Autoanalyzes    int64  `json:"autoanalyzes"`
	SizeBytesBefore int64  `json:"size_bytes_before"`
	SizeBytesAfter  int64  `json:"size_bytes_after"`
}

// QueryStat is one entry from pg_stat_statements (only populated when the
// extension is enabled), ranked by total execution time over the run.
type QueryStat struct {
	Query       string  `json:"query"`
	Calls       int64   `json:"calls"`
	TotalTimeMs float64 `json:"total_time_ms"`
	MeanTimeMs  float64 `json:"mean_time_ms"`
}

// HostInfo records where the bench process ran — relevant because a
// DB-direct measurement includes the host↔DB network round-trip.
type HostInfo struct {
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	NumCPU   int    `json:"num_cpu"`
}

// LoadStats is the run-level load-control summary for the open-loop HTTP
// tier (tools/scaletest/loadgen). It is a pointer on Report (omitempty)
// because the DB-direct bench tier has no offered-rate / in-flight
// concept and omits it.
//
//   - Attempted: jobs the scheduler tried to dispatch (offered load).
//   - Sent:      jobs that acquired an in-flight slot and were issued.
//   - Completed: jobs that received a response (success or error).
//   - Shed:      Attempted − Sent — jobs dropped because --max-inflight
//     was saturated (the open-loop overload signal).
//   - MaxInflight: high-water mark of concurrent in-flight requests.
//
// The *RPS fields are the corresponding counts divided by the measured
// (non-warmup) wall-clock seconds.
type LoadStats struct {
	TargetRPSBaseline float64 `json:"target_rps_baseline"`
	TargetRPSBurst    float64 `json:"target_rps_burst"`
	Attempted         int64   `json:"attempted"`
	Sent              int64   `json:"sent"`
	Completed         int64   `json:"completed"`
	Shed              int64   `json:"shed"`
	ShedRate          float64 `json:"shed_rate"`
	MaxInflight       int64   `json:"max_inflight"`
	AttemptedRPS      float64 `json:"attempted_rps"`
	SentRPS           float64 `json:"sent_rps"`
	CompletedRPS      float64 `json:"completed_rps"`
}

// NewLoadStats computes the rate fields from the raw counters and the
// measurement duration, guarding against division by zero.
func NewLoadStats(attempted, sent, completed, shed, maxInflight int64, measured time.Duration, baselineRPS, burstRPS float64) LoadStats {
	ls := LoadStats{
		TargetRPSBaseline: baselineRPS,
		TargetRPSBurst:    burstRPS,
		Attempted:         attempted,
		Sent:              sent,
		Completed:         completed,
		Shed:              shed,
		MaxInflight:       maxInflight,
	}
	if attempted > 0 {
		ls.ShedRate = float64(shed) / float64(attempted)
	}
	if secs := measured.Seconds(); secs > 0 {
		ls.AttemptedRPS = float64(attempted) / secs
		ls.SentRPS = float64(sent) / secs
		ls.CompletedRPS = float64(completed) / secs
	}
	return ls
}

// RampResult is the output of an overload-ramp run (D6): the offered
// rate is stepped up until shed or error rate crosses a threshold. The
// breaking point is the first step that crossed it (0 if the run reached
// its max RPS without breaking).
type RampResult struct {
	BreakingPointRPS float64    `json:"breaking_point_rps"`
	Steps            []RampStep `json:"steps"`
}

// RampStep summarizes one fixed-rate step of a ramp.
type RampStep struct {
	TargetRPS    float64 `json:"target_rps"`
	AttemptedRPS float64 `json:"attempted_rps"`
	CompletedRPS float64 `json:"completed_rps"`
	ShedRate     float64 `json:"shed_rate"`
	ErrorRate    float64 `json:"error_rate"`
	MaxP99Ms     float64 `json:"max_p99_ms"` // worst per-bucket p99 in the step
}

// Report is the full output of one measurement run.
type Report struct {
	RunID      string         `json:"run_id"`
	Tool       string         `json:"tool"`
	ToolCommit string         `json:"tool_commit"`
	StartedAt  time.Time      `json:"started_at"`
	Host       HostInfo       `json:"host"`
	DB         DBInfo         `json:"db"`
	Dataset    DatasetInfo    `json:"dataset"`
	Load       *LoadStats     `json:"load,omitempty"`
	Ramp       *RampResult    `json:"ramp,omitempty"`
	Buckets    []BucketResult `json:"buckets"`
}

// WriteJSON serializes the report as indented JSON.
func (r *Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return fmt.Errorf("report: encode json: %w", err)
	}
	return nil
}

// WriteJSONFile writes the report to path, creating/truncating it.
func (r *Report) WriteJSONFile(path string) error {
	f, err := os.Create(path) //nolint:gosec // path is an operator-supplied report destination.
	if err != nil {
		return fmt.Errorf("report: create %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // write error surfaces via WriteJSON; close on a written file is best-effort.
	return r.WriteJSON(f)
}

// Markdown renders a human-readable per-bucket summary table plus a
// dataset/DB header. Intended for the bench's stderr summary and for
// pasting into run notes; the JSON is the machine-readable form.
func (r *Report) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Scale-test report — %s\n\n", r.Tool)
	fmt.Fprintf(&b, "- preset: `%s`  seed: `%d`  segments: `%d`  flows: `%d`\n",
		r.Dataset.Preset, r.Dataset.Seed, r.Dataset.TotalSegments, r.Dataset.TotalFlows)
	if r.DB.Version != "" {
		fmt.Fprintf(&b, "- db: `%s`\n", r.DB.Version)
	}
	if r.Load != nil {
		l := r.Load
		fmt.Fprintf(&b, "- load: target %.0f→%.0f rps · attempted %.0f/s · sent %.0f/s · completed %.0f/s · shed %d (%.2f%%) · max in-flight %d\n",
			l.TargetRPSBaseline, l.TargetRPSBurst, l.AttemptedRPS, l.SentRPS, l.CompletedRPS,
			l.Shed, l.ShedRate*100, l.MaxInflight)
	}
	if s := r.DB.Stats; s != nil {
		fmt.Fprintf(&b, "- db stats: cache hit %.1f%% · WAL %s · checkpoints %d · commits %d · deadlocks %d · temp %s\n",
			s.CacheHitRatio*100, humanBytes(s.WALBytes), s.Checkpoints, s.Commits, s.Deadlocks, humanBytes(s.TempBytes))
		for _, tbl := range s.Tables {
			fmt.Fprintf(&b, "  - %s: live %d · dead %d · autovac %d · size %s→%s\n",
				tbl.Name, tbl.LiveTuples, tbl.DeadTuples, tbl.Autovacuums,
				humanBytes(tbl.SizeBytesBefore), humanBytes(tbl.SizeBytesAfter))
		}
	}
	if r.Ramp != nil {
		bp := "none (held to max)"
		if r.Ramp.BreakingPointRPS > 0 {
			bp = fmt.Sprintf("%.0f rps", r.Ramp.BreakingPointRPS)
		}
		fmt.Fprintf(&b, "\n### Overload ramp — breaking point: %s\n\n", bp)
		b.WriteString("| target rps | attempted/s | completed/s | shed% | err% | max p99 ms |\n")
		b.WriteString("|--:|--:|--:|--:|--:|--:|\n")
		for _, s := range r.Ramp.Steps {
			fmt.Fprintf(&b, "| %.0f | %.0f | %.0f | %.2f | %.2f | %.2f |\n",
				s.TargetRPS, s.AttemptedRPS, s.CompletedRPS, s.ShedRate*100, s.ErrorRate*100, s.MaxP99Ms)
		}
	}
	b.WriteString("\n")
	if len(r.Buckets) == 0 {
		return b.String()
	}
	b.WriteString("| bucket | op | samples | errors | err% | throughput/s | p50 ms | p95 ms | p99 ms | max ms |\n")
	b.WriteString("|---|---|--:|--:|--:|--:|--:|--:|--:|--:|\n")
	for _, bk := range r.Buckets {
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %.2f | %.1f | %.2f | %.2f | %.2f | %.2f |\n",
			bk.Name, bk.Op, bk.Samples, bk.Errors, bk.ErrorRate*100, bk.ThroughputPerSec,
			bk.Latency.P50, bk.Latency.P95, bk.Latency.P99, bk.Latency.Max)
	}
	return b.String()
}
