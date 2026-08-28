package timerange

import (
	"fmt"
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
func ParseTimestamp(s string) (Timestamp, error) {
	neg := false
	rest := s
	if strings.HasPrefix(s, "-") {
		neg = true
		rest = s[1:]
	}

	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 {
		return Timestamp{}, fmt.Errorf("invalid timestamp %q: expected seconds:nanoseconds", s)
	}

	secStr, nsStr := parts[0], parts[1]

	if !validInt(secStr) {
		return Timestamp{}, fmt.Errorf("invalid timestamp %q: bad seconds field", s)
	}
	if !validNano(nsStr) {
		return Timestamp{}, fmt.Errorf("invalid timestamp %q: bad nanoseconds field", s)
	}
	if neg && secStr == "0" {
		return Timestamp{}, fmt.Errorf("invalid timestamp %q: negative zero", s)
	}

	sec, err := strconv.ParseInt(secStr, 10, 64)
	if err != nil {
		return Timestamp{}, fmt.Errorf("invalid timestamp %q: seconds out of range", s)
	}
	// validNano guarantees nsStr is valid digits ≤ 999_999_999, so ParseInt cannot fail here.
	ns, _ := strconv.ParseInt(nsStr, 10, 64)

	if neg {
		sec = -sec
	}
	// validNano above bounds ns to [0, 999_999_999] — far below MaxInt32 — so the
	// int32 cast cannot overflow. Annotated to keep gosec G115 quiet.
	return Timestamp{Seconds: sec, Nanoseconds: int32(ns)}, nil //nolint:gosec // bounded by validNano above
}

// String returns the canonical timestamp string.
func (ts Timestamp) String() string {
	if ts.Seconds < 0 {
		return fmt.Sprintf("-%d:%d", -ts.Seconds, ts.Nanoseconds)
	}
	return fmt.Sprintf("%d:%d", ts.Seconds, ts.Nanoseconds)
}

// before returns true if ts is strictly before other.
func (ts Timestamp) before(other Timestamp) bool {
	if ts.Seconds != other.Seconds {
		return ts.Seconds < other.Seconds
	}
	return ts.Nanoseconds < other.Nanoseconds
}

// equal returns true if ts == other.
func (ts Timestamp) equal(other Timestamp) bool {
	return ts.Seconds == other.Seconds && ts.Nanoseconds == other.Nanoseconds
}

// Parse parses a TAMS bracket-notation timerange string.
//
// Valid forms:
//
//	_              eternity (unbounded both ends)
//	()             never (canonical empty)
//	[ts_ts)        half-open
//	[ts_ts]        closed
//	(ts_ts)        open
//	(ts_ts]        half-open reversed
//	[ts]           instantaneous — expands to [ts_ts]
//	[ts_ts)        where end==start with exclusive bound — valid empty range
//
// Returns an error if end is strictly before start.
func Parse(s string) (TimeRange, error) {
	if s == "_" {
		return TimeRange{StartType: Unbounded, EndType: Unbounded}, nil
	}
	if s == "()" {
		return TimeRange{StartType: Exclusive, EndType: Exclusive}, nil
	}
	if len(s) < 2 {
		return TimeRange{}, fmt.Errorf("invalid timerange %q", s)
	}

	startBracket := s[0]
	endBracket := s[len(s)-1]

	if (startBracket != '[' && startBracket != '(') ||
		(endBracket != ']' && endBracket != ')') {
		return TimeRange{}, fmt.Errorf("invalid timerange %q: bad brackets", s)
	}

	inner := s[1 : len(s)-1]

	var startStr, endStr string
	idx := strings.Index(inner, "_")
	if idx == -1 {
		// instantaneous: [ts] — no underscore, treat as [ts_ts]
		if startBracket != '[' || endBracket != ']' {
			return TimeRange{}, fmt.Errorf("invalid timerange %q: instantaneous form requires [] brackets", s)
		}
		startStr = inner
		endStr = inner
	} else {
		startStr = inner[:idx]
		endStr = inner[idx+1:]
	}

	start, err := ParseTimestamp(startStr)
	if err != nil {
		return TimeRange{}, fmt.Errorf("invalid timerange %q: %w", s, err)
	}
	end, err := ParseTimestamp(endStr)
	if err != nil {
		return TimeRange{}, fmt.Errorf("invalid timerange %q: %w", s, err)
	}

	// end strictly before start is invalid
	if end.before(start) {
		return TimeRange{}, fmt.Errorf("invalid timerange %q: end is before start", s)
	}

	startType := Inclusive
	if startBracket == '(' {
		startType = Exclusive
	}
	endType := Inclusive
	if endBracket == ')' {
		endType = Exclusive
	}

	return TimeRange{
		Start:     &start,
		StartType: startType,
		End:       &end,
		EndType:   endType,
	}, nil
}

// String returns the canonical bracket-notation string for the timerange.
func (tr TimeRange) String() string {
	if tr.StartType == Unbounded && tr.EndType == Unbounded {
		return "_"
	}
	if tr.Start == nil && tr.End == nil {
		return "()"
	}
	if tr.Start == nil || tr.End == nil {
		panic("timerange: malformed TimeRange: exactly one bound is nil")
	}

	openBracket := "["
	if tr.StartType == Exclusive {
		openBracket = "("
	}
	closeBracket := "]"
	if tr.EndType == Exclusive {
		closeBracket = ")"
	}
	return openBracket + tr.Start.String() + "_" + tr.End.String() + closeBracket
}

// IsEmpty returns true if the range contains no points.
func (tr TimeRange) IsEmpty() bool {
	if tr.StartType == Unbounded || tr.EndType == Unbounded {
		return false
	}
	if tr.Start == nil && tr.End == nil {
		// canonical never ()
		return true
	}
	if tr.Start.equal(*tr.End) {
		return tr.StartType == Exclusive || tr.EndType == Exclusive
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

// Overlaps returns true if the two timeranges share at least one point.
func (tr TimeRange) Overlaps(other TimeRange) bool {
	if tr.IsEmpty() || other.IsEmpty() {
		return false
	}
	if tr.IsEternity() || other.IsEternity() {
		return true
	}

	// tr starts after other ends?
	if tr.Start != nil && other.End != nil {
		if other.End.before(*tr.Start) {
			return false
		}
		if other.End.equal(*tr.Start) && (tr.StartType == Exclusive || other.EndType == Exclusive) {
			return false
		}
	}

	// other starts after tr ends?
	if other.Start != nil && tr.End != nil {
		if tr.End.before(*other.Start) {
			return false
		}
		if tr.End.equal(*other.Start) && (other.StartType == Exclusive || tr.EndType == Exclusive) {
			return false
		}
	}

	return true
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
