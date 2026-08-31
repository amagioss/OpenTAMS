package main

import (
	"fmt"
	"math/rand"
)

// opKind enumerates the HTTP operations the load generator can issue.
// One pick of opIdempotentRetry issues TWO requests (first + dedupe),
// recorded into separate report buckets — see the executor.
type opKind int

const (
	opListByFlow opKind = iota
	opListByTimerange
	opRegisterBulk
	opInsertDeep
	opInsertShallow
	opCreateStorage
	opIdempotentRetry
	opDeleteByTimerange
	opDeleteByObject

	numOpKinds // count of opKind values; keep last. Sizes per-op arrays.
)

// weightedOp is one entry in a mix profile: an operation and its
// relative weight (weights need not sum to any particular total — pick
// normalises by the running total).
type weightedOp struct {
	kind   opKind
	name   string
	weight float64
}

// mix is a weighted operation selector built from a named profile.
type mix struct {
	ops   []weightedOp
	cum   []float64 // cumulative weights, parallel to ops
	total float64
}

// Profiles. steady is the default non-destructive 60/40 read/write mix
// (no deletes) for repeatable steady-state numbers — reads never race
// shrinking data. destructive adds delete ops for teardown / GC-path
// measurement and must be opted into explicitly.
var profiles = map[string][]weightedOp{
	"steady": {
		{opListByTimerange, "list-by-timerange", 35},
		{opListByFlow, "list-by-flow", 25},
		{opRegisterBulk, "register-bulk", 14},
		{opInsertDeep, "insert-deep", 8},
		{opInsertShallow, "insert-shallow", 6},
		{opCreateStorage, "create-storage-endpoint", 6},
		{opIdempotentRetry, "idempotent-retry", 6},
	},
	// idem-contention concentrates load on the idempotent-retry op so
	// that, paired with --idem-keys=1 and high concurrency, many requests
	// collide on the same idempotency_keys row — the lock-contention
	// scenario. A slice of reads keeps the flow alive without deletes.
	"idem-contention": {
		{opIdempotentRetry, "idempotent-retry", 80},
		{opListByFlow, "list-by-flow", 20},
	},
	"destructive": {
		{opListByTimerange, "list-by-timerange", 32},
		{opListByFlow, "list-by-flow", 22},
		{opRegisterBulk, "register-bulk", 12},
		{opInsertDeep, "insert-deep", 7},
		{opInsertShallow, "insert-shallow", 5},
		{opCreateStorage, "create-storage-endpoint", 5},
		{opIdempotentRetry, "idempotent-retry", 5},
		{opDeleteByTimerange, "delete-by-timerange", 7},
		{opDeleteByObject, "delete-by-object", 5},
	},
}

// newMix builds the selector for a named profile.
func newMix(profile string) (*mix, error) {
	ops, ok := profiles[profile]
	if !ok {
		return nil, fmt.Errorf("unknown profile %q (want %q, %q, or %q)", profile, "steady", "destructive", "idem-contention")
	}
	m := &mix{ops: ops, cum: make([]float64, len(ops))}
	for i, op := range ops {
		if op.weight <= 0 {
			return nil, fmt.Errorf("profile %q op %q has non-positive weight %v", profile, op.name, op.weight)
		}
		m.total += op.weight
		m.cum[i] = m.total
	}
	return m, nil
}

// pick selects an op by weight using a single uniform draw.
func (m *mix) pick(r *rand.Rand) opKind {
	x := r.Float64() * m.total
	for i, c := range m.cum {
		if x < c {
			return m.ops[i].kind
		}
	}
	return m.ops[len(m.ops)-1].kind // float edge case
}

// weightFor returns the weight assigned to a kind (0 if absent).
func (m *mix) weightFor(k opKind) float64 {
	for _, op := range m.ops {
		if op.kind == k {
			return op.weight
		}
	}
	return 0
}
