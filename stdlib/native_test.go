package native

import (
	"math"
	"testing"
	"unicode/utf8"
)

func TestJSONStrings(t *testing.T) {
	cases := []struct {
		value, encoded string
	}{
		{"plain", `"plain"`},
		{`quote " and slash \`, `"quote \" and slash \\"`},
		{"line\n\tend", `"line\n\tend"`},
		{"λ二", `"λ二"`},
	}
	for _, tc := range cases {
		if got := JsonString(tc.value); got != tc.encoded {
			t.Errorf("JsonString(%q) = %q, want %q", tc.value, got, tc.encoded)
		}
		if got := StringTokenValue(tc.encoded); got != tc.value {
			t.Errorf("StringTokenValue(%q) = %q, want %q", tc.encoded, got, tc.value)
		}
		if got := StringTokenLength(tc.encoded + "tail"); got != int64(utf8.RuneCountInString(tc.encoded)) {
			t.Errorf("StringTokenLength(%q) = %d", tc.encoded+"tail", got)
		}
	}

	if got := StringTokenValue(`"\ud83d\ude00"`); got != "😀" {
		t.Errorf("surrogate pair decoded as %q", got)
	}
	for _, invalid := range []string{"", "plain", `"unterminated`, `"bad\q"`, "\"line\nbreak\""} {
		if got := StringTokenLength(invalid); got != -1 {
			t.Errorf("StringTokenLength(%q) = %d, want -1", invalid, got)
		}
	}
}

func TestJSONFloat(t *testing.T) {
	for _, tc := range []struct {
		value float64
		want  string
	}{
		{0, "0"},
		{-2.5, "-2.5"},
		{1e21, "1e+21"},
	} {
		if got := JsonFloat(tc.value); got != tc.want {
			t.Errorf("JsonFloat(%v) = %q, want %q", tc.value, got, tc.want)
		}
	}

	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("JsonFloat(%v) did not panic", value)
				}
			}()
			JsonFloat(value)
		}()
	}
}

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
}

func TestEntropySeed(t *testing.T) {
	// Two entropy seeds colliding is astronomically unlikely.
	if EntropySeed() == EntropySeed() {
		t.Error("EntropySeed returned the same seed twice")
	}
}
