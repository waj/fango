package native

import (
	"math"
	"testing"
)

// The float formatting golden table: ECMA-262 Number::toString semantics
// (Elm's String.fromFloat). The MVP acceptance value 12.56636 lives here.
func TestFloatToString(t *testing.T) {
	// Computed at runtime: Go constant arithmetic is exact and would fold
	// 0.1 + 0.2 to 0.3 — the very trap Fango's elaborator folds around.
	tenth, fifth := 0.1, 0.2
	cases := []struct {
		in   float64
		want string
	}{
		{3.14159 * 2.0 * 2.0, "12.56636"},
		{1.0, "1"},
		{0.1, "0.1"},
		{tenth + fifth, "0.30000000000000004"},
		{-2.5, "-2.5"},
		{1.0 / 3.0, "0.3333333333333333"},
		{1e21, "1e+21"},
		{1e-6, "0.000001"},
		{1e-7, "1e-7"},
		{5e-324, "5e-324"},
		{math.Copysign(0, -1), "0"},
		{0.0, "0"},
		{1.2345678901234568e20, "123456789012345680000"},
		{100.0, "100"},
		{1234.5, "1234.5"},
		{-1e-7, "-1e-7"},
		{math.Inf(1), "Infinity"},
		{math.Inf(-1), "-Infinity"},
		{math.NaN(), "NaN"},
		{math.MaxFloat64, "1.7976931348623157e+308"},
	}
	for _, c := range cases {
		if got := FloatToString(c.in); got != c.want {
			t.Errorf("FloatToString(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStringLiteral(t *testing.T) {
	cases := []struct{ in, want string }{
		{"hi", `"hi"`},
		{`a"b`, `"a\"b"`},
		{`back\slash`, `"back\\slash"`},
		{"line\nbreak", `"line\nbreak"`},
		{"tab\there", `"tab\there"`},
		{"cr\rhere", `"cr\rhere"`},
		{"bell\x07", `"bell\u{0007}"`},
		{"héllo", `"héllo"`},
		{"#{value}", `"\#{value}"`},
		{`\#{value}`, `"\\\#{value}"`},
		{"#plain {", `"#plain {"`},
	}
	for _, c := range cases {
		if got := StringLiteral(c.in); got != c.want {
			t.Errorf("StringLiteral(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestLiteralAppends(t *testing.T) {
	strings := []string{"", "plain", "二😀", "\"'\\\n\t\r", "#{x}##{y}", "\x00\x01\x1f\x7f"}
	for _, s := range strings {
		base := BufferAppend(nil, 0, "prefix:")
		built := BufferAppendStringLiteral(base, 7, s)
		want := "prefix:" + StringLiteral(s)
		if got := BufferText(built, BufferLength(built)); got != want {
			t.Fatalf("%q: %q != %q", s, got, want)
		}
		branch := BufferAppendStringLiteral(base, 7, "other")
		if BufferText(base, 7) != "prefix:" || BufferText(branch, BufferLength(branch)) != `prefix:"other"` {
			t.Fatal("literal append changed prefix")
		}
	}
	for _, tc := range []struct {
		r    rune
		want string
	}{
		{'\'', `'\''`}, {'"', `'\"'`}, {'\\', `'\\'`}, {'\n', `'\n'`},
		{0, `'\u{0000}'`}, {'二', `'二'`}, {'😀', `'😀'`}, {'#', `'#'`},
	} {
		if got := CharLiteral(tc.r); got != tc.want {
			t.Fatalf("%q: %q != %q", tc.r, got, tc.want)
		}
		b := BufferAppendCharLiteral(nil, 0, tc.r)
		if got := BufferText(b, BufferLength(b)); got != tc.want {
			t.Fatalf("append %q: %q != %q", tc.r, got, tc.want)
		}
	}
}

var literalResult string

func TestDirectStringLiteralAllocations(t *testing.T) {
	// Direct show should allocate only its result, including when escaping
	// expands the text past the builder's initial storage capacity.
	text := "quoted \"text\"\nwith #{interpolation} and non-ASCII 二 plus a longer suffix"
	if allocs := testing.AllocsPerRun(100, func() { literalResult = StringLiteral(text) }); allocs != 1 {
		t.Fatalf("direct string literal allocated %g times, want 1", allocs)
	}
}
