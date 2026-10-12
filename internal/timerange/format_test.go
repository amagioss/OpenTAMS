package timerange_test

import (
	"math"
	"testing"
	"time"

	"github.com/amagioss/opentams/internal/timerange"
)

func mustParse(t *testing.T, s string) timerange.TimeRange {
	t.Helper()
	tr, err := timerange.Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	return tr
}

func mustParseTimestamp(t *testing.T, s string) timerange.Timestamp {
	t.Helper()
	ts, err := timerange.ParseTimestamp(s)
	if err != nil {
		t.Fatalf("ParseTimestamp(%q): %v", s, err)
	}
	return ts
}

// BR-TR-05, BR-TR-06: forms the old parser rejected. The canonical string
// pins the parsed structure: markers, bound types and unbounded sides.
func TestParse_OmittedTimestampsAndMarkers(t *testing.T) {
	cases := []struct {
		input     string
		canonical string
	}{
		{"10:0", "[10:0]"},
		{"[10:0", "[10:0]"},
		{"10:0]", "[10:0]"},
		{"0:0_10:0", "[0:0_10:0]"},
		{"[0:0_10:0", "[0:0_10:0]"},
		{"0:0_10:0]", "[0:0_10:0]"},
		{"0:0_10:0)", "[0:0_10:0)"},
		{"(0:0_10:0", "(0:0_10:0]"},
		{"(5:0_", "(5:0_"},
		{"[5:0_", "[5:0_"},
		{"5:0_", "[5:0_"},
		{"(5:0_)", "(5:0_"},
		{"(5:0_]", "(5:0_"},
		{"_10:0)", "_10:0)"},
		{"_10:0]", "_10:0]"},
		{"_10:0", "_10:0]"},
		{"[_10:0)", "_10:0)"},
		{"(_10:0)", "_10:0)"},
		{"[_]", "_"},
		{"(_)", "_"},
		{"[_", "_"},
		{"_)", "_"},
		{"(-1:500000000_", "(-1:500000000_"},
	}
	for _, tc := range cases {
		tr, err := timerange.Parse(tc.input)
		if err != nil {
			t.Errorf("Parse(%q) unexpected error: %v", tc.input, err)
			continue
		}
		if got := tr.String(); got != tc.canonical {
			t.Errorf("Parse(%q).String() = %q, want %q", tc.input, got, tc.canonical)
		}
		if tr.IsEmpty() {
			t.Errorf("Parse(%q).IsEmpty() = true, want false", tc.input)
		}
	}
}

func TestParse_OneSidedBoundTypes(t *testing.T) {
	tr := mustParse(t, "(5:0_")
	if tr.Start == nil || tr.StartType != timerange.Exclusive {
		t.Errorf("(5:0_ start = %v/%v, want 5:0 exclusive", tr.Start, tr.StartType)
	}
	if tr.End != nil || tr.EndType != timerange.Unbounded {
		t.Errorf("(5:0_ end = %v/%v, want nil unbounded", tr.End, tr.EndType)
	}

	tr = mustParse(t, "[_10:0)")
	if tr.Start != nil || tr.StartType != timerange.Unbounded {
		t.Errorf("[_10:0) start = %v/%v, want nil unbounded (marker ignored)", tr.Start, tr.StartType)
	}
	if tr.End == nil || tr.EndType != timerange.Exclusive {
		t.Errorf("[_10:0) end = %v/%v, want 10:0 exclusive", tr.End, tr.EndType)
	}
}

// BR-TR-12: a leading "-" only for a value below zero, and the magnitude
// written as seconds:nanoseconds.
func TestTimestampString(t *testing.T) {
	cases := []struct {
		ts   timerange.Timestamp
		want string
	}{
		{timerange.Timestamp{Seconds: 0, Nanoseconds: 0}, "0:0"},
		{timerange.Timestamp{Seconds: 10, Nanoseconds: 500000000}, "10:500000000"},
		{timerange.Timestamp{Seconds: -1, Nanoseconds: 0}, "-1:0"},
		{timerange.Timestamp{Seconds: -2, Nanoseconds: 500000000}, "-1:500000000"},
		{timerange.Timestamp{Seconds: -1, Nanoseconds: 500000000}, "-0:500000000"},
		{timerange.Timestamp{Seconds: -1, Nanoseconds: 999999999}, "-0:1"},
		{timerange.Timestamp{Seconds: math.MinInt64, Nanoseconds: 0}, "-9223372036854775808:0"},
		{timerange.Timestamp{Seconds: math.MinInt64, Nanoseconds: 1}, "-9223372036854775807:999999999"},
		{timerange.Timestamp{Seconds: math.MaxInt64, Nanoseconds: 999999999}, "9223372036854775807:999999999"},
	}
	for _, tc := range cases {
		if got := tc.ts.String(); got != tc.want {
			t.Errorf("%+v.String() = %q, want %q", tc.ts, got, tc.want)
		}
	}
}

// BR-TR-01, BR-TR-12: parsing then rendering keeps the value. -0:0 is the
// one input that changes text, because String never writes "-0:0".
func TestTimestamp_ParseStringRoundTrip(t *testing.T) {
	cases := map[string]string{
		"-1:500000000":           "-1:500000000",
		"-0:500000000":           "-0:500000000",
		"-0:1":                   "-0:1",
		"-0:0":                   "0:0",
		"-9223372036854775808:0": "-9223372036854775808:0",
		"-9223372036:854775808":  "-9223372036:854775808",
	}
	for in, want := range cases {
		if got := mustParseTimestamp(t, in).String(); got != want {
			t.Errorf("ParseTimestamp(%q).String() = %q, want %q", in, got, want)
		}
	}
}

// BR-TR-01: the old parser read -1:500000000 as -0.5 s, which put it after
// -1:0 and inverted the order of timestamps inside a negative second.
func TestNegativeTimestampOrdering(t *testing.T) {
	tr := mustParse(t, "[-1:500000000_-1:0)")
	if tr.IsEmpty() {
		t.Fatal("[-1:500000000_-1:0) is -1.5 s to -1 s and must not be empty")
	}
	if !tr.Contains(mustParseTimestamp(t, "-1:200000000")) {
		t.Error("[-1:500000000_-1:0) must contain -1.2 s")
	}
	if tr.Contains(mustParseTimestamp(t, "-0:500000000")) {
		t.Error("[-1:500000000_-1:0) must not contain -0.5 s")
	}
	if !mustParse(t, "[-1:0_-0:500000000)").Overlaps(mustParse(t, "[-0:600000000_0:0)")) {
		t.Error("[-1:0_-0:500000000) and [-0:600000000_0:0) share -0.6 s to -0.5 s")
	}
}

func TestOverlaps_OneSided(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"(5:0_", "[0:0_5:0]", false},
		{"[5:0_", "[0:0_5:0]", true},
		{"(5:0_", "[0:0_5:0)", false},
		{"(5:0_", "[0:0_5:1)", false}, // shares no whole nanosecond (ADR-0038 rule 10)
		{"(5:0_", "[0:0_5:1]", true},
		{"(5:0_", "[0:0_5:2)", true},
		{"_10:0)", "[10:0_20:0)", false},
		{"_10:0]", "[10:0_20:0)", true},
		{"_10:0)", "(5:0_", true},
		{"_5:0)", "[5:0_", false},
		{"_5:0]", "[5:0_", true},
		{"_5:0]", "(5:0_", false},
		{"(5:0_", "_", true},
		{"(5:0_", "()", false},
		{"(5:0_", "[10:0_5:0)", false},
	}
	for _, tc := range cases {
		a, b := mustParse(t, tc.a), mustParse(t, tc.b)
		if got := a.Overlaps(b); got != tc.want {
			t.Errorf("%q.Overlaps(%q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
		if got := b.Overlaps(a); got != tc.want {
			t.Errorf("%q.Overlaps(%q) = %v, want %v (symmetry)", tc.b, tc.a, got, tc.want)
		}
	}
}

func TestContains_OneSidedAndReversed(t *testing.T) {
	cases := []struct {
		tr, ts string
		want   bool
	}{
		{"(5:0_", "5:0", false},
		{"(5:0_", "5:1", true},
		{"(5:0_", "9223372036854775807:999999999", true},
		{"_10:0)", "10:0", false},
		{"_10:0)", "-9223372036854775808:0", true},
		{"[10:0_5:0)", "7:0", false},
		{"[10:0_5:0]", "10:0", false},
	}
	for _, tc := range cases {
		if got := mustParse(t, tc.tr).Contains(mustParseTimestamp(t, tc.ts)); got != tc.want {
			t.Errorf("%q.Contains(%q) = %v, want %v", tc.tr, tc.ts, got, tc.want)
		}
	}
}

func TestIntersect_OneSided(t *testing.T) {
	cases := []struct {
		a, b, want string
	}{
		{"[0:0_", "_10:0)", "[0:0_10:0)"},
		{"(5:0_", "[0:0_10:0)", "(5:0_10:0)"},
		{"(5:0_", "[7:0_", "[7:0_"},
		{"_5:0)", "_3:0]", "_3:0]"},
		{"(5:0_", "_5:0]", "()"},
		{"[5:0_", "_5:0]", "[5:0]"},
		{"[10:0_5:0)", "_", "()"},
	}
	for _, tc := range cases {
		got := timerange.Intersect(mustParse(t, tc.a), mustParse(t, tc.b)).String()
		if got != tc.want {
			t.Errorf("Intersect(%q, %q) = %q, want %q", tc.a, tc.b, got, tc.want)
		}
	}
}

// BR-TR-04: an empty range contains nothing and overlaps nothing, itself included.
func TestEmptyRanges_MatchNothing(t *testing.T) {
	for _, s := range []string{"()", "[]", "[5:0_5:0)", "(5:0_5:0]", "[10:0_5:0)", "[10:0_5:0]"} {
		tr := mustParse(t, s)
		if !tr.IsEmpty() {
			t.Errorf("Parse(%q).IsEmpty() = false, want true", s)
		}
		if tr.Overlaps(tr) {
			t.Errorf("Parse(%q) overlaps itself", s)
		}
		if tr.Overlaps(mustParse(t, "_")) {
			t.Errorf("Parse(%q) overlaps eternity", s)
		}
		if tr.Contains(mustParseTimestamp(t, "5:0")) || tr.Contains(mustParseTimestamp(t, "10:0")) {
			t.Errorf("Parse(%q) contains a timestamp", s)
		}
	}
}

// BR-TR-14: durations use the Timestamp type, floor-normalised.
func TestTimestampFromDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want timerange.Timestamp
		str  string
	}{
		{0, timerange.Timestamp{}, "0:0"},
		{time.Hour, timerange.Timestamp{Seconds: 3600}, "3600:0"},
		{1500 * time.Millisecond, timerange.Timestamp{Seconds: 1, Nanoseconds: 500000000}, "1:500000000"},
		{time.Nanosecond, timerange.Timestamp{Nanoseconds: 1}, "0:1"},
		{-time.Nanosecond, timerange.Timestamp{Seconds: -1, Nanoseconds: 999999999}, "-0:1"},
		{-1500 * time.Millisecond, timerange.Timestamp{Seconds: -2, Nanoseconds: 500000000}, "-1:500000000"},
		{-time.Second, timerange.Timestamp{Seconds: -1}, "-1:0"},
		{math.MaxInt64, timerange.Timestamp{Seconds: 9223372036, Nanoseconds: 854775807}, "9223372036:854775807"},
		{math.MinInt64, timerange.Timestamp{Seconds: -9223372037, Nanoseconds: 145224192}, "-9223372036:854775808"},
	}
	for _, tc := range cases {
		got := timerange.TimestampFromDuration(tc.d)
		if got != tc.want {
			t.Errorf("TimestampFromDuration(%d) = %+v, want %+v", tc.d, got, tc.want)
		}
		if s := got.String(); s != tc.str {
			t.Errorf("TimestampFromDuration(%d).String() = %q, want %q", tc.d, s, tc.str)
		}
	}
}
