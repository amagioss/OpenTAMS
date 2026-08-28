package timerange_test

import (
	"testing"

	"github.com/amagioss/opentams/internal/timerange"
)

// --- TC-TR-01: Timestamp parsing ---

func TestParseTimestamp_Valid(t *testing.T) {
	cases := []struct {
		input string
		wantS int64
		wantN int32
	}{
		{"0:0", 0, 0},
		{"1:0", 1, 0},
		{"10:500000000", 10, 500000000},
		{"0:999999999", 0, 999999999}, // max valid nanoseconds
		{"1000:0", 1000, 0},
		{"-1:0", -1, 0},
		{"-10:500000000", -10, 500000000},
	}
	for _, tc := range cases {
		ts, err := timerange.ParseTimestamp(tc.input)
		if err != nil {
			t.Errorf("ParseTimestamp(%q) unexpected error: %v", tc.input, err)
			continue
		}
		if ts.Seconds != tc.wantS || ts.Nanoseconds != tc.wantN {
			t.Errorf("ParseTimestamp(%q) = {%d, %d}, want {%d, %d}",
				tc.input, ts.Seconds, ts.Nanoseconds, tc.wantS, tc.wantN)
		}
	}
}

func TestParseTimestamp_Invalid(t *testing.T) {
	cases := []string{
		"",
		"abc",
		"0",
		":0",
		"0:",
		"0:1000000000",          // nanoseconds >= 1e9
		"9999999999999999999:0", // seconds overflows int64
		"-0:0",                  // negative zero — cannot round-trip
		"-0:500000000",          // negative zero seconds — cannot round-trip
		"1a0:0",                 // non-digit in seconds
		"0:1a0",                 // non-digit in nanoseconds
		"0:-1",
		"01:0",  // leading zero on seconds
		"0:01",  // leading zero on nanoseconds
		"- 1:0", // space
	}
	for _, s := range cases {
		if _, err := timerange.ParseTimestamp(s); err == nil {
			t.Errorf("ParseTimestamp(%q) expected error, got nil", s)
		}
	}
}

// --- TC-TR-01b: String panics on malformed zero-value TimeRange ---

func TestString_MalformedPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("String() on half-nil TimeRange should panic")
		}
	}()
	ts, _ := timerange.ParseTimestamp("1:0")
	// Start set, End nil — malformed state that Parse never produces
	tr := timerange.TimeRange{Start: &ts, StartType: timerange.Inclusive, EndType: timerange.Inclusive}
	_ = tr.String()
}

// --- TC-TR-02: TimeRange parsing — valid ---

func TestParse_Valid(t *testing.T) {
	cases := []struct {
		input      string
		wantStr    string // round-trip canonical form
		isEmpty    bool
		isEternity bool
	}{
		{"_", "_", false, true},
		{"()", "()", true, false},
		{"[0:0_10:0)", "[0:0_10:0)", false, false},
		{"[0:0_10:0]", "[0:0_10:0]", false, false},
		{"(0:0_10:0)", "(0:0_10:0)", false, false},
		{"(0:0_10:0]", "(0:0_10:0]", false, false},
		{"[1:0]", "[1:0_1:0]", false, false}, // instantaneous — canonical form expands it
		{"[-10:0_0:0)", "[-10:0_0:0)", false, false},
		{"[0:0_0:500000000)", "[0:0_0:500000000)", false, false},
		{"[0:400000000_1:0)", "[0:400000000_1:0)", false, false},
		{"[0:0_0:0)", "[0:0_0:0)", true, false}, // end==start, exclusive end → valid empty
		{"[5:0_5:0)", "[5:0_5:0)", true, false}, // end==start, exclusive end → valid empty
		{"(5:0_5:0)", "(5:0_5:0)", true, false}, // both exclusive, same point → valid empty
		{"(5:0_5:0]", "(5:0_5:0]", true, false}, // exclusive start, same point → valid empty
	}
	for _, tc := range cases {
		tr, err := timerange.Parse(tc.input)
		if err != nil {
			t.Errorf("Parse(%q) unexpected error: %v", tc.input, err)
			continue
		}
		if got := tr.String(); got != tc.wantStr {
			t.Errorf("Parse(%q).String() = %q, want %q", tc.input, got, tc.wantStr)
		}
		if got := tr.IsEmpty(); got != tc.isEmpty {
			t.Errorf("Parse(%q).IsEmpty() = %v, want %v", tc.input, got, tc.isEmpty)
		}
		if got := tr.IsEternity(); got != tc.isEternity {
			t.Errorf("Parse(%q).IsEternity() = %v, want %v", tc.input, got, tc.isEternity)
		}
	}
}

// --- TC-TR-03: TimeRange parsing — invalid ---

func TestParse_Invalid(t *testing.T) {
	cases := []string{
		"",
		"0:0_10:0",           // missing brackets
		"[0:0_10:0",          // missing closing bracket
		"0:0_10:0]",          // missing opening bracket
		"[10:0_5:0)",         // end strictly before start
		"[abc_10:0)",         // invalid timestamp
		"[0:0_xyz]",          // invalid timestamp
		"[0:1000000000_1:0)", // nanoseconds out of range
		"(1:0)",              // instantaneous form requires [] brackets
		"(1:0]",              // instantaneous form requires [] brackets
	}
	for _, s := range cases {
		if _, err := timerange.Parse(s); err == nil {
			t.Errorf("Parse(%q) expected error, got nil", s)
		}
	}
}

// --- TC-TR-04: String round-trip ---

func TestString_RoundTrip(t *testing.T) {
	cases := []string{
		"_",
		"()",
		"[0:0_10:0)",
		"[0:0_10:0]",
		"(0:0_10:0)",
		"(0:0_10:0]",
		"[-10:0_0:0)",
		"[0:0_0:500000000)",
	}
	for _, s := range cases {
		tr, err := timerange.Parse(s)
		if err != nil {
			t.Fatalf("Parse(%q): %v", s, err)
		}
		if got := tr.String(); got != s {
			t.Errorf("Parse(%q).String() = %q, want %q", s, got, s)
		}
	}
}

// --- TC-TR-05: Overlaps ---

func TestOverlaps(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		// non-overlapping — gap between ranges
		{"[0:0_3:0)", "[5:0_10:0)", false}, // tr ends strictly before other starts
		{"[5:0_10:0)", "[0:0_3:0)", false}, // other ends strictly before tr starts

		// non-overlapping boundary cases
		{"[0:0_5:0)", "[5:0_10:0)", false}, // exclusive end meets inclusive start — no overlap
		{"(0:0_5:0)", "[5:0_10:0)", false}, // exclusive end meets inclusive start — no overlap

		// overlapping boundary cases
		{"[0:0_5:0]", "[5:0_10:0)", true},  // inclusive end meets inclusive start — overlap at 5:0
		{"[0:0_5:0)", "[3:0_8:0)", true},   // standard interior overlap
		{"[0:0_10:0)", "[0:0_10:0)", true}, // same range

		// sub-second / nanosecond precision
		{"[0:0_5:0)", "[0:4_10:0)", true},                 // nanosecond-level overlap
		{"[0:0_0:500000000)", "[0:400000000_1:0)", true},  // sub-second overlap
		{"[0:0_0:400000000)", "[0:400000000_1:0)", false}, // exclusive boundary at nanosecond level
		{"[0:0_0:400000001)", "[0:400000000_1:0)", true},  // one nanosecond of overlap

		// instantaneous
		{"[5:0_5:0]", "[5:0_5:0]", true},   // same point
		{"[5:0_5:0]", "[5:0_10:0)", true},  // instantaneous at start of range
		{"[5:0_5:0]", "(5:0_10:0)", false}, // instantaneous at exclusive start — no overlap

		// never — canonical and degenerate forms
		{"()", "[0:0_10:0)", false},
		{"()", "_", false},
		{"()", "()", false},
		{"[5:0_5:0)", "[0:0_10:0)", false}, // degenerate empty overlaps nothing
		{"[5:0_5:0)", "[5:0_5:0]", false},  // degenerate empty vs instantaneous at same point

		// eternity
		{"_", "[0:0_10:0)", true},
		{"_", "_", true},
		{"_", "()", false},

		// negative timestamps
		{"[-10:0_-5:0)", "[-7:0_0:0)", true},
		{"[-10:0_-5:0)", "[-5:0_0:0)", false}, // exclusive boundary
		{"[-10:0_-5:0]", "[-5:0_0:0)", true},  // inclusive boundary
	}
	for _, tc := range cases {
		a, err := timerange.Parse(tc.a)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.a, err)
		}
		b, err := timerange.Parse(tc.b)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.b, err)
		}
		if got := a.Overlaps(b); got != tc.want {
			t.Errorf("Parse(%q).Overlaps(Parse(%q)) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
		// Overlaps must be symmetric
		if got := b.Overlaps(a); got != tc.want {
			t.Errorf("Parse(%q).Overlaps(Parse(%q)) = %v, want %v (symmetry check)", tc.b, tc.a, got, tc.want)
		}
	}
}

// --- TC-TR-07: Intersect ---

func TestIntersect(t *testing.T) {
	cases := []struct {
		a, b string
		want string
	}{
		// Overlapping half-open ranges
		{"[0:0_10:0)", "[5:0_15:0)", "[5:0_10:0)"},
		// One contains the other
		{"[0:0_10:0)", "[2:0_8:0)", "[2:0_8:0)"},
		{"[2:0_8:0)", "[0:0_10:0)", "[2:0_8:0)"},
		// Adjacent (exclusive end meets inclusive start) — no overlap
		{"[0:0_5:0)", "[5:0_10:0)", "()"},
		// Non-overlapping
		{"[0:0_3:0)", "[5:0_10:0)", "()"},
		// Eternity intersect range = range
		{"_", "[5:0_15:0)", "[5:0_15:0)"},
		{"[5:0_15:0)", "_", "[5:0_15:0)"},
		// Eternity intersect eternity = eternity
		{"_", "_", "_"},
		// Empty intersect anything = empty
		{"()", "[0:0_10:0)", "()"},
		{"[0:0_10:0)", "()", "()"},
		// Exact same range
		{"[0:0_10:0)", "[0:0_10:0)", "[0:0_10:0)"},
		// Mixed bound types: exclusive start
		{"(0:0_10:0)", "[0:0_5:0)", "(0:0_5:0)"},
		// Inclusive end
		{"[0:0_10:0]", "[5:0_15:0)", "[5:0_10:0]"},
	}
	for _, tc := range cases {
		a, err := timerange.Parse(tc.a)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.a, err)
		}
		b, err := timerange.Parse(tc.b)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.b, err)
		}
		want, err := timerange.Parse(tc.want)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.want, err)
		}
		got := timerange.Intersect(a, b)
		if got.String() != want.String() {
			t.Errorf("Intersect(%q, %q) = %q, want %q", tc.a, tc.b, got.String(), want.String())
		}
	}
}

// --- TC-TR-06: Contains ---

func TestContains(t *testing.T) {
	cases := []struct {
		tr   string
		ts   string
		want bool
	}{
		{"[0:0_10:0)", "5:0", true},
		{"[0:0_10:0)", "0:0", true},   // inclusive start
		{"[5:0_10:0)", "3:0", false},  // ts strictly before inclusive start
		{"[0:0_10:0)", "10:0", false}, // exclusive end
		{"[0:0_10:0]", "10:0", true},  // inclusive end
		{"(0:0_10:0)", "0:0", false},  // exclusive start, ts at boundary
		{"(5:0_10:0)", "3:0", false},  // exclusive start, ts strictly before start
		{"(0:0_10:0)", "5:0", true},
		{"()", "5:0", false},                        // never contains nothing
		{"_", "5:0", true},                          // eternity contains everything
		{"_", "-999:0", true},                       // eternity, negative
		{"[5:0_5:0]", "5:0", true},                  // instantaneous contains its own point
		{"[5:0_5:0]", "5:1", false},                 // instantaneous, nanosecond off
		{"[0:0_0:500000000)", "0:499999999", true},  // nanosecond before exclusive end
		{"[0:0_0:500000000)", "0:500000000", false}, // at exclusive end
	}
	for _, tc := range cases {
		tr, err := timerange.Parse(tc.tr)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.tr, err)
		}
		ts, err := timerange.ParseTimestamp(tc.ts)
		if err != nil {
			t.Fatalf("ParseTimestamp(%q): %v", tc.ts, err)
		}
		if got := tr.Contains(ts); got != tc.want {
			t.Errorf("Parse(%q).Contains(%q) = %v, want %v", tc.tr, tc.ts, got, tc.want)
		}
	}
}
