package native

import "unicode/utf8"

// Length returns the number of Unicode scalar values in a String.
func Length(s string) int64 { return int64(utf8.RuneCountInString(s)) }

func ByteLength(s string) int64 { return int64(len(s)) }

// ByteAt returns the byte at a 0-based index, or -1 when it is out of range.
func ByteAt(i int64, s string) int64 {
	if i < 0 || i >= int64(len(s)) {
		return -1
	}
	return int64(s[i])
}

func ByteSlice(start, end int64, s string) string { return s[start:end] }

// Slice uses clamped half-open Unicode-scalar indices.
func Slice(start, end int64, s string) string {
	runes := []rune(s)
	if start < 0 {
		start = 0
	}
	if end < 0 {
		end = 0
	}
	if start > int64(len(runes)) {
		start = int64(len(runes))
	}
	if end > int64(len(runes)) {
		end = int64(len(runes))
	}
	if end <= start {
		return ""
	}
	return string(runes[start:end])
}

func FirstChar(s string) rune { r, _ := utf8.DecodeRuneInString(s); return r }

func RestString(s string) string { _, n := utf8.DecodeRuneInString(s); return s[n:] }

func FromChar(r rune) string { return string(r) }
