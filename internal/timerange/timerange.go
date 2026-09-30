package timerange

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Timestamp is a TAI timestamp with nanosecond precision.
// Seconds may be negative. Nanoseconds must be in [0, 999_999_999].
type Timestamp struct {
	Seconds     int64
	Nanoseconds int32
}

// BoundType describes whether a TimeRange bound is inclusive, exclusive, or absent.
type BoundType int

// Bound types for TimeRange endpoints. Unbounded denotes an absent
// bound (open-ended on that side); the literal values are not
// significant beyond ordering — only the iota positions matter.
const (
	Inclusive BoundType = iota
	Exclusive
	Unbounded
)

// TimeRange is a TAI time interval in TAMS bracket notation.
type TimeRange struct {
	Start     *Timestamp
	StartType BoundType
	End       *Timestamp
	EndType   BoundType
}

// ParseTimestamp parses a timestamp string of the form {sign?}{seconds}:{nanoseconds}.
// The sign applies to the whole value, so "-1:500000000" is -1.5 s and
// "-0:500000000" is -0.5 s. The result is floor-normalised.
func ParseTimestamp(s string) (Timestamp, error) {
	neg := strings.HasPrefix(s, "-")
	secStr, nsStr, ok := strings.Cut(strings.TrimPrefix(s, "-"), ":")
	if !ok {
		return Timestamp{}, fmt.Errorf("invalid timestamp %q: expected seconds:nanoseconds", s)
	}
	if !validInt(secStr) {
		return Timestamp{}, fmt.Errorf("invalid timestamp %q: bad seconds field", s)
	}
	if !validNano(nsStr) {
		return Timestamp{}, fmt.Errorf("invalid timestamp %q: bad nanoseconds field", s)
	}

	mag, err := strconv.ParseUint(secStr, 10, 64)
	if err != nil {
		return Timestamp{}, fmt.Errorf("invalid timestamp %q: seconds out of range", s)
	}
	// validNano guarantees nsStr is valid digits ≤ 999_999_999, so ParseInt cannot fail here.
	ns, _ := strconv.ParseInt(nsStr, 10, 32)
	outOfRange := fmt.Errorf("invalid timestamp %q: seconds out of range", s)

	switch {
	case !neg || (mag == 0 && ns == 0):
		if mag > math.MaxInt64 {
			return Timestamp{}, outOfRange
		}
		return Timestamp{Seconds: int64(mag), Nanoseconds: int32(ns)}, nil //nolint:gosec // bounded above and by validNano
	case ns == 0:
		if mag > math.MaxInt64+1 {
			return Timestamp{}, outOfRange
		}
		return Timestamp{Seconds: -int64(mag-1) - 1}, nil //nolint:gosec // mag-1 ≤ MaxInt64
	default:
		if mag > math.MaxInt64 {
			return Timestamp{}, outOfRange
		}
		return Timestamp{Seconds: -int64(mag) - 1, Nanoseconds: int32(nsPerSec - ns)}, nil //nolint:gosec // bounded above and by validNano
	}
}

// String returns the canonical timestamp string: the magnitude as
// seconds:nanoseconds, with a leading "-" only for a value below zero.
func (ts Timestamp) String() string {
	if ts.Seconds >= 0 {
		return fmt.Sprintf("%d:%d", ts.Seconds, ts.Nanoseconds)
	}
	magSec := uint64(-(ts.Seconds + 1)) //nolint:gosec // Seconds+1 ≤ 0, so the negation is non-negative and fits
	magNs := nsPerSec - int64(ts.Nanoseconds)
	if ts.Nanoseconds == 0 {
		magSec++
		magNs = 0
	}
	return fmt.Sprintf("-%d:%d", magSec, magNs)
}

// before returns true if ts is strictly before other.
func (ts Timestamp) before(other Timestamp) bool {
	if ts.Seconds != other.Seconds {
		return ts.Seconds < other.Seconds
	}
	return ts.Nanoseconds < other.Nanoseconds
}

// next returns the timestamp one nanosecond after ts. Callers only use it
// when a later timestamp exists, so Seconds cannot overflow.
func (ts Timestamp) next() Timestamp {
	if ts.Nanoseconds < nsPerSec-1 {
		return Timestamp{Seconds: ts.Seconds, Nanoseconds: ts.Nanoseconds + 1}
	}
	return Timestamp{Seconds: ts.Seconds + 1}
}

// equal returns true if ts == other.
func (ts Timestamp) equal(other Timestamp) bool {
	return ts.Seconds == other.Seconds && ts.Nanoseconds == other.Nanoseconds
}

// Parse parses a TAMS timerange string of the form
// {start marker}{start timestamp}_{end timestamp}{end marker}, where every
// part is optional:
//
//	_              eternity; markers next to it are ignored
//	()             empty; so is any other string with no timestamp and no "_"
//	[ts_ts)        a bound range; an omitted marker means inclusive
//	(ts_           unbounded end; a marker next to an omitted timestamp is ignored
//	_ts)           unbounded start
//	[ts] or ts     instantaneous, the same as [ts_ts]
//
// An end before its start parses as an empty range, not as an error.
// An instantaneous range with an exclusive marker is an error.
func Parse(s string) (TimeRange, error) {
	if s == "" {
		return TimeRange{}, fmt.Errorf("invalid timerange %q: empty string", s)
	}
	body := s
	var startMarker, endMarker byte
	if body[0] == '[' || body[0] == '(' {
		startMarker = body[0]
		body = body[1:]
	}
	if n := len(body); n > 0 && (body[n-1] == ']' || body[n-1] == ')') {
		endMarker = body[n-1]
		body = body[:n-1]
	}

	startStr, endStr, hasSep := strings.Cut(body, "_")
	if !hasSep {
		if startStr == "" {
			return TimeRange{StartType: Exclusive, EndType: Exclusive}, nil
		}
		if startMarker == '(' || endMarker == ')' {
			return TimeRange{}, fmt.Errorf("invalid timerange %q: an instantaneous range cannot use exclusive markers", s)
		}
		ts, err := ParseTimestamp(startStr)
		if err != nil {
			return TimeRange{}, fmt.Errorf("invalid timerange %q: %w", s, err)
		}
		end := ts
		return TimeRange{Start: &ts, StartType: Inclusive, End: &end, EndType: Inclusive}, nil
	}

	tr := TimeRange{StartType: Unbounded, EndType: Unbounded}
	if startStr != "" {
		ts, err := ParseTimestamp(startStr)
		if err != nil {
			return TimeRange{}, fmt.Errorf("invalid timerange %q: %w", s, err)
		}
		tr.Start, tr.StartType = &ts, Inclusive
		if startMarker == '(' {
			tr.StartType = Exclusive
		}
	}
	if endStr != "" {
		ts, err := ParseTimestamp(endStr)
		if err != nil {
			return TimeRange{}, fmt.Errorf("invalid timerange %q: %w", s, err)
		}
		tr.End, tr.EndType = &ts, Inclusive
		if endMarker == ')' {
			tr.EndType = Exclusive
		}
	}
	return tr, nil
}

// String returns the canonical string for the timerange. It is for values
// the server derives; a value a client sent is returned as the client sent it.
// An empty range renders as "()" and an instantaneous range as "[ts]".
func (tr TimeRange) String() string {
	if tr.IsEmpty() {
		return "()"
	}
	if tr.IsEternity() {
		return "_"
	}
	if (tr.Start == nil) != (tr.StartType == Unbounded) || (tr.End == nil) != (tr.EndType == Unbounded) {
		panic("timerange: malformed TimeRange: a nil bound must be Unbounded")
	}
	if tr.Start != nil && tr.End != nil && tr.Start.equal(*tr.End) {
		return "[" + tr.Start.String() + "]"
	}

	var b strings.Builder
	if tr.Start != nil {
		if tr.StartType == Exclusive {
			b.WriteByte('(')
		} else {
			b.WriteByte('[')
		}
		b.WriteString(tr.Start.String())
	}
	b.WriteByte('_')
	if tr.End != nil {
		b.WriteString(tr.End.String())
		if tr.EndType == Exclusive {
			b.WriteByte(')')
		} else {
			b.WriteByte(']')
		}
	}
	return b.String()
}

// IsEmpty returns true if the range contains no instant. An instant is a
// whole nanosecond, so (0:0_0:1) is empty as well as "()", an end before its
// start, and an end equal to its start with an exclusive marker.
func (tr TimeRange) IsEmpty() bool {
	switch {
	case tr.Start == nil && tr.End == nil:
		return tr.StartType != Unbounded || tr.EndType != Unbounded
	case tr.Start == nil || tr.End == nil:
		return false
	case tr.End.before(*tr.Start):
		return true
	case tr.Start.equal(*tr.End):
		return tr.StartType == Exclusive || tr.EndType == Exclusive
	case tr.StartType == Exclusive && tr.EndType == Exclusive:
		return tr.End.equal(tr.Start.next())
	}
	return false
}

// IsEternity returns true if the range is unbounded on both ends.
func (tr TimeRange) IsEternity() bool {
	return tr.StartType == Unbounded && tr.EndType == Unbounded
}

// Contains returns true if ts falls within the timerange.
func (tr TimeRange) Contains(ts Timestamp) bool {
	if tr.IsEmpty() {
		return false
	}
	if tr.IsEternity() {
		return true
	}

	if tr.Start != nil {
		switch tr.StartType {
		case Inclusive:
			if ts.before(*tr.Start) {
				return false
			}
		case Exclusive:
			if ts.before(*tr.Start) || ts.equal(*tr.Start) {
				return false
			}
		}
	}

	if tr.End != nil {
		switch tr.EndType {
		case Inclusive:
			if tr.End.before(ts) {
				return false
			}
		case Exclusive:
			if tr.End.before(ts) || tr.End.equal(ts) {
				return false
			}
		}
	}

	return true
}

// Overlaps returns true if the two timeranges share at least one whole nanosecond.
func (tr TimeRange) Overlaps(other TimeRange) bool {
	return !Intersect(tr, other).IsEmpty()
}

// Intersect returns the intersection of two timeranges.
// Returns an empty timerange if they do not overlap.
func Intersect(a, b TimeRange) TimeRange {
	if a.IsEmpty() || b.IsEmpty() {
		return TimeRange{StartType: Exclusive, EndType: Exclusive}
	}
	if a.IsEternity() {
		return b
	}
	if b.IsEternity() {
		return a
	}

	// Lower bound: take the later (more restrictive) start.
	var start *Timestamp
	var startType BoundType
	switch {
	case a.StartType == Unbounded:
		start, startType = b.Start, b.StartType
	case b.StartType == Unbounded:
		start, startType = a.Start, a.StartType
	case a.Start.before(*b.Start):
		start, startType = b.Start, b.StartType
	case b.Start.before(*a.Start):
		start, startType = a.Start, a.StartType
	default: // equal timestamps — exclusive wins
		start = a.Start
		if a.StartType == Exclusive || b.StartType == Exclusive {
			startType = Exclusive
		} else {
			startType = Inclusive
		}
	}

	// Upper bound: take the earlier (more restrictive) end.
	var end *Timestamp
	var endType BoundType
	switch {
	case a.EndType == Unbounded:
		end, endType = b.End, b.EndType
	case b.EndType == Unbounded:
		end, endType = a.End, a.EndType
	case b.End.before(*a.End):
		end, endType = b.End, b.EndType
	case a.End.before(*b.End):
		end, endType = a.End, a.EndType
	default: // equal timestamps — exclusive wins
		end = a.End
		if a.EndType == Exclusive || b.EndType == Exclusive {
			endType = Exclusive
		} else {
			endType = Inclusive
		}
	}

	result := TimeRange{Start: start, StartType: startType, End: end, EndType: endType}
	if result.IsEmpty() {
		return TimeRange{StartType: Exclusive, EndType: Exclusive}
	}

	// Check start >= end (non-overlapping).
	if start != nil && end != nil {
		if end.before(*start) {
			return TimeRange{StartType: Exclusive, EndType: Exclusive}
		}
		if end.equal(*start) && (startType == Exclusive || endType == Exclusive) {
			return TimeRange{StartType: Exclusive, EndType: Exclusive}
		}
	}
	return result
}

// validInt returns true for strings matching (0|[1-9][0-9]*).
func validInt(s string) bool {
	if s == "" {
		return false
	}
	if s == "0" {
		return true
	}
	if s[0] < '1' || s[0] > '9' {
		return false
	}
	for _, c := range s[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// validNano returns true for strings matching (0|[1-9][0-9]{0,8}).
func validNano(s string) bool {
	if s == "" {
		return false
	}
	if s == "0" {
		return true
	}
	if s[0] < '1' || s[0] > '9' {
		return false
	}
	if len(s) > 9 {
		return false
	}
	for _, c := range s[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
