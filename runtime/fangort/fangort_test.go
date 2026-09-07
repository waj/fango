package fangort

import (
	"bufio"
	"errors"
	"io"
	"math"
	"strings"
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

func TestStringLengthByteAt(t *testing.T) {
	if got := StringLength(""); got != 0 {
		t.Errorf("StringLength(\"\") = %d", got)
	}
	if got := StringLength("hello"); got != 5 {
		t.Errorf("StringLength(\"hello\") = %d", got)
	}
	// Byte semantics: one CJK character is three UTF-8 bytes.
	if got := StringLength("二"); got != 3 {
		t.Errorf("StringLength(\"二\") = %d", got)
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
	if got := StringSlice(1, 4, "a二z"); got != "二" {
		t.Errorf("StringSlice over UTF-8 bytes = %q, want %q", got, "二")
	}
}

func TestRandom(t *testing.T) {
	old := RandomSwap(42)
	defer RandomSwap(old)

	first := []int64{RandomInt(1, 100), RandomInt(1, 100), RandomInt(1, 100)}
	if got := RandomSwap(42); got == 42 {
		t.Fatal("state did not advance across draws")
	}
	second := []int64{RandomInt(1, 100), RandomInt(1, 100), RandomInt(1, 100)}
	if first[0] != second[0] || first[1] != second[1] || first[2] != second[2] {
		t.Errorf("same seed gave %v then %v", first, second)
	}

	RandomSwap(7)
	for range 1000 {
		if v := RandomInt(1, 6); v < 1 || v > 6 {
			t.Fatalf("RandomInt(1, 6) = %d out of range", v)
		}
		if v := RandomInt(6, 1); v < 1 || v > 6 {
			t.Fatalf("RandomInt(6, 1) = %d out of range", v)
		}
		if v := RandomInt(-3, 3); v < -3 || v > 3 {
			t.Fatalf("RandomInt(-3, 3) = %d out of range", v)
		}
	}
	if v := RandomInt(5, 5); v != 5 {
		t.Errorf("RandomInt(5, 5) = %d", v)
	}

	RandomSwap(1)
	if prev := RandomSwap(9); prev != 1 {
		t.Errorf("RandomSwap returned %d, want the previous state 1", prev)
	}

	// Two entropy seeds colliding is astronomically unlikely.
	e1 := RandomEntropy(UnitValue)
	e2 := RandomEntropy(UnitValue)
	if e1 == e2 {
		t.Error("RandomEntropy returned the same seed twice")
	}
}

func TestReadLineFrom(t *testing.T) {
	cases := []struct {
		name, input, first, second string
	}{
		{"lf", "one\ntwo\n", "one", "two"},
		{"crlf", "one\r\ntwo\r\n", "one", "two"},
		{"unterminated", "last", "last", ""},
		{"clean eof", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := bufio.NewReader(strings.NewReader(tc.input))
			got, err := ReadLineFrom(r)
			if err != nil || got != tc.first {
				t.Fatalf("first read = %q, %v; want %q, nil", got, err, tc.first)
			}
			got, err = ReadLineFrom(r)
			if err != nil || got != tc.second {
				t.Fatalf("second read = %q, %v; want %q, nil", got, err, tc.second)
			}
		})
	}

	want := errors.New("broken input")
	r := bufio.NewReader(io.MultiReader(strings.NewReader("partial"), errorReader{want}))
	if got, err := ReadLineFrom(r); !errors.Is(err, want) || got != "" {
		t.Fatalf("non-EOF read = %q, %v; want empty string and %v", got, err, want)
	}
}

func TestRawLineAndEOF(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("one\r\ntwo\nlast"))
	for _, want := range []struct {
		raw, text, ending string
	}{
		{"one\r\n", "one", "\r\n"},
		{"two\n", "two", "\n"},
		{"last", "last", ""},
	} {
		has, err := HasInputFrom(r)
		if err != nil || !has {
			t.Fatalf("HasInputFrom = %v, %v; want true, nil", has, err)
		}
		raw, err := ReadRawLineFrom(r)
		if err != nil || raw != want.raw {
			t.Fatalf("ReadRawLineFrom = %q, %v; want %q, nil", raw, err, want.raw)
		}
		if text, ending := LineText(raw), LineEnding(raw); text != want.text || ending != want.ending {
			t.Fatalf("split %q = (%q, %q); want (%q, %q)", raw, text, ending, want.text, want.ending)
		}
	}
	if has, err := HasInputFrom(r); err != nil || has {
		t.Fatalf("clean EOF = %v, %v; want false, nil", has, err)
	}

	wantErr := errors.New("broken input")
	broken := bufio.NewReader(errorReader{wantErr})
	if has, err := HasInputFrom(broken); has || !errors.Is(err, wantErr) {
		t.Fatalf("broken input = %v, %v; want false, %v", has, err, wantErr)
	}
}

func TestWriteStringTo(t *testing.T) {
	var out strings.Builder
	if err := WriteStringTo(&out, "one"); err != nil {
		t.Fatal(err)
	}
	if err := WriteStringTo(&out, " 二"); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "one 二" {
		t.Fatalf("WriteStringTo output = %q", got)
	}

	want := errors.New("broken output")
	if err := WriteStringTo(errorWriter{want}, "x"); !errors.Is(err, want) {
		t.Fatalf("WriteStringTo error = %v, want %v", err, want)
	}
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

type errorWriter struct{ err error }

func (w errorWriter) Write([]byte) (int, error) { return 0, w.err }
