package timerange_test

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"testing"

	"github.com/amagioss/opentams/internal/timerange"
)

const sec = int64(1_000_000_000)

func bounded(lower, upper int64) timerange.NsBounds {
	return timerange.NsBounds{Lower: lower, Upper: upper}
}

// BR-TR-10: the marker table, the original [0:0] bug, negative values inside
// one second, and both int64 edges.
func TestNsBounds(t *testing.T) {
	cases := []struct {
		input string
		want  timerange.NsBounds
	}{
		// The bug report: [0:0] became lower_ns = upper_ns = 0.
		{"[0:0]", bounded(0, 1)},
		{"10:0", bounded(10*sec, 10*sec+1)},
		{"[-0:0]", bounded(0, 1)},
		{"[-1:500000000]", bounded(-1_500_000_000, -1_499_999_999)},
		{"[-0:500000000]", bounded(-500_000_000, -499_999_999)},
		{"[-1:500000000_-1:0)", bounded(-1_500_000_000, -sec)},

		// Marker table.
		{"[0:0_10:0)", bounded(0, 10*sec)},
		{"[0:0_10:0]", bounded(0, 10*sec+1)},
		{"(0:0_10:0)", bounded(1, 10*sec)},
		{"(0:0_10:0]", bounded(1, 10*sec+1)},
		{"0:0_10:0", bounded(0, 10*sec+1)},

		// Unbounded sides are flags, not sentinels.
		{"(5:0_", timerange.NsBounds{Lower: 5*sec + 1, UpperUnbounded: true}},
		{"[5:0_", timerange.NsBounds{Lower: 5 * sec, UpperUnbounded: true}},
		{"_10:0)", timerange.NsBounds{LowerUnbounded: true, Upper: 10 * sec}},
		{"[_10:0)", timerange.NsBounds{LowerUnbounded: true, Upper: 10 * sec}},
		{"_10:0]", timerange.NsBounds{LowerUnbounded: true, Upper: 10*sec + 1}},
		{"_", timerange.NsBounds{LowerUnbounded: true, UpperUnbounded: true}},

		// int64 edges that still fit.
		{"[0:0_9223372036:854775807)", bounded(0, math.MaxInt64)},
		{"[-9223372036:854775808_0:0)", bounded(math.MinInt64, 0)},
		{"[9223372036:854775806]", bounded(math.MaxInt64-1, math.MaxInt64)},
		{"(-9223372036:854775808_0:0)", bounded(math.MinInt64+1, 0)},
		{"(9223372036:854775806_", timerange.NsBounds{Lower: math.MaxInt64, UpperUnbounded: true}},
		{"_-9223372036:854775808]", timerange.NsBounds{LowerUnbounded: true, Upper: math.MinInt64 + 1}},
	}
	for _, tc := range cases {
		got, err := mustParse(t, tc.input).NsBounds()
		if err != nil {
			t.Errorf("Parse(%q).NsBounds() unexpected error: %v", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Parse(%q).NsBounds() = %+v, want %+v", tc.input, got, tc.want)
		}
	}
}

// BR-TR-10, BR-TR-11: a finite bound outside int64 nanoseconds, after the
// marker adjustment, is ErrOutOfRange. It never wraps.
func TestNsBounds_OutOfRange(t *testing.T) {
	cases := []string{
		"[9223372036:854775807]",      // inclusive end: +1 overflows
		"(9223372036:854775807_",      // exclusive start: +1 overflows
		"[9223372036:854775808_",      // seconds × 10⁹ + ns overflows
		"_9223372036:854775808)",      // exclusive end past MaxInt64
		"[-9223372036:854775809_0:0)", // below MinInt64
		"_-9223372036:854775809]",     // below MinInt64 even after +1
		"[9223372037:0_",              // seconds × 10⁹ overflows
		"[-9223372037:0_0:0)",         // seconds × 10⁹ underflows
		"[9223372036854775807:0_",     // seconds far out of range
		"[0:0_9223372036854775807:999999999]",
	}
	for _, s := range cases {
		b, err := mustParse(t, s).NsBounds()
		if !errors.Is(err, timerange.ErrOutOfRange) {
			t.Errorf("Parse(%q).NsBounds() = %+v, %v; want ErrOutOfRange", s, b, err)
		}
		if errors.Is(err, timerange.ErrEmptyRange) {
			t.Errorf("Parse(%q).NsBounds() error matches ErrEmptyRange too", s)
		}
	}
}

// BR-TR-04, BR-TR-10: an empty range has no bounds.
func TestNsBounds_Empty(t *testing.T) {
	for _, s := range []string{"()", "[]", "[5:0_5:0)", "(5:0_5:0]", "(5:0_5:0)", "[10:0_5:0)", "[10:0_5:0]"} {
		_, err := mustParse(t, s).NsBounds()
		if !errors.Is(err, timerange.ErrEmptyRange) {
			t.Errorf("Parse(%q).NsBounds() error = %v, want ErrEmptyRange", s, err)
		}
	}
}

// Every non-empty range gives Lower < Upper, which is what the
// segments_bounds_nonempty constraint (upper_ns > lower_ns) requires.
func TestNsBounds_NonEmptyIsStrictlyIncreasing(t *testing.T) {
	for _, s := range []string{"[0:0]", "[-0:0]", "[-1:500000000]", "(0:0_0:2)", "(0:0_0:1]", "[0:0_0:1)"} {
		b, err := mustParse(t, s).NsBounds()
		if err != nil {
			t.Fatalf("Parse(%q).NsBounds(): %v", s, err)
		}
		if b.Lower >= b.Upper {
			t.Errorf("Parse(%q).NsBounds() = %+v, want Lower < Upper", s, b)
		}
	}
}

// ADR-0038 rule 10: an instant is a whole nanosecond. A range with no whole
// nanosecond is empty, and ranges that share only a sub-nanosecond gap do not
// overlap. mediatimestamp treats time as continuous and disagrees on both.
func TestWholeNanoseconds(t *testing.T) {
	for _, s := range []string{"(0:0_0:1)", "(-0:1_0:0)", "(-1:750000000_-1:749999999)", "(9223372036:854775806_9223372036:854775807)"} {
		tr := mustParse(t, s)
		if !tr.IsEmpty() {
			t.Errorf("Parse(%q).IsEmpty() = false, want true: it contains no whole nanosecond", s)
		}
		if _, err := tr.NsBounds(); !errors.Is(err, timerange.ErrEmptyRange) {
			t.Errorf("Parse(%q).NsBounds() error = %v, want ErrEmptyRange", s, err)
		}
		if got := tr.String(); got != "()" {
			t.Errorf("Parse(%q).String() = %q, want ()", s, got)
		}
	}

	narrowest := mustParse(t, "(0:0_0:2)")
	if b, err := narrowest.NsBounds(); err != nil || b != bounded(1, 2) {
		t.Errorf("(0:0_0:2).NsBounds() = %+v, %v; want [1, 2)", b, err)
	}
	if !narrowest.Contains(mustParseTimestamp(t, "0:1")) {
		t.Error("(0:0_0:2) must contain 0:1")
	}

	disjoint := []struct{ a, b string }{
		{"_0:1)", "(0:0_"},
		{"_-1:749999999)", "(-1:750000000_-1:250000000)"},
		{"[0:0_0:1)", "(0:0_0:1]"},
		{"(0:0_0:1)", "[0:0_0:1]"},
	}
	for _, tc := range disjoint {
		a, b := mustParse(t, tc.a), mustParse(t, tc.b)
		if a.Overlaps(b) || b.Overlaps(a) {
			t.Errorf("%q and %q share no whole nanosecond and must not overlap", tc.a, tc.b)
		}
		if got := timerange.Intersect(a, b).String(); got != "()" {
			t.Errorf("Intersect(%q, %q) = %q, want ()", tc.a, tc.b, got)
		}
	}
}

// The spec example that motivates half-open bounds: [1:0_2:0) followed by
// [2:0] do not overlap, in the parser and in the bounds.
func TestNsBounds_AdjacentSpecExample(t *testing.T) {
	a, b := mustParse(t, "[1:0_2:0)"), mustParse(t, "[2:0]")
	if a.Overlaps(b) {
		t.Error("[1:0_2:0) overlaps [2:0]")
	}
	ab, _ := a.NsBounds()
	bb, _ := b.NsBounds()
	if boundsOverlap(ab, bb) {
		t.Errorf("bounds %+v and %+v overlap", ab, bb)
	}
}

// BR-TR-07 as a property: Overlaps agrees with the half-open bounds
// comparison for every pair, including empty, one-sided and negative ranges.
func TestOverlaps_AgreesWithNsBounds(t *testing.T) {
	rng := rand.New(rand.NewSource(20260928)) //nolint:gosec // deterministic test data
	for i := 0; i < 50_000; i++ {
		as, bs := randomRange(rng), randomRange(rng)
		a, err := timerange.Parse(as)
		if err != nil {
			t.Fatalf("Parse(%q): %v", as, err)
		}
		b, err := timerange.Parse(bs)
		if err != nil {
			t.Fatalf("Parse(%q): %v", bs, err)
		}
		ab, aerr := a.NsBounds()
		bb, berr := b.NsBounds()
		if errors.Is(aerr, timerange.ErrOutOfRange) || errors.Is(berr, timerange.ErrOutOfRange) {
			continue
		}
		if (aerr != nil) != a.IsEmpty() || (berr != nil) != b.IsEmpty() {
			t.Fatalf("IsEmpty and ErrEmptyRange disagree for %q (%v) or %q (%v)", as, aerr, bs, berr)
		}
		want := aerr == nil && berr == nil && boundsOverlap(ab, bb)
		if got := a.Overlaps(b); got != want {
			t.Fatalf("%q.Overlaps(%q) = %v, bounds say %v (%+v, %+v)", as, bs, got, want, ab, bb)
		}
	}
}

func boundsOverlap(a, b timerange.NsBounds) bool {
	return (a.LowerUnbounded || b.UpperUnbounded || a.Lower < b.Upper) &&
		(b.LowerUnbounded || a.UpperUnbounded || b.Lower < a.Upper)
}

// randomRange draws timestamps from a small window around zero, so equal
// timestamps, sub-second negatives and touching bounds occur often.
func randomRange(rng *rand.Rand) string {
	ts := func() string {
		v := rng.Int63n(4*sec+1) - 2*sec
		v -= v % 250_000_000
		v += rng.Int63n(3) - 1
		if v < 0 {
			return fmt.Sprintf("-%d:%d", (-v)/sec, (-v)%sec)
		}
		return fmt.Sprintf("%d:%d", v/sec, v%sec)
	}
	open := []string{"[", "(", ""}[rng.Intn(3)]
	closing := []string{"]", ")", ""}[rng.Intn(3)]
	switch rng.Intn(10) {
	case 0:
		return "()"
	case 1:
		return "_"
	case 2:
		return open + ts() + "_"
	case 3:
		return "_" + ts() + closing
	case 4:
		return "[" + ts() + "]"
	default:
		return open + ts() + "_" + ts() + closing
	}
}
