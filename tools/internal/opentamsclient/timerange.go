package opentamsclient

import (
	"fmt"
	"time"

	"github.com/amagioss/opentams/internal/timerange"
)

const nsPerSec = int64(time.Second)

// FormatTS formats a nanosecond offset since some agreed-on epoch as
// the canonical TAMS `{sec}:{ns}` form. Negative values are emitted
// with a leading `-`.
func FormatTS(ns int64) string {
	ts := timerange.Timestamp{
		Seconds: ns / nsPerSec,
		// FormatTS works on positive offsets in the demo's clock, so the
		// fractional component is always non-negative. timerange.Timestamp
		// requires Nanoseconds in [0, 1e9), so for negative inputs we
		// renormalize. int32 cast is safe: `r` is in [0, 1e9).
		Nanoseconds: int32(ns % nsPerSec), //nolint:gosec // bounded by modulus
	}
	if ns < 0 && ts.Nanoseconds != 0 {
		ts.Seconds--
		ts.Nanoseconds += int32(nsPerSec) //nolint:gosec // constant nsPerSec < MaxInt32
	}
	return ts.String()
}

// HalfOpenRange returns a TAMS timerange `[startNs:endNs)` covering the
// duration `[start, end)`. Panics if end <= start — the demo tools have
// no use for an empty range so a programmer error is louder this way.
func HalfOpenRange(startNs, endNs int64) string {
	if endNs <= startNs {
		panic(fmt.Sprintf("opentamsclient.HalfOpenRange: empty/inverted range [%d:%d)", startNs, endNs))
	}
	return "[" + FormatTS(startNs) + "_" + FormatTS(endNs) + ")"
}

// ParseRange parses a TAMS bracket-notation timerange and returns the
// half-open `[startNs, endNs)` nanosecond bounds. Open-ended sides
// return MinInt64 / MaxInt64. Unsupported (eternity, instantaneous-only)
// inputs return an error.
func ParseRange(s string) (startNs, endNs int64, err error) {
	tr, err := timerange.Parse(s)
	if err != nil {
		return 0, 0, err
	}
	startNs = minInt64
	endNs = maxInt64
	if tr.Start != nil {
		startNs = tr.Start.Seconds*nsPerSec + int64(tr.Start.Nanoseconds)
		if tr.StartType == timerange.Exclusive {
			startNs++
		}
	}
	if tr.End != nil {
		endNs = tr.End.Seconds*nsPerSec + int64(tr.End.Nanoseconds)
		if tr.EndType == timerange.Inclusive {
			endNs++
		}
	}
	return startNs, endNs, nil
}

const (
	minInt64 = -1 << 63
	maxInt64 = 1<<63 - 1
)
