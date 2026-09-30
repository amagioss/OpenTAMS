package timerange

import (
	"errors"
	"fmt"
	"math"
	"time"
)

const nsPerSec = 1_000_000_000

// Errors returned by NsBounds. Callers match them with errors.Is.
var (
	ErrOutOfRange = errors.New("timerange: bound outside the int64 nanosecond range")
	ErrEmptyRange = errors.New("timerange: empty range has no bounds")
)

// NsBounds is the half-open interval [Lower, Upper) in int64 nanoseconds.
type NsBounds struct {
	Lower          int64 // inclusive; meaningful only when !LowerUnbounded
	Upper          int64 // exclusive; meaningful only when !UpperUnbounded
	LowerUnbounded bool
	UpperUnbounded bool
}

// NsBounds converts a non-empty range into half-open int64 nanosecond bounds
// for database comparison. An exclusive start and an inclusive end move by
// one nanosecond. The result is never rendered back to text.
func (tr TimeRange) NsBounds() (NsBounds, error) {
	if tr.IsEmpty() {
		return NsBounds{}, ErrEmptyRange
	}
	var b NsBounds
	if tr.Start == nil {
		b.LowerUnbounded = true
	} else {
		v, err := boundNanos(*tr.Start, tr.StartType == Exclusive)
		if err != nil {
			return NsBounds{}, fmt.Errorf("start of %s: %w", tr, err)
		}
		b.Lower = v
	}
	if tr.End == nil {
		b.UpperUnbounded = true
	} else {
		v, err := boundNanos(*tr.End, tr.EndType == Inclusive)
		if err != nil {
			return NsBounds{}, fmt.Errorf("end of %s: %w", tr, err)
		}
		b.Upper = v
	}
	return b, nil
}

func boundNanos(ts Timestamp, addOne bool) (int64, error) {
	v, ok := ts.nanos()
	if !ok || (addOne && v == math.MaxInt64) {
		return 0, ErrOutOfRange
	}
	if addOne {
		v++
	}
	return v, nil
}

// nanos returns the value in int64 nanoseconds, or false if it does not fit.
// A negative second with nanoseconds is computed from Seconds+1, so that
// math.MinInt64 itself does not overflow on the way.
func (ts Timestamp) nanos() (int64, bool) {
	s, ns := ts.Seconds, int64(ts.Nanoseconds)
	if s < 0 && ns > 0 {
		s++
		ns -= nsPerSec
	}
	if s > math.MaxInt64/nsPerSec || s < math.MinInt64/nsPerSec {
		return 0, false
	}
	v := s * nsPerSec
	if (ns > 0 && v > math.MaxInt64-ns) || (ns < 0 && v < math.MinInt64-ns) {
		return 0, false
	}
	return v + ns, true
}

// TimestampFromDuration returns d as a floor-normalised Timestamp. TAMS uses
// the Timestamp type for durations such as min_object_timeout.
func TimestampFromDuration(d time.Duration) Timestamp {
	s, ns := int64(d)/nsPerSec, int64(d)%nsPerSec
	if ns < 0 {
		s--
		ns += nsPerSec
	}
	return Timestamp{Seconds: s, Nanoseconds: int32(ns)} //nolint:gosec // 0 ≤ ns < 10⁹
}
