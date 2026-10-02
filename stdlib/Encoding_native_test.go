package native

import (
	"testing"
	"unicode/utf8"
)

func TestUtf8CodeAt(t *testing.T) {
	for _, char := range []rune{0, 'a', 'é', '二', '😀', utf8.RuneError, 0x10FFFF} {
		encoded := []byte(string(char))
		for length := 1; length < len(encoded); length++ {
			if got := Utf8CodeAt(encoded[:length], 0); got != -3 {
				t.Errorf("prefix %x: got %d, want incomplete", encoded[:length], got)
			}
		}
		if got := Utf8CodeAt(encoded, 0); got != int64(char) {
			t.Errorf("%x: got %d, want %d", encoded, got, char)
		}
		if got := Utf8CodeAt(append([]byte{'a'}, encoded...), 1); got != int64(char) {
			t.Errorf("offset decoding %x: got %d", encoded, got)
		}
	}
	for _, bytes := range [][]byte{
		{0x80}, {0xFF}, {0xC0}, {0xC1, 0x80}, {0xE0, 0x80},
		{0xED, 0xA0}, {0xF0, 0x80}, {0xF4, 0x90}, {0xF5},
		{0xC2, 'a'}, {0xE2, 0x82, 'a'},
	} {
		if got := Utf8CodeAt(bytes, 0); got != -2 {
			t.Errorf("invalid prefix %x: got %d, want invalid", bytes, got)
		}
	}
	if got := Utf8CodeAt(nil, 0); got != -1 {
		t.Errorf("empty input: got %d, want end", got)
	}
	if got := Utf8CodeAt([]byte{'a'}, 1); got != -1 {
		t.Errorf("past end: got %d, want end", got)
	}
}

func TestUtf8MatchAt(t *testing.T) {
	bytes := []byte(`{"café":1}`)
	for _, c := range []struct {
		offset int64
		text   string
		want   bool
	}{
		{1, `"café"`, true}, {2, "café", true}, {1, `"cafe"`, false}, {0, "", true},
		{int64(len(bytes)), "", true}, {int64(len(bytes)), "}", false}, {9, "}}", false},
		{-1, "", false}, {int64(len(bytes)) + 1, "", false},
	} {
		if got := Utf8MatchAt(bytes, c.offset, c.text); got != c.want {
			t.Errorf("Utf8MatchAt(%d, %q) = %v, want %v", c.offset, c.text, got, c.want)
		}
	}
}
