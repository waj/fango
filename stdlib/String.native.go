package native

import (
	"math"
	"strconv"
	"unicode/utf8"
)

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

// ToFloatNative accepts the decimal grammar exposed by String.toFloat. NaN is
// a private failure sentinel: the grammar deliberately has no spelling for it.
func ToFloatNative(text string) float64 {
	if !decimalFloat(text) {
		return math.NaN()
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsInf(value, 0) || value == 0 && nonzeroMantissa(text) {
		return math.NaN()
	}
	return value
}

func nonzeroMantissa(text string) bool {
	for i := 0; i < len(text) && text[i] != 'e' && text[i] != 'E'; i++ {
		if text[i] >= '1' && text[i] <= '9' {
			return true
		}
	}
	return false
}

func decimalFloat(text string) bool {
	i := 0
	if i < len(text) && (text[i] == '+' || text[i] == '-') {
		i++
	}
	start := i
	for i < len(text) && text[i] >= '0' && text[i] <= '9' {
		i++
	}
	if i == start {
		return false
	}
	if i < len(text) && text[i] == '.' {
		i++
		start = i
		for i < len(text) && text[i] >= '0' && text[i] <= '9' {
			i++
		}
		if i == start {
			return false
		}
	}
	if i < len(text) && (text[i] == 'e' || text[i] == 'E') {
		i++
		if i < len(text) && (text[i] == '+' || text[i] == '-') {
			i++
		}
		start = i
		for i < len(text) && text[i] >= '0' && text[i] <= '9' {
			i++
		}
		if i == start {
			return false
		}
	}
	return i == len(text)
}
