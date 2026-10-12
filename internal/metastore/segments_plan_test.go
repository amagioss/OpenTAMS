//go:build integration || perf

package metastore

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The against-existing overlap check runs once per segment in a batch, so
// it must use the no_segment_overlap GiST index, not scan every segment of
// the flow. Postgres uses an expression index only when the query repeats
// the index expression exactly, including its COALESCE. Otherwise the
// index serves only flow_id and the range becomes a filter. Sequential
// scans are disabled so that the planner shows which index the query can
// use on a small test table.
func TestExistingOverlapCheckUsesExclusionIndex(t *testing.T) {
	_, tx := withTx(t)
	ctx := context.Background()

	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatalf("disable seqscan: %v", err)
	}
	rows, err := tx.Query(ctx, "EXPLAIN "+existingOverlapSQL, uuid.New(), int64(0), int64(10))
	if err != nil {
		t.Fatalf("EXPLAIN: %v", err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan.WriteString(line + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan rows: %v", err)
	}
	indexCond := ""
	for _, line := range strings.Split(plan.String(), "\n") {
		if strings.Contains(line, "Index Cond:") {
			indexCond = line
		}
	}
	if !strings.Contains(plan.String(), "no_segment_overlap") || !strings.Contains(indexCond, "&&") {
		t.Errorf("the range comparison is not an index condition on no_segment_overlap, so the check reads every segment of the flow:\n%s", plan.String())
	}
}
