package fangort

import (
	"math"
	"testing"
)

// The float formatting golden table: ECMA-262 Number::toString semantics
// (Elm's String.fromFloat). The MVP acceptance value 12.56636 lives here.
func TestShowFloat(t *testing.T) {
	// Computed at runtime: Go constant arithmetic is exact and would fold
	// 0.1 + 0.2 to 0.3 — the very trap fango's elaborator folds around.
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
		if got := ShowFloat(c.in); got != c.want {
			t.Errorf("ShowFloat(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestShowStringLiteral(t *testing.T) {
	cases := []struct{ in, want string }{
		{"hi", `"hi"`},
		{`a"b`, `"a\"b"`},
		{`back\slash`, `"back\\slash"`},
		{"line\nbreak", `"line\nbreak"`},
		{"tab\there", `"tab\there"`},
		{"cr\rhere", `"cr\rhere"`},
		{"bell\x07", `"bell\u{0007}"`},
		{"héllo", `"héllo"`},
	}
	for _, c := range cases {
		if got := ShowStringLiteral(c.in); got != c.want {
			t.Errorf("ShowStringLiteral(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestShowBoolUnit(t *testing.T) {
	if ShowBool(true) != "True" || ShowBool(false) != "False" {
		t.Error("ShowBool wrong")
	}
	if ShowUnit() != "()" {
		t.Error("ShowUnit wrong")
	}
}
