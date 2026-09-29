package native

import (
	"math"
	"testing"
)

func TestStringNatives(t *testing.T) {
	if got := Length(""); got != 0 {
		t.Errorf("Length(\"\") = %d", got)
	}
	if got := Length("hello"); got != 5 {
		t.Errorf("Length(\"hello\") = %d", got)
	}
	if got := Length("二"); got != 1 {
		t.Errorf("Length(\"二\") = %d", got)
	}
	cases := []struct {
		i    int64
		s    string
		want int64
	}{
		{0, "A9", 'A'},
		{1, "A9", '9'},
		{2, "A9", -1},
		{-1, "A9", -1},
		{0, "", -1},
		{0, "二", 0xE4},
	}
	for _, c := range cases {
		if got := ByteAt(c.i, c.s); got != c.want {
			t.Errorf("ByteAt(%d, %q) = %d, want %d", c.i, c.s, got, c.want)
		}
	}
	if got := Slice(1, 2, "a二z"); got != "二" {
		t.Errorf("Slice over Unicode scalars = %q, want %q", got, "二")
	}
	if got := ByteLength("二"); got != 3 {
		t.Errorf("ByteLength(二) = %d, want 3", got)
	}
	if first, rest := FirstChar("λ二"), RestString("λ二"); first != 'λ' || rest != "二" {
		t.Errorf("String first/rest = %q, %q", first, rest)
	}
	if got := Slice(-2, 99, "a二z"); got != "a二z" {
		t.Errorf("clamped Slice = %q", got)
	}
	for _, c := range []struct {
		text string
		want float64
	}{
		{"0", 0},
		{"-0", math.Copysign(0, -1)},
		{"+17", 17},
		{"1.25", 1.25},
		{"1e3", 1000},
		{"1.0E-2", 0.01},
		{"5e-324", math.SmallestNonzeroFloat64},
		{"1.7976931348623157e308", math.MaxFloat64},
	} {
		if got := ToFloatNative(c.text); got != c.want || (c.text == "-0" && !math.Signbit(got)) {
			t.Errorf("ToFloatNative(%q) = %v, want %v", c.text, got, c.want)
		}
	}
	for _, invalid := range []string{"", "+", ".5", "1.", "1e", " 1", "1 ", "NaN", "Infinity", "0x1p2", "1e309", "1e-4000"} {
		if got := ToFloatNative(invalid); !math.IsNaN(got) {
			t.Errorf("ToFloatNative(%q) = %v, want NaN sentinel", invalid, got)
		}
	}
}

func TestEntropySeed(t *testing.T) {
	// Two entropy seeds colliding is astronomically unlikely.
	if EntropySeed() == EntropySeed() {
		t.Error("EntropySeed returned the same seed twice")
	}
}
